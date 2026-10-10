package cli

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
)

// /api/devices costs two OpenFDA UDI legs (1.8-5.4 s measured). devicesGroup
// caches the finished response BODY per device for a short TTL and collapses
// identical in-flight requests into one upstream run, the same way synthGroup
// does for the dossier.
const (
	devicesCacheTTL = 5 * time.Minute
	// devicesShortTTL applies to a response marked rate_limited or partial: it is
	// missing rows through no fault of the device and must not stay for 5 minutes.
	devicesShortTTL = 60 * time.Second
	devicesMaxItems = 32
	devicesRunLimit = 90 * time.Second
)

func init() { cliutil.UpstreamLogger = func() *slog.Logger { return reqLog } }

// devicesNow is the clock for the cache TTLs; tests replace it.
var devicesNow = time.Now

// devicesResult is one finished /api/devices answer. body is immutable once
// stored; every request writes its own copy of the bytes to the wire.
type devicesResult struct {
	status int
	body   []byte
	// ttl > 0 means the result may be reused for that long. Zero (errors, any
	// non-200) means it is never cached.
	ttl time.Duration
}

type devicesEntry struct {
	done    chan struct{}
	res     devicesResult
	created time.Time
	readyAt time.Time
}

type devicesGroup struct {
	mu      sync.Mutex
	entries map[string]*devicesEntry
}

var sharedDevices = &devicesGroup{entries: make(map[string]*devicesEntry)}

// Do returns the result for key, running run at most once per key per TTL.
func (g *devicesGroup) Do(ctx context.Context, key string,
	run func(context.Context) devicesResult,
) (devicesResult, error) {
	g.mu.Lock()
	e, reuse := g.entries[key], false
	if e != nil {
		select {
		case <-e.done:
			reuse = e.res.ttl > 0 && devicesNow().Sub(e.readyAt) < e.res.ttl
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
		return e.res, nil
	case <-ctx.Done():
		return devicesResult{}, ctx.Err()
	}
}

// evictLocked makes room for a new entry under key: expired finished entries go
// first, then the oldest entry while the map is at devicesMaxItems.
func (g *devicesGroup) evictLocked(key string) {
	now := devicesNow()
	for k, v := range g.entries {
		select {
		case <-v.done:
			if now.Sub(v.readyAt) >= v.res.ttl {
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
