package cliutil

import (
	"context"
	"errors"
	"fmt"
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

// hostGuardKey is the fake key for the host-guard tests (DVC-SEC-01).
const hostGuardKey = "TESTKEY-123"

// seenRequest is one request as an upstream saw it: URL and headers.
type seenRequest struct {
	url     string
	headers string
}

// recordRequests starts an upstream that records every request (URL and all
// headers) and answers handle's response, or 200 JSON when handle is nil.
func recordRequests(t *testing.T, handle http.HandlerFunc) (*httptest.Server, func() []seenRequest) {
	t.Helper()
	var (
		mu  sync.Mutex
		got []seenRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, seenRequest{url: r.URL.String(), headers: fmt.Sprint(r.Header)})
		mu.Unlock()
		if handle != nil {
			handle(w, r)
			return
		}
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenRequest(nil), got...)
	}
}

// redirectingPair returns server A (the client's base URL host), which answers
// every request with a 302 to server B, a different httptest host, plus B's
// recorder.
func redirectingPair(t *testing.T) (a *httptest.Server, bGot func() []seenRequest) {
	t.Helper()
	b, bGot := recordRequests(t, nil)
	a, _ = recordRequests(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, b.URL+"/stolen?x=1", http.StatusFound)
	})
	return a, bGot
}

// TestOpenFDAKeySentToBaseHost: a request to the base URL's own host carries
// api_key=<key>.
func TestOpenFDAKeySentToBaseHost(t *testing.T) {
	withOpenFDAKey(t, hostGuardKey)
	srv, got := recordRequests(t, nil)
	if _, _, err := openFDATestClient(srv.URL).GetJSON(context.Background(), "/device/event.json", url.Values{"limit": {"1"}}); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	reqs := got()
	if len(reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(reqs))
	}
	u, err := url.Parse(reqs[0].url)
	if err != nil {
		t.Fatalf("bad URL on the wire: %v", err)
	}
	if u.Query().Get("api_key") != hostGuardKey {
		t.Errorf("base-host request carries no api_key=%s: %q", hostGuardKey, reqs[0].url)
	}
}

// TestOpenFDAKeyNotSentAfterCrossHostRedirect: A (the allowed host) redirects
// to B (another host). B must see no api_key and no key anywhere in its URL or
// headers. Each guard is covered on its own: "client" is the whole client, where
// CheckRedirect must refuse the hop so B is never called; "transport_only"
// follows the redirect with the default policy, so the transport guard alone
// must keep the key off B.
func TestOpenFDAKeyNotSentAfterCrossHostRedirect(t *testing.T) {
	noKey := func(t *testing.T, bGot func() []seenRequest) {
		t.Helper()
		for _, r := range bGot() {
			if strings.Contains(r.url, "api_key") || strings.Contains(r.url, hostGuardKey) {
				t.Errorf("host B received the key in its URL: %q", r.url)
			}
			if strings.Contains(r.headers, hostGuardKey) {
				t.Errorf("host B received the key in a header: %s", r.headers)
			}
		}
	}

	t.Run("client", func(t *testing.T) {
		withOpenFDAKey(t, hostGuardKey)
		a, bGot := redirectingPair(t)
		_, _, err := openFDATestClient(a.URL).GetJSON(context.Background(), "/device/event.json", url.Values{"limit": {"1"}})
		if err == nil {
			t.Error("want the cross-host redirect refused, got no error")
		}
		if n := len(bGot()); n != 0 {
			t.Errorf("host B was contacted %d time(s); the redirect must be refused", n)
		}
		noKey(t, bGot)
	})

	t.Run("transport_only", func(t *testing.T) {
		withOpenFDAKey(t, hostGuardKey)
		a, bGot := redirectingPair(t)
		// Same transport, default redirect policy: a redirect is followed.
		hc := &http.Client{Transport: openFDATestClient(a.URL).HTTP.Transport}
		resp, err := hc.Get(a.URL + "/device/event.json?limit=1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		resp.Body.Close()
		if len(bGot()) == 0 {
			t.Fatal("host B was never reached, so the transport guard is untested")
		}
		noKey(t, bGot)
	})
}

// TestOpenFDAFailedRequestErrorHasNoKey: a failing request's error text never
// carries the key, whether the connection is refused or the cross-host
// redirect is refused.
func TestOpenFDAFailedRequestErrorHasNoKey(t *testing.T) {
	withOpenFDAKey(t, hostGuardKey)

	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	_, _, err := openFDATestClient(dead.URL).GetJSON(context.Background(), "/device/event.json", url.Values{"limit": {"1"}})
	if err == nil {
		t.Fatal("want a transport error from a closed server")
	}
	if strings.Contains(err.Error(), hostGuardKey) {
		t.Errorf("transport error carries the key: %v", err)
	}

	a, _ := redirectingPair(t)
	_, _, err = openFDATestClient(a.URL).GetJSON(context.Background(), "/device/event.json", url.Values{"limit": {"1"}})
	if err == nil {
		t.Fatal("want an error from the refused cross-host redirect")
	}
	if strings.Contains(err.Error(), hostGuardKey) {
		t.Errorf("redirect error carries the key: %v", err)
	}
}
