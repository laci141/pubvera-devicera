package cli

import (
	"context"
	"sync"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/intelligence"
)

// A page load calls /api/dossier and /api/signals at the same moment for the
// same device, and both commands run the identical eleven-probe suite: twenty-
// two probe runs where eleven would do, each pair racing the other for the same
// upstream feeds. synthGroup collapses that into one run.
//
// It does two related things. Requests that arrive while a run is in flight
// JOIN it rather than starting their own — that is the case that matters here,
// because the two requests are simultaneous and a plain cache would still be
// empty when the second one looks. Requests that arrive shortly AFTER a run
// finished reuse its dossier, which also covers a reload or a second tab.
const (
	// synthCacheTTL is how long a finished dossier is reused. Kept short: the
	// dossier is a live read of public records, and a stale one would quietly
	// misreport how much attention a device is getting right now.
	synthCacheTTL = 5 * time.Minute
	// synthRunLimit bounds a detached run so a wedged upstream cannot pin an
	// entry — and its goroutine — open forever.
	synthRunLimit = 90 * time.Second
)

// synthEntry is one device's run: in flight until done is closed, then holding
// the result. Every field other than done is written once, under the group's
// mutex, before done is closed.
type synthEntry struct {
	done    chan struct{}
	dossier *intelligence.IntelligenceDossier
	err     error
	readyAt time.Time
}

type synthGroup struct {
	mu      sync.Mutex
	entries map[string]*synthEntry
}

var sharedSynth = &synthGroup{entries: make(map[string]*synthEntry)}

// logSynthesis records the outcome of one suite run. A probe that fails does
// NOT fail the request: Synthesize turns it into a note and the dossier comes
// back partial with HTTP 200, which is the intended behaviour but also means
// the request log alone can never show it — every line reads 200. This is the
// only place the notes are visible, so this is where they are written down.
//
// Logged per run, not per request: the group runs the suite once per device per
// TTL, so the joining request does not produce a duplicate line.
func logSynthesis(device string, d *intelligence.IntelligenceDossier, err error, took time.Duration) {
	if err != nil {
		reqLog.Error("synthesis",
			"device", clip(device, 80),
			"duration_ms", took.Milliseconds(),
			"error", err.Error())
		return
	}
	if d == nil {
		return
	}
	attrs := []any{
		"device", clip(device, 80),
		"duration_ms", took.Milliseconds(),
		"signals_measured", d.SignalsMeasured,
		"signals_total", len(d.Signals),
		"probes_failed", len(d.Notes),
	}
	// The notes name the probe and carry its error verbatim, e.g.
	// "benchmark/severity-delta unavailable: ...". They are the answer to
	// "which of the eleven dropped out, and why".
	if len(d.Notes) > 0 {
		attrs = append(attrs, "notes", d.Notes)
	}
	reqLog.Info("synthesis", attrs...)
}

// Do returns the dossier for device, running it at most once per device per
// TTL. Callers that arrive during a run block on the same entry.
func (g *synthGroup) Do(ctx context.Context, device string,
	run func(context.Context, string) (*intelligence.IntelligenceDossier, error),
) (*intelligence.IntelligenceDossier, error) {
	g.mu.Lock()
	g.sweepLocked()

	e, reuse := g.entries[device], false
	if e != nil {
		select {
		case <-e.done:
			// Finished. Reuse it only if it succeeded and is still fresh;
			// a failure is never cached, so the next caller retries.
			reuse = e.err == nil && time.Since(e.readyAt) < synthCacheTTL
		default:
			// Still running — join it.
			reuse = true
		}
	}

	if !reuse {
		e = &synthEntry{done: make(chan struct{})}
		g.entries[device] = e
		// The run is detached from the caller's context on purpose: if the
		// first requester's browser tab goes away mid-run, the second one is
		// still waiting on this entry and must not inherit that cancellation.
		// WithoutCancel drops the cancel signal but keeps the context values,
		// and the timeout puts a ceiling back on.
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), synthRunLimit)
		entry := e
		go func() {
			defer cancel()
			start := time.Now()
			d, err := run(runCtx, device)
			g.mu.Lock()
			entry.dossier, entry.err, entry.readyAt = d, err, time.Now()
			g.mu.Unlock()
			close(entry.done)
			logSynthesis(device, d, err, time.Since(start))
		}()
	}
	g.mu.Unlock()

	select {
	case <-e.done:
		return e.dossier, e.err
	case <-ctx.Done():
		// This caller gave up; the run continues for whoever else is waiting.
		return nil, ctx.Err()
	}
}

// sweepLocked drops finished entries past their TTL so the map tracks recent
// devices rather than every term ever searched. Callers hold g.mu.
func (g *synthGroup) sweepLocked() {
	for k, v := range g.entries {
		select {
		case <-v.done:
			if time.Since(v.readyAt) > synthCacheTTL {
				delete(g.entries, k)
			}
		default:
		}
	}
}
