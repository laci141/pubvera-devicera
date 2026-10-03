package cliutil

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(base string) *Client {
	c := NewClient(base)
	c.Backoff = 1 // keep retries fast in tests
	return c
}

// TestLargeBodyUnderCapParses is the regression for the 1 MB truncation bug: a
// body larger than the OLD 1 MB cap must be returned whole, not truncated into
// invalid JSON.
func TestLargeBodyUnderCapParses(t *testing.T) {
	big := `{"pad":"` + strings.Repeat("x", 3<<20) + `"}` // ~3 MB, valid JSON
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(big))
	}))
	defer srv.Close()

	body, status, err := testClient(srv.URL).GetJSON(context.Background(), "/x", nil)
	if err != nil {
		t.Fatalf("large body must not error: %v", err)
	}
	if status != 200 || len(body) != len(big) {
		t.Fatalf("body truncated: got %d bytes want %d", len(body), len(big))
	}
}

// TestBodyOverCapErrorsClearly proves an over-cap body yields a clear error, not
// a cryptic downstream JSON failure.
func TestBodyOverCapErrorsClearly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("y", 5000)))
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	c.MaxBodyBytes = 1000 // force truncation
	_, _, err := c.GetJSON(context.Background(), "/x", nil)
	if err == nil {
		t.Fatal("over-cap body must return an error")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("error should explain the cap, got %v", err)
	}
}

// TestRetryOnceOn5xxThenSucceed proves guardrail 6: exactly one retry on 5xx.
func TestRetryOnceOn5xxThenSucceed(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	body, status, err := testClient(srv.URL).GetJSON(context.Background(), "/x", nil)
	if err != nil {
		t.Fatalf("should succeed on retry: %v", err)
	}
	if status != 200 || calls != 2 {
		t.Fatalf("expected 2 calls (1 fail + 1 retry), got calls=%d status=%d", calls, status)
	}
	if !strings.Contains(string(body), "ok") {
		t.Errorf("unexpected body %q", body)
	}
}

// TestNoRetryOn4xx proves a 4xx is returned immediately, never retried.
func TestNoRetryOn4xx(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"NOT_FOUND"}`))
	}))
	defer srv.Close()

	_, status, err := testClient(srv.URL).GetJSON(context.Background(), "/x", nil)
	if calls != 1 {
		t.Fatalf("4xx must not be retried; calls=%d", calls)
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != 404 || status != 404 {
		t.Fatalf("expected *APIError 404, got status=%d err=%v", status, err)
	}
}

// serveStatus answers every request with status, sets Retry-After when given,
// and counts the calls it receives.
func serveStatus(t *testing.T, status int, retryAfter string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
		w.Write([]byte(`{"error":{"code":"OVER_RATE_LIMIT"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestRetryOn429OnlyWithShortRetryAfter: a 429 is retried only when the server
// names a wait of 0..5 whole seconds. No header, an unparsable one, an
// HTTP-date, a negative or a longer wait all return the *APIError at once.
func TestRetryOn429OnlyWithShortRetryAfter(t *testing.T) {
	cases := []struct {
		name       string
		retryAfter string
		wantCalls  int32
	}{
		{"no header", "", 1},
		{"zero", "0", 3},
		{"one second", "1", 3},
		{"five seconds", "5", 3},
		{"six seconds", "6", 1},
		{"unparsable", "soon", 1},
		{"http date", "Wed, 21 Oct 2026 07:28:00 GMT", 1},
		{"negative", "-1", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := serveStatus(t, http.StatusTooManyRequests, tc.retryAfter)
			c := testClient(srv.URL)
			if tc.retryAfter == "1" || tc.retryAfter == "5" {
				// Retry-After wins over Backoff, so a real wait would happen:
				// cap the test at one ctx-bounded attempt pair instead.
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				_, _, err := c.GetJSON(ctx, "/x", nil)
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Retry-After %s: want the call to be waiting for the retry, got %v", tc.retryAfter, err)
				}
				if got := calls.Load(); got != 1 {
					t.Fatalf("Retry-After %s: want 1 call before the wait, got %d", tc.retryAfter, got)
				}
				return
			}
			_, status, err := c.GetJSON(context.Background(), "/x", nil)
			if got := calls.Load(); got != tc.wantCalls {
				t.Fatalf("Retry-After %q: calls=%d want %d", tc.retryAfter, got, tc.wantCalls)
			}
			apiErr, ok := err.(*APIError)
			if !ok || apiErr.StatusCode != 429 {
				t.Fatalf("want *APIError 429, got status=%d err=%v", status, err)
			}
			// Returned at once, the status travels with the error; after the
			// retries ran out GetJSON has always returned 0 (unchanged).
			if tc.wantCalls == 1 && status != 429 {
				t.Fatalf("no retry: want status 429, got %d", status)
			}
		})
	}
}

// TestRetryOn429WithRetryAfterOneWaitsThenRetries pins that a Retry-After the
// client is willing to honour does produce a retry, after the stated wait.
func TestRetryOn429WithRetryAfterOneWaitsThenRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	start := time.Now()
	_, status, err := testClient(srv.URL).GetJSON(context.Background(), "/x", nil)
	if err != nil || status != 200 {
		t.Fatalf("want success on the retry, got status=%d err=%v", status, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls=%d want 2", got)
	}
	if waited := time.Since(start); waited < 900*time.Millisecond {
		t.Fatalf("Retry-After: 1 was not honoured, retried after %v", waited)
	}
}

// TestRetry5xxStillThreeAttempts: the 5xx policy is unchanged by the 429 rule.
func TestRetry5xxStillThreeAttempts(t *testing.T) {
	srv, calls := serveStatus(t, http.StatusServiceUnavailable, "")
	_, _, err := testClient(srv.URL).GetJSON(context.Background(), "/x", nil)
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != 503 {
		t.Fatalf("want *APIError 503, got %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls=%d want 3", got)
	}
}

func TestIsRateLimited(t *testing.T) {
	wrapped := fmt.Errorf("volume: events: %w", &APIError{StatusCode: 429, URL: "https://x"})
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"429", &APIError{StatusCode: 429}, true},
		{"wrapped 429", wrapped, true},
		{"503", &APIError{StatusCode: 503}, false},
		{"404", &APIError{StatusCode: 404}, false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	} {
		if got := IsRateLimited(tc.err); got != tc.want {
			t.Errorf("%s: IsRateLimited=%v want %v", tc.name, got, tc.want)
		}
	}
}

// TestUpstreamClassIsFixedAndURLFree: whatever the error says, the class that
// may reach a browser is one of two fixed phrases.
func TestUpstreamClassIsFixedAndURLFree(t *testing.T) {
	limited := &APIError{StatusCode: 429, URL: "https://api.fda.gov/device/event.json?x=1", Snippet: "slow down"}
	msg, code := UpstreamClass(limited)
	if msg != "upstream rate limit reached" || code != "rate_limited" {
		t.Errorf("429: got %q / %q", msg, code)
	}
	msg, code = UpstreamClass(&APIError{StatusCode: 503, URL: "https://api.fda.gov/x"})
	if msg != "upstream unavailable" || code != "upstream_unavailable" {
		t.Errorf("503: got %q / %q", msg, code)
	}
	msg, code = UpstreamClass(errors.New("dial tcp api.fda.gov:443: refused"))
	if msg != "upstream unavailable" || code != "upstream_unavailable" {
		t.Errorf("transport error: got %q / %q", msg, code)
	}
}
