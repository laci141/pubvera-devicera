package cli

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
)

// /api/devices costs two OpenFDA UDI legs (1.8-5.4 s measured; 4.35 s cold for
// "pacemaker"). devicesGroup caches the finished response BODY per device and
// collapses identical in-flight requests into one upstream run, the same way
// synthGroup does for the dossier. A complete result is fresh for
// devicesCacheTTL and then stale-but-servable up to devicesStaleTTL: the stale
// body is returned at once and ONE background refresh replaces it.
const (
	devicesCacheTTL = 15 * time.Minute
	// devicesStaleTTL is the total age up to which a complete result may still be
	// served (stale) while it is refreshed in the background.
	devicesStaleTTL = 24 * time.Hour
	// devicesShortTTL applies to a response marked rate_limited or partial: it is
	// missing rows through no fault of the device, so it is fresh for a minute and
	// never served stale.
	devicesShortTTL = 60 * time.Second
	devicesMaxItems = 32
	devicesRunLimit = 90 * time.Second
)

// X-Cache values.
const (
	cacheHit   = "HIT"
	cacheStale = "STALE"
	cacheMiss  = "MISS"
)

func init() { cliutil.UpstreamLogger = func() *slog.Logger { return reqLog } }

// devicesNow is the clock for the cache TTLs; tests replace it.
var devicesNow = time.Now

// devicesResult is one finished /api/devices answer. body is immutable once
// stored; every request writes its own copy of the bytes to the wire.
type devicesResult struct {
	status int
	body   []byte
	// ttl > 0 means the result is fresh for that long. Zero (errors, any
	// non-200) means it is never cached.
	ttl time.Duration
	// stale > 0 is the total age up to which the result may still be served
	// while a background refresh runs. Zero means never served stale.
	stale time.Duration
}

// lifetime is how long a finished result stays in the map at all.
func (r devicesResult) lifetime() time.Duration {
	if r.stale > r.ttl {
		return r.stale
	}
	return r.ttl
}

type devicesEntry struct {
	done    chan struct{}
	res     devicesResult
	created time.Time
	readyAt time.Time
	// refreshing is true while a background refresh of this entry runs.
	refreshing bool
	// retryAt delays the next background refresh after a failed one, so a
	// rate-limited upstream is not hit on every stale read.
	retryAt time.Time
}

type devicesGroup struct {
	mu      sync.Mutex
	entries map[string]*devicesEntry
}

var sharedDevices = &devicesGroup{entries: make(map[string]*devicesEntry)}

// Do returns the result for key and how it was served (HIT, STALE or MISS),
// running run at most once per key at a time.
func (g *devicesGroup) Do(ctx context.Context, key string,
	run func(context.Context) devicesResult,
) (devicesResult, string, error) {
	g.mu.Lock()
	e, reuse := g.entries[key], false
	if e != nil {
		select {
		case <-e.done:
			age := devicesNow().Sub(e.readyAt)
			switch {
			case e.res.ttl > 0 && age < e.res.ttl:
				res := e.res
				g.mu.Unlock()
				return res, cacheHit, nil
			case e.res.ttl > 0 && age < e.res.stale:
				res := e.res
				if !e.refreshing && !devicesNow().Before(e.retryAt) {
					e.refreshing = true
					g.refreshAsync(key, e, run)
				}
				g.mu.Unlock()
				return res, cacheStale, nil
			}
		default:
			reuse = true // in flight — join it
		}
	}
	if !reuse {
		e = &devicesEntry{done: make(chan struct{}), created: devicesNow()}
		g.evictLocked(key)
		g.entries[key] = e
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), devicesRunLimit)
		entry := e
		go func() {
			defer cancel()
			res := run(runCtx)
			g.mu.Lock()
			entry.res, entry.readyAt = res, devicesNow()
			if res.ttl <= 0 {
				// Never cache: drop it so the next caller retries (waiters
				// already hold the entry and still get this result).
				if g.entries[key] == entry {
					delete(g.entries, key)
				}
			}
			g.mu.Unlock()
			close(entry.done)
		}()
	}
	g.mu.Unlock()

	select {
	case <-e.done:
		return e.res, cacheMiss, nil
	case <-ctx.Done():
		return devicesResult{}, "", ctx.Err()
	}
}

// refreshAsync re-runs key in the background on a detached context. Only a
// complete result replaces the stale entry; a failure or a degraded
// (partial / rate_limited) answer leaves it unchanged. Called with g.mu held.
func (g *devicesGroup) refreshAsync(key string, e *devicesEntry, run func(context.Context) devicesResult) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), devicesRunLimit)
		defer cancel()
		res := run(ctx)
		g.mu.Lock()
		defer g.mu.Unlock()
		e.refreshing = false
		if res.status == http.StatusOK && res.ttl > 0 && res.stale > 0 {
			e.res, e.readyAt, e.retryAt = res, devicesNow(), time.Time{}
			return
		}
		e.retryAt = devicesNow().Add(devicesShortTTL)
	}()
}

// evictLocked makes room for a new entry under key: expired finished entries go
// first, then the oldest entry while the map is at devicesMaxItems.
func (g *devicesGroup) evictLocked(key string) {
	now := devicesNow()
	for k, v := range g.entries {
		select {
		case <-v.done:
			if now.Sub(v.readyAt) >= v.res.lifetime() {
				delete(g.entries, k)
			}
		default:
		}
	}
	for len(g.entries) >= devicesMaxItems {
		oldest, first := "", true
		var at time.Time
		for k, v := range g.entries {
			if k == key {
				continue
			}
			if first || v.created.Before(at) {
				oldest, at, first = k, v.created, false
			}
		}
		if first {
			return
		}
		delete(g.entries, oldest)
	}
}
