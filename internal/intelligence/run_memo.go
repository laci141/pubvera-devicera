package intelligence

import (
	"context"
	"maps"
	"sync"
)

// runMemo wraps the Data of ONE Synthesize call. EventTypeCounts and RecallTotal
// are asked for by several probes with identical arguments; the memo sends each
// distinct (method, arguments) to upstream once and hands the answer to every
// probe, error included. Probes that arrive while the call is in flight join it.
// Every other method passes straight through to the embedded Data. The memo is
// built per run and dropped with it: nothing is kept across runs.
type runMemo struct {
	Data
	mu      sync.Mutex
	flights map[memoKey]*memoFlight
}

// memoKey is the method plus every argument of the call.
type memoKey struct {
	method string
	device string
}

// memoFlight is one upstream call. counts, total and err are written once before
// done is closed.
type memoFlight struct {
	done   chan struct{}
	counts map[string]int
	total  int
	err    error
}

func newRunMemo(d Data) *runMemo {
	return &runMemo{Data: d, flights: make(map[memoKey]*memoFlight)}
}

// flight returns the flight for key and whether the caller must run it.
func (m *runMemo) flight(key memoKey) (*memoFlight, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.flights[key]; ok {
		return f, false
	}
	f := &memoFlight{done: make(chan struct{})}
	m.flights[key] = f
	return f, true
}

// wait blocks until the flight finishes or this caller's context ends.
func (f *memoFlight) wait(ctx context.Context) error {
	select {
	case <-f.done:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *runMemo) EventTypeCounts(ctx context.Context, device string) (map[string]int, error) {
	f, owner := m.flight(memoKey{"EventTypeCounts", device})
	if owner {
		f.counts, f.err = m.Data.EventTypeCounts(ctx, device)
		close(f.done)
	}
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	return maps.Clone(f.counts), nil
}

func (m *runMemo) RecallTotal(ctx context.Context, device string) (int, error) {
	f, owner := m.flight(memoKey{"RecallTotal", device})
	if owner {
		f.total, f.err = m.Data.RecallTotal(ctx, device)
		close(f.done)
	}
	if err := f.wait(ctx); err != nil {
		return 0, err
	}
	return f.total, nil
}
