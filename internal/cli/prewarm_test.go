package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
	"github.com/laci141/medical-device-intelligence/internal/intelligence"
)

// prewarmStub answers every upstream locally and counts the two device-independent
// baseline requests (no search term). While gate is non-nil they wait for it.
type prewarmStub struct {
	mu   sync.Mutex
	n    int
	gate chan struct{}
}

func (s *prewarmStub) RoundTrip(r *http.Request) (*http.Response, error) {
	q := r.URL.Query()
	body := `{"meta":{"results":{"total":0}},"results":[]}`
	if r.URL.Host == "api.fda.gov" && q.Get("search") == "" {
		switch q.Get("count") {
		case "event_type.exact":
			s.mu.Lock()
			s.n++
			s.mu.Unlock()
			if s.gate != nil {
				select {
				case <-s.gate:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			body = `{"results":[{"term":"Malfunction","count":9000}]}`
		case "device.generic_name.exact":
			s.mu.Lock()
			s.n++
			s.mu.Unlock()
			if s.gate != nil {
				select {
				case <-s.gate:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			body = `{"results":[{"term":"a","count":10},{"term":"b","count":20}]}`
		}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func (s *prewarmStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func installPrewarmStub(t *testing.T, s *prewarmStub) {
	t.Helper()
	restoreKey := cliutil.SetOpenFDAKey("")
	t.Cleanup(restoreKey)
	old := http.DefaultTransport
	http.DefaultTransport = s
	t.Cleanup(func() { http.DefaultTransport = old })
	intelligence.ResetBaselinesForTest()
	t.Cleanup(intelligence.ResetBaselinesForTest)
}

// syncBuf is a bytes.Buffer safe for the prewarm goroutine to write while the
// test reads.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func setPrewarm(t *testing.T, enabled bool, fn func(context.Context) error) {
	t.Helper()
	oldEnabled, oldFn := prewarmEnabled, prewarmBaselines
	prewarmEnabled = enabled
	if fn != nil {
		prewarmBaselines = fn
	}
	t.Cleanup(func() { prewarmEnabled, prewarmBaselines = oldEnabled, oldFn })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// G7a: startup prewarm performs exactly 2 baseline requests, in the background:
// startBaselinePrewarm returns and the first response is served while both
// requests are still held open.
func TestG7PrewarmTwoRequestsInBackgroundNoDelay(t *testing.T) {
	stub := &prewarmStub{gate: make(chan struct{})}
	installPrewarmStub(t, stub)
	setPrewarm(t, true, intelligence.PrewarmBaselines)

	var stderr syncBuf
	start := time.Now()
	returned := make(chan struct{})
	go func() {
		startBaselinePrewarm(context.Background(), &stderr)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(500 * time.Millisecond):
		close(stub.gate) // free the blocked prewarm so the test can end
		t.Fatal("startBaselinePrewarm did not return while the baselines were held open: prewarm blocks startup")
	}
	rec := httptest.NewRecorder()
	NewServeHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("first response status %d", rec.Code)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("start + first response took %v while the baselines were still held open; prewarm must not delay it", elapsed)
	}
	waitFor(t, "both baseline requests", func() bool { return stub.count() == 2 })
	close(stub.gate)
	time.Sleep(150 * time.Millisecond)
	if n := stub.count(); n != 2 {
		t.Errorf("baseline requests=%d, want exactly 2", n)
	}
	if stderr.String() != "" {
		t.Errorf("unexpected log output: %q", stderr.String())
	}
}

// G7b: a failing prewarm logs one line without a URL and the server still serves.
func TestG7FailingPrewarmLogsAndServerServes(t *testing.T) {
	installPrewarmStub(t, &prewarmStub{})
	done := make(chan struct{})
	setPrewarm(t, true, func(context.Context) error {
		defer close(done)
		return errors.New(`Get "https://api.fda.gov/device/event.json?count=x": boom`)
	})
	var stderr syncBuf
	startBaselinePrewarm(context.Background(), &stderr)
	waitFor(t, "the prewarm call", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	waitFor(t, "the log line", func() bool { return strings.Contains(stderr.String(), "baseline prewarm failed") })
	if s := stderr.String(); strings.Contains(s, "://") || strings.Contains(s, "api.fda.gov") || strings.Count(s, "\n") != 1 {
		t.Errorf("log must be one line without a URL: %q", s)
	}
	rec := httptest.NewRecorder()
	NewServeHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("server must still serve, got %d", rec.Code)
	}
}

// G7c: with prewarm off (the default for tests that do not ask) nothing runs.
func TestG7PrewarmOffMakesNoRequests(t *testing.T) {
	stub := &prewarmStub{}
	installPrewarmStub(t, stub)
	setPrewarm(t, false, nil)
	startBaselinePrewarm(context.Background(), io.Discard)
	time.Sleep(100 * time.Millisecond)
	if n := stub.count(); n != 0 {
		t.Errorf("baseline requests=%d with prewarm disabled, want 0", n)
	}
}
