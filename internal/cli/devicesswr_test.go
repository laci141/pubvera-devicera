package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// getFull is get() plus the response headers.
func (g *devicesRig) getFull(device string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handleDevices(rec, httptest.NewRequest(http.MethodGet, "/api/devices?device="+url.QueryEscape(device), nil))
	return rec
}

// waitHits waits until the upstream request count reaches n.
func (g *devicesRig) waitHits(t *testing.T, n int) {
	t.Helper()
	waitFor(t, "upstream hits", func() bool { return g.hits() >= n })
}

func TestSWRFreshHitNoUpstream(t *testing.T) {
	g := newDevicesRig(t, 0, never, 0)
	if x := g.getFull("swr fresh").Header().Get("X-Cache"); x != "MISS" {
		t.Errorf("first call X-Cache=%q want MISS", x)
	}
	h := g.hits()
	g.advance(devicesCacheTTL - time.Second)
	rec := g.getFull("swr fresh")
	if x := rec.Header().Get("X-Cache"); x != "HIT" {
		t.Errorf("fresh call X-Cache=%q want HIT", x)
	}
	if g.hits() != h {
		t.Errorf("fresh hit reached upstream: %d -> %d", h, g.hits())
	}
}

func TestSWRFreshWindowIs15Minutes(t *testing.T) {
	if devicesCacheTTL != 15*time.Minute {
		t.Errorf("devicesCacheTTL=%v want 15m", devicesCacheTTL)
	}
	if devicesStaleTTL != 24*time.Hour {
		t.Errorf("devicesStaleTTL=%v want 24h", devicesStaleTTL)
	}
}

func TestSWRStaleReturnsImmediatelyAndRefreshesOnce(t *testing.T) {
	g := newDevicesRig(t, 0, never, 0)
	_, old := g.get("swr stale")
	h := g.hits()
	g.advance(devicesCacheTTL + time.Minute)

	// Make the refresh slow so an in-line wait would be visible.
	http.DefaultTransport.(*slowStub).delay = 400 * time.Millisecond
	start := time.Now()
	rec := g.getFull("swr stale")
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Errorf("stale read took %v: it waited for the refresh", el)
	}
	if x := rec.Header().Get("X-Cache"); x != "STALE" {
		t.Errorf("X-Cache=%q want STALE", x)
	}
	if rec.Body.String() != old {
		t.Errorf("stale body differs from the old body")
	}
	g.waitHits(t, h+2) // one brand request + one category page
	time.Sleep(600 * time.Millisecond)
	if g.hits() != h+2 {
		t.Errorf("refresh made %d upstream requests, want 2", g.hits()-h)
	}
}

func TestSWRConcurrentStaleReadsStartOneRefresh(t *testing.T) {
	g := newDevicesRig(t, 0, never, 0)
	g.get("swr conc")
	h := g.hits()
	g.advance(devicesCacheTTL + time.Minute)
	http.DefaultTransport.(*slowStub).delay = 200 * time.Millisecond
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); g.getFull("swr conc") }()
	}
	wg.Wait()
	time.Sleep(700 * time.Millisecond)
	if got := g.hits() - h; got != 2 {
		t.Errorf("8 concurrent stale reads made %d upstream requests, want 2 (one refresh)", got)
	}
}

func TestSWRFailedRefreshKeepsStale(t *testing.T) {
	var failing struct {
		sync.Mutex
		on bool
	}
	fail := func(*http.Request) bool { failing.Lock(); defer failing.Unlock(); return failing.on }
	g := newDevicesRig(t, http.StatusTooManyRequests, fail, 0)
	_, old := g.get("swr keep")
	h := g.hits()
	failing.Lock()
	failing.on = true
	failing.Unlock()
	g.advance(devicesCacheTTL + time.Minute)
	g.get("swr keep") // STALE, triggers the failing refresh
	g.waitHits(t, h+2)
	time.Sleep(100 * time.Millisecond)
	rec := g.getFull("swr keep")
	if x := rec.Header().Get("X-Cache"); x != "STALE" {
		t.Errorf("after a failed refresh X-Cache=%q want STALE (entry kept)", x)
	}
	if rec.Body.String() != old {
		t.Errorf("after a failed refresh the old body was not kept")
	}
}

