package sources

import (
	"context"
	"sync"
	"time"
)

// rateGate spaces outgoing requests to one host so a burst cannot exceed an
// upstream's per-second cap.
//
// It exists because of a real 429. NCBI allows 3 requests/second per IP without
// an API key. A single PubMed Fetch issues at most 2 sequential requests, which
// was safe while the dossier ran its probes one at a time — but the probes now
// run concurrently, and two PubMed-touching probes overlapping means 4 requests
// inside one second. NCBI answered: count 4, limit 3. The probe degraded to a
// note and the dossier came back partial, which is correct behaviour but a poor
// reason to lose a signal.
//
// Deliberately not solved with an API key: a key raises the ceiling to 10/s but
// introduces a secret to manage and would travel in the request URL. The gate
// needs no secret and holds regardless of how the callers are scheduled.
type rateGate struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time // earliest time the next request may start
}

// wait blocks until this caller's slot is due, then returns. Slots are handed
// out in arrival order: each caller reserves the next one under the lock and
// sleeps outside it, so waiting callers never block each other.
//
// A caller that gives up mid-wait leaves its reserved slot unused. That errs
// toward being slightly slower than the cap, which is the safe direction.
func (g *rateGate) wait(ctx context.Context) error {
	g.mu.Lock()
	now := time.Now()
	slot := g.next
	if slot.Before(now) {
		slot = now
	}
	g.next = slot.Add(g.interval)
	g.mu.Unlock()

	d := time.Until(slot)
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ncbiGate paces every E-utilities request in this process.
//
// 350ms, not 333ms: NCBI counts against its own clock and network latency
// varies, so pacing exactly at the limit would still trip it on an unlucky
// second. 350ms gives ~2.85 req/s — under the cap with room to spare.
//
// The cost is small and does not lengthen a dossier run: only two of the eleven
// probes touch PubMed, and they run alongside nine openFDA probes that take
// several seconds each. Serialising four PubMed requests over ~1s fits inside
// that window. openFDA requests are unaffected — they use their own client.
var ncbiGate = &rateGate{interval: 350 * time.Millisecond}
