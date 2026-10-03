package intelligence

import (
	"context"
	"maps"
	"sync"
	"time"
)

const (
	// baselineTTL is how long a successful device-independent baseline is reused.
	// They are global MAUDE statistics that do not move within hours.
	baselineTTL = 6 * time.Hour
	// baselineRunLimit bounds the detached lookup so a wedged upstream cannot pin
	// an entry, and its goroutine, open forever.
	baselineRunLimit = 90 * time.Second
)

// baselineCache runs at most one lookup per key at a time and keeps a successful
// answer for baselineTTL. Callers that arrive while a lookup is in flight join it
// and share its result, error included. A failure is never kept. The zero value
// is ready to use. Same pattern as dailyCache in internal/sources.
type baselineCache struct {
	mu      sync.Mutex
	entries map[string]*baselineEntry
	now     func() time.Time // nil = time.Now; tests replace it
}

// baselineEntry is one key's lookup: in flight until done is closed, then holding
// the result. val, err and readyAt are written once, under the cache mutex,
// before done is closed.
type baselineEntry struct {
	done    chan struct{}
	val     any
	err     error
	empty   bool // answered, but with nothing: returned to the callers, never kept
	readyAt time.Time
}

// baselines is the process-wide cache behind GlobalEventTypeCounts and
// VolumeBaseline. Neither takes the device term, so the key is a fixed name.
var baselines = &baselineCache{}

func (c *baselineCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// reset drops every entry (tests).
func (c *baselineCache) reset() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

// ResetBaselinesForTest empties the process-wide baseline cache so a test in
// another package starts cold. Production code never calls it.
func ResetBaselinesForTest() { baselines.reset() }

func (e *baselineEntry) finished() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// cachedBaseline returns the cached value for key, joins a lookup in flight, or
// starts one. The lookup is detached from the first caller's context: if that
// caller goes away, the callers still waiting must not inherit the cancellation.
// A timeout puts a ceiling back on. Only the waiting is tied to ctx. An error or
// an empty answer (isEmpty) is handed to the callers of that flight only; the
// next call asks upstream again.
func cachedBaseline[T any](c *baselineCache, ctx context.Context, key string, isEmpty func(T) bool, fetch func(context.Context) (T, error)) (T, error) {
	var zero T
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*baselineEntry)
	}
	e := c.entries[key]
	if e != nil && e.finished() && (e.err != nil || e.empty || c.clock().Sub(e.readyAt) >= baselineTTL) {
		delete(c.entries, key)
		e = nil
	}
	if e == nil {
		e = &baselineEntry{done: make(chan struct{})}
		c.entries[key] = e
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), baselineRunLimit)
		go func() {
			defer cancel()
			v, err := fetch(runCtx)
			c.mu.Lock()
			e.val, e.err, e.readyAt = v, err, c.clock()
			e.empty = err == nil && isEmpty(v)
			c.mu.Unlock()
			close(e.done)
		}()
	}
	c.mu.Unlock()

	select {
	case <-e.done:
		if e.err != nil {
			return zero, e.err
		}
		return e.val.(T), nil
	case <-ctx.Done():
		// This caller gave up; the lookup continues for whoever else waits.
		return zero, ctx.Err()
	}
}

func copyCounts(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	return maps.Clone(m)
}

// PrewarmBaselines fills both baseline entries so the first search after a start
// does not pay for them. Both lookups run concurrently; the first error is
// returned after both have ended. Failures are not cached (see cachedBaseline).
func PrewarmBaselines(ctx context.Context) error {
	d := NewLiveData()
	errs := make(chan error, 2)
	go func() {
		_, err := d.GlobalEventTypeCounts(ctx)
		errs <- err
	}()
	go func() {
		_, _, err := d.VolumeBaseline(ctx)
		errs <- err
	}()
	var first error
	for range 2 {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
	}
	return first
}