func TestSWRPartialNeverServedStaleAfter60s(t *testing.T) {
	g := newDevicesRig(t, http.StatusTooManyRequests, failLeg("100"), 0)
	_, body := g.get("swr partial")
	if !strings.Contains(body, `"rate_limited":true`) {
		t.Fatalf("setup: expected rate_limited, got %s", body)
	}
	h := g.hits()
	g.advance(devicesShortTTL + time.Second)
	rec := g.getFull("swr partial")
	if x := rec.Header().Get("X-Cache"); x != "MISS" {
		t.Errorf("expired partial X-Cache=%q want MISS (never stale)", x)
	}
	if g.hits() == h {
		t.Errorf("expired partial result was served without a new upstream run")
	}
}

func TestSWRErrorHasMissHeaderAndIsNotCached(t *testing.T) {
	g := newDevicesRig(t, http.StatusTooManyRequests, failAll, 0)
	rec := g.getFull("swr err")
	if rec.Code != http.StatusBadGateway || rec.Header().Get("X-Cache") != "MISS" {
		t.Errorf("code=%d X-Cache=%q want 502 MISS", rec.Code, rec.Header().Get("X-Cache"))
	}
}

func TestSWRBeyond24hIsMiss(t *testing.T) {
	g := newDevicesRig(t, 0, never, 0)
	g.get("swr old")
	h := g.hits()
	g.advance(devicesStaleTTL + time.Minute)
	if x := g.getFull("swr old").Header().Get("X-Cache"); x != "MISS" {
		t.Errorf("X-Cache=%q want MISS after 24h", x)
	}
	if g.hits() == h {
		t.Errorf("an entry older than 24h was served without upstream")
	}
}

// ---- gzip ----

func bigJSONHandler(n int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"x":"` + strings.Repeat("a", n) + `"}`))
	})
}

func gzipGet(h http.Handler, path, ae string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if ae != "" {
		req.Header.Set("Accept-Encoding", ae)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGzipBigBodyWithHeader(t *testing.T) {
	rec := gzipGet(withGzip(bigJSONHandler(5000)), "/api/x", "gzip")
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding=%q want gzip", rec.Header().Get("Content-Encoding"))
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Errorf("Vary=%q lacks Accept-Encoding", rec.Header().Get("Vary"))
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if !strings.HasPrefix(string(plain), `{"x":"aaa`) || len(plain) < 5000 {
		t.Errorf("decoded body wrong (len %d)", len(plain))
	}
	if rec.Body.Len() >= len(plain) {
		t.Errorf("compressed %d >= plain %d", rec.Body.Len(), len(plain))
	}
}

func TestGzipOffWithoutHeader(t *testing.T) {
	rec := gzipGet(withGzip(bigJSONHandler(5000)), "/api/x", "")
	if rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("compressed without Accept-Encoding")
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Errorf("Vary=%q lacks Accept-Encoding", rec.Header().Get("Vary"))
	}
	if !strings.HasPrefix(rec.Body.String(), `{"x":"aaa`) {
		t.Errorf("body not plain")
	}
}

func TestGzipSmallBodyNotCompressed(t *testing.T) {
	rec := gzipGet(withGzip(bigJSONHandler(100)), "/api/x", "gzip")
	if rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("small body was compressed")
	}
}

func TestGzipNonAPIPathUntouched(t *testing.T) {
	rec := gzipGet(withGzip(bigJSONHandler(5000)), "/config.json", "gzip")
	if rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("non-/api path was compressed")
	}
}

func TestGzipNoDoubleCompress(t *testing.T) {
	h := withGzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "br")
		_, _ = w.Write([]byte(strings.Repeat("z", 3000)))
	}))
	rec := gzipGet(h, "/api/x", "gzip")
	if rec.Header().Get("Content-Encoding") != "br" || rec.Body.Len() != 3000 {
		t.Errorf("already-encoded body touched: CE=%q len=%d", rec.Header().Get("Content-Encoding"), rec.Body.Len())
	}
}

func TestGzipThroughRealHandlerDevices(t *testing.T) {
	newDevicesRig(t, 0, never, 0)
	req := httptest.NewRequest(http.MethodGet, "/api/devices?device=gz", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	NewServeHandler().ServeHTTP(rec, req)
	// The stub answers an empty result set; its envelope (note + disclaimer) is > 1 KiB.
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("NewServeHandler did not gzip /api/devices (len %d)", rec.Body.Len())
	}
}

var _ = context.Background
