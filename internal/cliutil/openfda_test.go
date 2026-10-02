package cliutil

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// testKey is a fake openFDA key; no real key ever appears in a test.
const testKey = "TESTKEY123"

// withOpenFDAKey sets the process key for one test and restores it after.
func withOpenFDAKey(t *testing.T, key string) {
	t.Helper()
	restore := SetOpenFDAKey(key)
	t.Cleanup(restore)
}

// recordQueries starts an upstream that records every raw query and answers
// status with a small JSON body.
func recordQueries(t *testing.T, status int) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu  sync.Mutex
		got []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.RawQuery)
		mu.Unlock()
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "0")
		}
		w.WriteHeader(status)
		w.Write([]byte(`{"error":{"code":"OVER_RATE_LIMIT"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func openFDATestClient(base string) *Client {
	c := NewOpenFDAClient(base)
	c.Backoff = 1 // keep retries fast in tests
	return c
}

// TestOpenFDAClientSendsKeyOnEveryRequest: with a key set, every request —
// with params and without — carries api_key=<key>, and the original params
// arrive unchanged.
func TestOpenFDAClientSendsKeyOnEveryRequest(t *testing.T) {
	withOpenFDAKey(t, testKey)
	srv, got := recordQueries(t, http.StatusOK)
	c := openFDATestClient(srv.URL)

	params := url.Values{}
	params.Set("search", `device.generic_name:"pace maker" AND date_received:[20200101 TO 20201231]`)
	params.Set("limit", "1")
	if _, _, err := c.GetJSON(context.Background(), "/device/event.json", params); err != nil {
		t.Fatalf("with params: %v", err)
	}
	if _, _, err := c.GetJSON(context.Background(), "/device/event.json", nil); err != nil {
		t.Fatalf("without params: %v", err)
	}

	queries := got()
	if len(queries) != 2 {
		t.Fatalf("want 2 requests, got %d", len(queries))
	}
	for i, raw := range queries {
		q, err := url.ParseQuery(raw)
		if err != nil {
			t.Fatalf("request %d: bad query %q: %v", i, raw, err)
		}
		if q.Get("api_key") != testKey {
			t.Errorf("request %d carries no api_key=%s: %q", i, testKey, raw)
		}
	}
	first, _ := url.ParseQuery(queries[0])
	if first.Get("search") != params.Get("search") || first.Get("limit") != "1" {
		t.Errorf("original params changed on the wire: %q", queries[0])
	}
}

// TestOpenFDAClientWithoutKeySendsNoParam: no key means exactly today's
// keyless request — no api_key param at all.
func TestOpenFDAClientWithoutKeySendsNoParam(t *testing.T) {
	withOpenFDAKey(t, "")
	srv, got := recordQueries(t, http.StatusOK)
	c := openFDATestClient(srv.URL)

	params := url.Values{}
	params.Set("limit", "1")
	if _, _, err := c.GetJSON(context.Background(), "/device/udi.json", params); err != nil {
		t.Fatalf("with params: %v", err)
	}
	if _, _, err := c.GetJSON(context.Background(), "/device/udi.json", nil); err != nil {
		t.Fatalf("without params: %v", err)
	}
	queries := got()
	if len(queries) != 2 {
		t.Fatalf("want 2 requests, got %d", len(queries))
	}
	if queries[0] != "limit=1" || queries[1] != "" {
		t.Errorf("keyless queries changed: %q", queries)
	}
	for i, raw := range queries {
		if strings.Contains(raw, "api_key") {
			t.Errorf("request %d carries api_key without a key set: %q", i, raw)
		}
	}
}

// TestOpenFDAKeyNeverInErrorText: neither an upstream error (APIError) nor a
// transport error (connection refused) carries the key in its text.
func TestOpenFDAKeyNeverInErrorText(t *testing.T) {
	withOpenFDAKey(t, testKey)

	srv, got := recordQueries(t, http.StatusTooManyRequests)
	_, _, err := openFDATestClient(srv.URL).GetJSON(context.Background(), "/device/event.json", url.Values{"limit": {"1"}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want *APIError 429, got %v", err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(apiErr.URL, testKey) {
		t.Errorf("APIError carries the key: %v", err)
	}
	// The key did go out: the error is clean because the URL never had it.
	for _, raw := range got() {
		if !strings.Contains(raw, "api_key="+testKey) {
			t.Fatalf("upstream request without the key: %q", raw)
		}
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close() // connection refused from here on
	_, _, err = openFDATestClient(dead.URL).GetJSON(context.Background(), "/device/event.json", url.Values{"limit": {"1"}})
	if err == nil {
		t.Fatal("want a transport error from a closed server")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("transport error carries the key: %v", err)
	}
}

// TestAPIErrorRedactsAPIKey is the backstop: an APIError whose URL did carry
// api_key prints [REDACTED] in its place.
func TestAPIErrorRedactsAPIKey(t *testing.T) {
	e := &APIError{
		StatusCode: http.StatusTooManyRequests,
		URL:        "https://api.fda.gov/device/event.json?limit=1&api_key=" + testKey + "&search=x",
		Snippet:    `{"error":{"code":"OVER_RATE_LIMIT"}}`,
	}
	msg := e.Error()
	if strings.Contains(msg, testKey) {
		t.Fatalf("APIError text carries the key: %s", msg)
	}
	if !strings.Contains(msg, "api_key=[REDACTED]&search=x") {
		t.Errorf("want api_key=[REDACTED] with the rest of the URL kept, got %s", msg)
	}
}
