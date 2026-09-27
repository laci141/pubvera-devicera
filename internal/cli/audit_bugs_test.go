package cli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/sources"
	"github.com/laci141/medical-device-intelligence/internal/store"
)

// TestAuditBUG04EmptyIDRecordSurfacedInSync: an empty-ID record must not be
// lost silently — it is reported — and the valid records are still stored.
func TestAuditBUG04EmptyIDRecordSurfacedInSync(t *testing.T) {
	withSources(t, map[string]sources.Source{
		"pubmed": fakeSource{name: "pubmed", id: "pmid", recs: []sources.RawRecord{
			{ID: "P-1", Raw: map[string]any{"title": "a"}},
			{ID: "", Raw: map[string]any{"title": "no id"}},
			{ID: "P-2", Raw: map[string]any{"title": "b"}},
		}},
	})
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var stderr bytes.Buffer
	res, err := syncPass(context.Background(), &stderr, st, "pacemaker", "", "", 100, 10)
	if err != nil {
		t.Fatalf("syncPass: %v", err)
	}
	if !strings.Contains(stderr.String(), "empty id") {
		t.Errorf("empty-ID record not surfaced; stderr=%q", stderr.String())
	}
	if res.Total != 2 || res.New != 2 {
		t.Errorf("valid records: new=%d total=%d want 2/2", res.New, res.Total)
	}
}

// TestAuditBUG06PanicDetailNotLeaked: a handler panic yields a generic 500;
// the panic value never reaches the client, and withLogging still logs 500.
func TestAuditBUG06PanicDetailNotLeaked(t *testing.T) {
	const marker = "SECRET-PANIC-MARKER-7f3a /home/app/internal/x.go:42"
	var logBuf bytes.Buffer
	orig := reqLog
	reqLog = slog.New(slog.NewJSONHandler(&logBuf, nil))
	defer func() { reqLog = orig }()

	h := withLogging(withRecovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(marker)
	})))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/x", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "SECRET-PANIC-MARKER") {
		t.Fatalf("panic value leaked to client: %s", rr.Body.String())
	}
	if !strings.Contains(logBuf.String(), `"status":500`) {
		t.Fatalf("request not logged as 500: %s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "SECRET-PANIC-MARKER") {
		t.Fatalf("panic value not logged server-side: %s", logBuf.String())
	}
}

// swapLUCache resets the freshness cache and fetch func, restoring both after.
func swapLUCache(t *testing.T, fetch func(context.Context) string) {
	t.Helper()
	origFetch := fetchOpenFDALastUpdated
	luCache.mu.Lock()
	origVal, origAt := luCache.val, luCache.at
	luCache.val, luCache.at = "", time.Time{}
	luCache.mu.Unlock()
	fetchOpenFDALastUpdated = fetch
	t.Cleanup(func() {
		fetchOpenFDALastUpdated = origFetch
		luCache.mu.Lock()
		luCache.val, luCache.at = origVal, origAt
		luCache.mu.Unlock()
	})
}

// TestAuditBUG08FetchDoesNotHoldCacheMutex: while one caller is blocked in the
// upstream fetch, the cache mutex must be free.
func TestAuditBUG08FetchDoesNotHoldCacheMutex(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	swapLUCache(t, func(context.Context) string {
		close(started)
		<-release
		return "2026-09-01"
	})
	done := make(chan string)
	go func() { done <- lastUpdatedCached(context.Background()) }()

	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("fetch never started")
	}
	locked := luCache.mu.TryLock()
	if locked {
		luCache.mu.Unlock()
	}
	close(release)
	if got := <-done; got != "2026-09-01" {
		t.Fatalf("got %q", got)
	}
	if !locked {
		t.Fatal("cache mutex held during upstream fetch")
	}
}

// TestAuditBUG08CacheSemantics: a fresh value skips the fetch; a failed fetch
// is not cached as success.
func TestAuditBUG08CacheSemantics(t *testing.T) {
	calls := 0
	result := ""
	swapLUCache(t, func(context.Context) string { calls++; return result })

	if got := lastUpdatedCached(context.Background()); got != "" || calls != 1 {
		t.Fatalf("failed fetch: got %q calls %d", got, calls)
	}
	result = "2026-09-02"
	if got := lastUpdatedCached(context.Background()); got != "2026-09-02" || calls != 2 {
		t.Fatalf("retry after failure: got %q calls %d", got, calls)
	}
	result = "changed"
	if got := lastUpdatedCached(context.Background()); got != "2026-09-02" || calls != 2 {
		t.Fatalf("fresh value: got %q calls %d", got, calls)
	}
}

// TestAuditBUG09SinceRejectsImpossibleDates: --since must be a real date.
func TestAuditBUG09SinceRejectsImpossibleDates(t *testing.T) {
	for _, c := range []struct {
		in string
		ok bool
	}{
		{"20260101", true}, {"20240229", true},
		{"20261399", false}, {"20261301", false}, {"20260199", false},
		{"20260230", false}, {"2026011", false}, {"2026-1-1", false},
	} {
		if got := validSince(c.in); got != c.ok {
			t.Errorf("validSince(%q) = %v, want %v", c.in, got, c.ok)
		}
	}
}

// failingCounter is an event source whose field count fails with err.
type failingCounter struct{ fakeSource }

func (f failingCounter) CountField(context.Context, sources.Query, string) (map[string]int, error) {
	return nil, f.err
}

// TestAuditUpstreamErrorNotLeaked: an upstream failure keeps its 502 status,
// but the raw error text stays in the server log and never reaches the client.
func TestAuditUpstreamErrorNotLeaked(t *testing.T) {
	const marker = "SECRET-UPSTREAM-MARKER-9c1d https://internal.example/key=abc"
	var logBuf bytes.Buffer
	orig := reqLog
	reqLog = slog.New(slog.NewJSONHandler(&logBuf, nil))
	defer func() { reqLog = orig }()

	fail := fakeSource{name: "openfda_device_event", id: "report_number", err: errors.New(marker)}
	withSources(t, map[string]sources.Source{"openfda_device_event": failingCounter{fail}})

	// The API routes degrade gracefully on source errors, so the exit-1 branch
	// of routeHandler is driven with a command that fails hard (adverse).
	adverse := withLogging(routeHandler(apiRoute{
		params: []string{"device"},
		argv:   func(q url.Values) []string { return []string{"adverse", q.Get("device"), "--json"} },
	}))
	handlers := map[string]http.Handler{
		"/api/failure-modes?device=x": NewServeHandler(),
		"/api/adverse?device=x":       adverse,
	}
	for path, h := range handlers {
		logBuf.Reset()
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusBadGateway {
			t.Errorf("%s: status = %d, want 502; body=%s", path, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "SECRET-UPSTREAM-MARKER") {
			t.Errorf("%s: upstream error leaked to client: %s", path, rr.Body.String())
		}
		if !strings.Contains(logBuf.String(), "SECRET-UPSTREAM-MARKER") {
			t.Errorf("%s: upstream error not logged server-side: %s", path, logBuf.String())
		}
	}
}
