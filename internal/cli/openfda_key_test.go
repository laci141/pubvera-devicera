package cli

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
)

// keyTestKey is a fake openFDA key; no real key ever appears in a test.
const keyTestKey = "TESTKEY123"

// upstreamStub stands in for every upstream host by replacing
// http.DefaultTransport, which the openFDA, PubMed and ClinicalTrials.gov
// clients all end at. It records each request exactly as it goes on the wire.
type upstreamStub struct {
	limited bool // openFDA answers 429, and device/udi.json fails at transport level
	mu      sync.Mutex
	reqs    []*url.URL
}

func (s *upstreamStub) RoundTrip(r *http.Request) (*http.Response, error) {
	u := *r.URL
	s.mu.Lock()
	s.reqs = append(s.reqs, &u)
	s.mu.Unlock()

	status, body := http.StatusOK, `{}`
	header := http.Header{"Content-Type": {"application/json"}}
	switch r.URL.Host {
	case "api.fda.gov":
		if s.limited {
			if r.URL.Path == "/device/udi.json" {
				return nil, errors.New("stub: connection reset by peer")
			}
			status, body = http.StatusTooManyRequests, `{"error":{"code":"OVER_RATE_LIMIT","message":"You have exceeded your rate limit."}}`
			header.Set("Retry-After", "0")
		} else {
			body = `{"meta":{"last_updated":"2026-09-22","results":{"total":5}},"results":[{"term":"Death","count":3},{"term":"Injury","count":2}]}`
		}
	case "eutils.ncbi.nlm.nih.gov":
		body = `{"esearchresult":{"count":"3","idlist":[]}}`
	case "clinicaltrials.gov":
		body = `{"totalCount":3,"studies":[]}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func (s *upstreamStub) requests() []*url.URL {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*url.URL(nil), s.reqs...)
}

// keyedServe runs the page's five calls plus /api/search for device through the
// real handler and the real sources, with the key set and every upstream
// stubbed. It returns the response bodies, the log output and the requests.
func keyedServe(t *testing.T, device string, limited bool) ([]string, string, []*url.URL) {
	t.Helper()
	restore := cliutil.SetOpenFDAKey(keyTestKey)
	t.Cleanup(restore)

	stub := &upstreamStub{limited: limited}
	old := http.DefaultTransport
	http.DefaultTransport = stub
	t.Cleanup(func() { http.DefaultTransport = old })

	var logBuf bytes.Buffer
	origLog := reqLog
	reqLog = slog.New(slog.NewJSONHandler(&logBuf, nil))
	t.Cleanup(func() { reqLog = origLog })

	// The real last-updated fetch, against an empty cache, so it goes out too.
	swapLUCache(t, fetchOpenFDALastUpdated)
	sharedSynth.mu.Lock()
	delete(sharedSynth.entries, device)
	sharedSynth.mu.Unlock()

	h := NewServeHandler()
	paths := []string{"/api/dossier", "/api/signals", "/api/trend", "/api/failure-modes", "/api/devices", "/api/search"}
	bodies := make([]string, len(paths))
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p+"?device="+url.QueryEscape(device), nil))
			bodies[i] = p + " " + rec.Body.String()
		}()
	}
	wg.Wait()
	return bodies, logBuf.String(), stub.requests()
}

// TestOpenFDAKeyOnEveryOpenFDARequestOnly: with OPENFDA_API_KEY set, every
// request to api.fda.gov — all three endpoints and the last-updated probe —
// carries api_key=<key>; PubMed and ClinicalTrials.gov requests never do.
func TestOpenFDAKeyOnEveryOpenFDARequestOnly(t *testing.T) {
	bodies, logs, reqs := keyedServe(t, "keytest pacemaker", false)

	seenPath := map[string]bool{}
	lastUpdatedProbe := false
	other := map[string]int{}
	for _, u := range reqs {
		q := u.Query()
		if u.Host == "api.fda.gov" {
			seenPath[u.Path] = true
			if q.Get("api_key") != keyTestKey {
				t.Errorf("openFDA request without the key: %s", u)
			}
			if u.Path == "/device/event.json" && q.Get("search") == "" && q.Get("count") == "" {
				lastUpdatedProbe = true
			}
			continue
		}
		other[u.Host]++
		if _, ok := q["api_key"]; ok || strings.Contains(u.String(), keyTestKey) {
			t.Errorf("the openFDA key went to %s: %s", u.Host, u)
		}
	}
	for _, p := range []string{"/device/event.json", "/device/enforcement.json", "/device/udi.json"} {
		if !seenPath[p] {
			t.Errorf("no openFDA request to %s was made", p)
		}
	}
	if !lastUpdatedProbe {
		t.Error("the last-updated probe never reached api.fda.gov")
	}
	for _, host := range []string{"eutils.ncbi.nlm.nih.gov", "clinicaltrials.gov"} {
		if other[host] == 0 {
			t.Errorf("no request to %s was made, so the no-key check proves nothing", host)
		}
	}
	assertNoKey(t, bodies, logs)
}

// TestOpenFDAKeyNeverInResponsesOrLogs: openFDA answers 429 and device/udi.json
// fails at transport level. The error text reaches both the browser (devices,
// trend, dossier notes) and the log today; the key must reach neither.
func TestOpenFDAKeyNeverInResponsesOrLogs(t *testing.T) {
	bodies, logs, reqs := keyedServe(t, "keytest limited", true)

	sent := 0
	for _, u := range reqs {
		if u.Host == "api.fda.gov" && u.Query().Get("api_key") == keyTestKey {
			sent++
		}
	}
	if sent == 0 {
		t.Fatal("no keyed openFDA request was made, so the leak check proves nothing")
	}
	// The errors under test must actually surface, or a clean output is vacuous.
	all := strings.Join(bodies, "\n")
	if !strings.Contains(all, "http 429 for https://api.fda.gov") {
		t.Errorf("the 429 error text never reached a response:\n%s", all)
	}
	if !strings.Contains(all, "stub: connection reset by peer") {
		t.Errorf("the transport error never reached a response:\n%s", all)
	}
	if !strings.Contains(logs, "http 429 for https://api.fda.gov") {
		t.Errorf("the 429 error text never reached the log:\n%s", logs)
	}
	assertNoKey(t, bodies, logs)
}

func assertNoKey(t *testing.T, bodies []string, logs string) {
	t.Helper()
	for _, b := range bodies {
		if strings.Contains(b, keyTestKey) {
			t.Errorf("response carries the key: %s", b)
		}
	}
	if strings.Contains(logs, keyTestKey) {
		t.Errorf("log carries the key:\n%s", logs)
	}
}
