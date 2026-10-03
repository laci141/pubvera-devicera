package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/intelligence"
)

// withSlowDossier installs a canned dossier that takes d to produce, standing in
// for the eleven-probe synthesis.
func withSlowDossier(t *testing.T, d time.Duration) {
	t.Helper()
	old := synthesize
	synthesize = func(context.Context, string) (*intelligence.IntelligenceDossier, error) {
		time.Sleep(d)
		return sampleDossier(), nil
	}
	t.Cleanup(func() { synthesize = old })
}

// metaOf decodes a response body and returns its meta block.
func metaOf(t *testing.T, rec *httptest.ResponseRecorder) (map[string]any, map[string]any) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body.String())
	}
	meta, ok := body["meta"].(map[string]any)
	if !ok {
		t.Fatalf("response has no meta block: %s", rec.Body.String())
	}
	return body, meta
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// T1: on a cold cache the freshness lookup runs while the synthesis runs, so the
// endpoint takes max(synthesis, meta), not their sum.
func TestMetaRunsConcurrentlyWithDispatch(t *testing.T) {
	withSlowDossier(t, 300*time.Millisecond)
	swapLUCache(t, func(context.Context) string {
		time.Sleep(300 * time.Millisecond)
		return "2026-10-03"
	})
	h := NewServeHandler()

	start := time.Now()
	rec := get(h, "/api/dossier?device=x")
	took := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
	}
	if took >= 500*time.Millisecond {
		t.Fatalf("cold-cache response took %v, want < 500ms (max of 300ms and 300ms, not their sum)", took)
	}
	_, meta := metaOf(t, rec)
	if meta["openfda_last_updated"] != "2026-10-03" {
		t.Errorf("openfda_last_updated = %v, want 2026-10-03", meta["openfda_last_updated"])
	}
}

// T2: /api/dossier and /api/signals arrive together on a cold cache and share one
// upstream freshness call.
func TestMetaColdLookupsAreCoalesced(t *testing.T) {
	withSlowDossier(t, 50*time.Millisecond)
	var calls atomic.Int32
	swapLUCache(t, func(context.Context) string {
		calls.Add(1)
		time.Sleep(200 * time.Millisecond)
		return "2026-10-03"
	})
	h := NewServeHandler()

	recs := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i, p := range []string{"/api/dossier?device=x", "/api/signals?device=x"} {
		wg.Go(func() { recs[i] = get(h, p) })
	}
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("freshness stub called %d times for two simultaneous cold requests, want exactly 1", got)
	}
	for i, rec := range recs {
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status=%d: %s", i, rec.Code, rec.Body.String())
		}
		if _, meta := metaOf(t, rec); meta["openfda_last_updated"] != "2026-10-03" {
			t.Errorf("request %d: openfda_last_updated = %v, want the shared result", i, meta["openfda_last_updated"])
		}
	}
}

// T3: a failing lookup (the fetch func reports failure as "") leaves the body and
// status as they are, with an empty openfda_last_updated, and is not cached.
func TestMetaFailureKeepsNormalBody(t *testing.T) {
	withSlowDossier(t, 0)
	var calls atomic.Int32
	swapLUCache(t, func(context.Context) string {
		calls.Add(1)
		return ""
	})
	h := NewServeHandler()

	rec := get(h, "/api/dossier?device=x")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 with an empty openfda_last_updated: %s", rec.Code, rec.Body.String())
	}
	body, meta := metaOf(t, rec)
	if v, ok := meta["openfda_last_updated"]; !ok || v != "" {
		t.Errorf("openfda_last_updated = %#v (present=%v), want \"\"", v, ok)
	}
	if _, ok := meta["sources"]; !ok {
		t.Errorf("meta lost its sources list: %v", meta)
	}
	if body["device"] != "pacemaker" {
		t.Errorf("the dossier body is not the normal one: %s", rec.Body.String())
	}

	// A failure is not cached: the next request asks again.
	get(h, "/api/dossier?device=x")
	if got := calls.Load(); got != 2 {
		t.Errorf("stub called %d times over two requests after a failure, want 2 (failures are not cached)", got)
	}
}

// T4: with a fresh cached value the upstream is not touched.
func TestMetaWarmCacheSkipsUpstream(t *testing.T) {
	withSlowDossier(t, 0)
	var calls atomic.Int32
	swapLUCache(t, func(context.Context) string {
		calls.Add(1)
		return "from-upstream"
	})
	luCache.mu.Lock()
	luCache.val, luCache.at = "2026-09-30", time.Now()
	luCache.mu.Unlock()
	h := NewServeHandler()

	rec := get(h, "/api/dossier?device=x")
	if got := calls.Load(); got != 0 {
		t.Fatalf("freshness stub called %d times on a warm cache, want 0", got)
	}
	if _, meta := metaOf(t, rec); meta["openfda_last_updated"] != "2026-09-30" {
		t.Errorf("openfda_last_updated = %v, want the cached 2026-09-30", meta["openfda_last_updated"])
	}
}

// A requester that goes away must not take the shared lookup down with it: the
// one still waiting gets the result.
func TestMetaCancelledLeaderDoesNotPoisonJoiners(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	swapLUCache(t, func(ctx context.Context) string {
		once.Do(func() { close(started) })
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(200 * time.Millisecond):
			return "2026-10-03"
		}
	})

	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		lastUpdatedCached(leaderCtx)
	}()
	<-started

	joiner := make(chan string, 1)
	go func() { joiner <- lastUpdatedCached(context.Background()) }()
	time.Sleep(20 * time.Millisecond) // let the joiner reach the in-flight lookup
	cancel()
	<-leaderDone

	select {
	case got := <-joiner:
		if got != "2026-10-03" {
			t.Fatalf("joiner got %q after the first requester went away, want 2026-10-03", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("joiner never returned")
	}
}
