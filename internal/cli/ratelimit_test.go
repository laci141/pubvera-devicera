package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
	"github.com/laci141/medical-device-intelligence/internal/intelligence"
	"github.com/laci141/medical-device-intelligence/internal/sources"
)

// limitStub stands in for every upstream host by replacing
// http.DefaultTransport. api.fda.gov requests for which fail returns true are
// answered with status (and NO Retry-After header, like the openFDA 429 seen in
// production); everything else answers 200 with an empty result.
type limitStub struct {
	fail   func(*http.Request) bool
	status int
	// udiTotal, when set, is the meta.results.total every successful UDI answer
	// reports, so the category leg asks for more pages.
	udiTotal int
	mu       sync.Mutex
	reqs     []*url.URL
}

func (s *limitStub) RoundTrip(r *http.Request) (*http.Response, error) {
	u := *r.URL
	s.mu.Lock()
	s.reqs = append(s.reqs, &u)
	s.mu.Unlock()

	status, body := http.StatusOK, `{"meta":{"results":{"total":0}},"results":[]}`
	switch r.URL.Host {
	case "api.fda.gov":
		if s.fail(r) {
			status, body = s.status, `{"error":{"code":"OVER_RATE_LIMIT","message":"You have exceeded your rate limit."}}`
		} else if s.udiTotal > 0 && r.URL.Path == "/device/udi.json" {
			body = fmt.Sprintf(`{"meta":{"results":{"total":%d}},"results":[]}`, s.udiTotal)
		}
	case "eutils.ncbi.nlm.nih.gov":
		body = `{"esearchresult":{"count":"3","idlist":[]}}`
	case "clinicaltrials.gov":
		body = `{"totalCount":3,"studies":[]}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func (s *limitStub) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, u := range s.reqs {
		if u.Host == "api.fda.gov" && u.Path == path {
			n++
		}
	}
	return n
}

func failAll(*http.Request) bool { return true }

// failLeg fails the UDI requests of one devices leg: the brand-name leg asks for
// limit=100, the product-category leg for limit=1000.
func failLeg(limit string) func(*http.Request) bool {
	return func(r *http.Request) bool {
		return r.URL.Path == "/device/udi.json" && r.URL.Query().Get("limit") == limit
	}
}

// failLaterPages fails every category request after the first page (skip > 0).
func failLaterPages(r *http.Request) bool {
	return r.URL.Path == "/device/udi.json" && r.URL.Query().Get("skip") != ""
}

type limitedResult struct {
	status int
	body   string
	json   map[string]any
	logs   string
	stub   *limitStub
}

// serveLimited runs one GET through the real handler, real sources and real
// HTTP clients, with every upstream stubbed. The openFDA key is cleared and the
// freshness probe stubbed so the request count is exactly the handler's own.
func serveLimited(t *testing.T, path, device string, status int, fail func(*http.Request) bool) limitedResult {
	t.Helper()
	return serveLimitedUDI(t, path, device, status, 0, fail)
}

func serveLimitedUDI(t *testing.T, path, device string, status, udiTotal int, fail func(*http.Request) bool) limitedResult {
	t.Helper()
	restoreKey := cliutil.SetOpenFDAKey("")
	t.Cleanup(restoreKey)

	stub := &limitStub{fail: fail, status: status, udiTotal: udiTotal}
	old := http.DefaultTransport
	http.DefaultTransport = stub
	t.Cleanup(func() { http.DefaultTransport = old })

	var logBuf bytes.Buffer
	origLog := reqLog
	reqLog = slog.New(slog.NewJSONHandler(&logBuf, nil))
	t.Cleanup(func() { reqLog = origLog })

	swapLUCache(t, func(context.Context) string { return "2026-09-22" })
	sharedSynth.mu.Lock()
	delete(sharedSynth.entries, device)
	sharedSynth.mu.Unlock()

	rec := httptest.NewRecorder()
	NewServeHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?device="+url.QueryEscape(device), nil))
	res := limitedResult{status: rec.Code, body: rec.Body.String(), stub: stub}
	if err := json.Unmarshal(rec.Body.Bytes(), &res.json); err != nil {
		t.Fatalf("%s: body is not JSON: %v\n%s", path, err, res.body)
	}
	res.logs = logBuf.String()
	return res
}

// assertNoUpstreamURL is the browser-side rule: no URL, no host, no status line.
func assertNoUpstreamURL(t *testing.T, name, body string) {
	t.Helper()
	for _, bad := range []string{"://", "api.fda.gov", "http 429 for", "http 503 for", "ncbi.nlm.nih.gov", "clinicaltrials.gov"} {
		if strings.Contains(body, bad) {
			t.Errorf("%s: response body contains %q:\n%s", name, bad, body)
		}
	}
}

func strList(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, str(e))
	}
	return out
}

// T2 + T3: /api/trend under a 429 on every year.
func TestRateLimitedTrendBody(t *testing.T) {
	res := serveLimited(t, "/api/trend", "ratelimit trend", http.StatusTooManyRequests, failAll)
	if res.status != http.StatusOK {
		t.Fatalf("status=%d want 200 (partial answer): %s", res.status, res.body)
	}
	assertNoUpstreamURL(t, "trend", res.body)
	if res.json["rate_limited"] != true {
		t.Errorf("trend must carry rate_limited:true: %s", res.body)
	}
	partial := strList(res.json["partial"])
	if len(partial) == 0 {
		t.Fatalf("trend must report what is missing in partial: %s", res.body)
	}
	for _, p := range partial {
		if !strings.HasSuffix(p, "upstream rate limit reached") {
			t.Errorf("partial entry is not the fixed class phrase: %q", p)
		}
	}
	if !strings.Contains(res.logs, "http 429 for https://api.fda.gov") {
		t.Errorf("the raw 429 text must go to the log; log was:\n%s", res.logs)
	}
}

// T3: after the first rate-limited year the trend loop stops.
func TestRateLimitedTrendStopsAfterFirstYear(t *testing.T) {
	res := serveLimited(t, "/api/trend", "ratelimit trend stop", http.StatusTooManyRequests, failAll)
	if got := res.stub.count("/device/event.json"); got != 1 {
		t.Errorf("trend made %d upstream calls under a 429, want exactly 1", got)
	}
	partial := strList(res.json["partial"])
	want := "remaining years skipped: upstream rate limit reached"
	if len(partial) == 0 || partial[len(partial)-1] != want {
		t.Errorf("partial must end with %q, got %q", want, partial)
	}
}

// fastBackoffEvent is a trend event source whose client retries with a 1 ms
// backoff. Fetch makes the one-record count call the trend handler makes per
// year and returns its error untouched.
type fastBackoffEvent struct{ client *cliutil.Client }

func (fastBackoffEvent) Name() string    { return "openfda_device_event" }
func (fastBackoffEvent) IDField() string { return "report_number" }
func (fastBackoffEvent) Health(context.Context) error {
	return nil
}
func (f fastBackoffEvent) Fetch(ctx context.Context, q sources.Query) ([]sources.RawRecord, sources.Page, error) {
	if _, _, err := f.client.GetJSON(ctx, "/device/event.json", url.Values{"limit": {"1"}}); err != nil {
		return nil, sources.Page{}, err
	}
	return nil, sources.Page{}, nil
}

// Other errors keep the per-year behaviour: a 503 on every year is NOT a rate
// limit, so the loop is not cut short and the body carries no rate_limited.
//
// The real event source's client keeps the production 300/600 ms backoff and has
// no seam reachable from this package, so the test stands in a source that
// calls the same /device/event.json through a client with Backoff = 1 ms.
//
// fastBackoffEvent has no DailyCounts, so this covers the per-year FALLBACK path
// of handleTrend. The count path's 503 answer (one "all years unavailable"
// entry) is TestTrendCountUnavailableOnePartialEntry.
func TestUnavailableTrendKeepsPerYearBehaviour(t *testing.T) {
	client := cliutil.NewOpenFDAClient("https://api.fda.gov")
	client.Backoff = time.Millisecond
	withSources(t, map[string]sources.Source{
		"openfda_device_event": fastBackoffEvent{client: client},
	})
	res := serveLimited(t, "/api/trend", "unavailable trend", http.StatusServiceUnavailable, failAll)
	assertNoUpstreamURL(t, "trend 503", res.body)
	if res.json["rate_limited"] == true {
		t.Errorf("a 503 must not set rate_limited: %s", res.body)
	}
	partial := strList(res.json["partial"])
	if len(partial) != 10 {
		t.Fatalf("503 on every year: want 10 partial entries, got %d: %q", len(partial), partial)
	}
	for _, p := range partial {
		if !strings.HasSuffix(p, "upstream unavailable") {
			t.Errorf("partial entry is not the fixed class phrase: %q", p)
		}
	}
	if got := res.stub.count("/device/event.json"); got != 30 {
		t.Errorf("503 retries unchanged: want 30 calls (10 years × 3), got %d", got)
	}
}

// T2: /api/failure-modes.
func TestRateLimitedFailureModesBody(t *testing.T) {
	res := serveLimited(t, "/api/failure-modes", "ratelimit modes", http.StatusTooManyRequests, failAll)
	if res.status != http.StatusBadGateway {
		t.Fatalf("status=%d want 502: %s", res.status, res.body)
	}
	assertNoUpstreamURL(t, "failure-modes", res.body)
	if res.json["error"] != "upstream data source failed" || res.json["code"] != "rate_limited" {
		t.Errorf("want error=upstream data source failed code=rate_limited: %s", res.body)
	}
	if !strings.Contains(res.logs, "http 429 for https://api.fda.gov") {
		t.Errorf("the raw error must stay in the log:\n%s", res.logs)
	}
}

func TestUnavailableFailureModesCode(t *testing.T) {
	res := serveLimited(t, "/api/failure-modes", "unavailable modes", http.StatusServiceUnavailable, failAll)
	if res.status != http.StatusBadGateway {
		t.Fatalf("status=%d want 502: %s", res.status, res.body)
	}
	assertNoUpstreamURL(t, "failure-modes 503", res.body)
	if res.json["error"] != "upstream data source failed" || res.json["code"] != "upstream_unavailable" {
		t.Errorf("want code=upstream_unavailable: %s", res.body)
	}
	if _, ok := res.json["rate_limited"]; ok {
		t.Errorf("a 503 must not carry rate_limited: %s", res.body)
	}
}

// T2: /api/devices, both legs failing.
func TestRateLimitedDevicesBothLegsBody(t *testing.T) {
	res := serveLimited(t, "/api/devices", "ratelimit devices", http.StatusTooManyRequests, failAll)
	if res.status != http.StatusBadGateway {
		t.Fatalf("status=%d want 502: %s", res.status, res.body)
	}
	assertNoUpstreamURL(t, "devices both legs", res.body)
	if res.json["error"] != "upstream data source failed" || res.json["code"] != "rate_limited" {
		t.Errorf("want error=upstream data source failed code=rate_limited: %s", res.body)
	}
	if !strings.Contains(res.logs, "http 429 for https://api.fda.gov") {
		t.Errorf("the raw error must stay in the log:\n%s", res.logs)
	}
}

// T2: /api/devices, one leg failing: a 200 that says what is missing.
func TestRateLimitedDevicesOneLegBody(t *testing.T) {
	for _, tc := range []struct {
		name, limit, want string
	}{
		{"brand leg", "100", "brand-name search unavailable: upstream rate limit reached"},
		{"category leg", "1000", "product-category search unavailable: upstream rate limit reached"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := serveLimited(t, "/api/devices", "ratelimit one leg", http.StatusTooManyRequests, failLeg(tc.limit))
			if res.status != http.StatusOK {
				t.Fatalf("status=%d want 200: %s", res.status, res.body)
			}
			assertNoUpstreamURL(t, "devices "+tc.name, res.body)
			if res.json["rate_limited"] != true {
				t.Errorf("must carry rate_limited:true: %s", res.body)
			}
			if partial := strList(res.json["partial"]); len(partial) != 1 || partial[0] != tc.want {
				t.Errorf("partial=%q want [%q]", partial, tc.want)
			}
			if !strings.Contains(res.logs, "http 429 for https://api.fda.gov") {
				t.Errorf("the raw error must stay in the log:\n%s", res.logs)
			}
		})
	}
}

// A later category page that hits the limit costs records, not the response:
// 200, marked rate_limited, no URL; the raw error stays in the log.
func TestRateLimitedDevicesLaterPageMarksResponse(t *testing.T) {
	res := serveLimitedUDI(t, "/api/devices", "ratelimit pages", http.StatusTooManyRequests, 2380, failLaterPages)
	if res.status != http.StatusOK {
		t.Fatalf("status=%d want 200: %s", res.status, res.body)
	}
	assertNoUpstreamURL(t, "devices later page", res.body)
	if res.json["rate_limited"] != true {
		t.Errorf("a rate-limited later page must set rate_limited:true: %s", res.body)
	}
	if !strings.Contains(res.logs, "http 429 for https://api.fda.gov") {
		t.Errorf("the raw error must stay in the log:\n%s", res.logs)
	}
}

// T2: /api/dossier and /api/signals when every probe is rate limited.
func TestRateLimitedDossierBody(t *testing.T) {
	res := serveLimited(t, "/api/dossier", "ratelimit dossier", http.StatusTooManyRequests, failAll)
	if res.status != http.StatusOK {
		t.Fatalf("status=%d want 200 (partial dossier): %s", res.status, res.body)
	}
	assertNoUpstreamURL(t, "dossier", res.body)
	if res.json["rate_limited"] != true {
		t.Errorf("dossier must carry rate_limited:true: %s", res.body)
	}
	notes := strList(res.json["notes"])
	if len(notes) == 0 {
		t.Fatalf("dossier must list the failed probes in notes: %s", res.body)
	}
	for _, n := range notes {
		if !strings.HasSuffix(n, "unavailable: upstream rate limit reached") &&
			!strings.HasSuffix(n, "unavailable: upstream unavailable") {
			t.Errorf("note is not a fixed class phrase: %q", n)
		}
	}

	// The same cached run feeds /api/signals: no URL there either.
	rec := httptest.NewRecorder()
	NewServeHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/signals?device="+url.QueryEscape("ratelimit dossier"), nil))
	assertNoUpstreamURL(t, "signals", rec.Body.String())
}

// A clean dossier carries no rate_limited field at all.
func TestCleanDossierHasNoRateLimitedField(t *testing.T) {
	b, err := json.Marshal(&intelligence.IntelligenceDossier{Device: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "rate_limited") || strings.Contains(string(b), "LogNotes") {
		t.Errorf("clean dossier JSON must not mention rate_limited or LogNotes: %s", b)
	}
}

// T5: a rate-limited synthesis is reused for 60 s; a clean one for 5 minutes.
func TestSynthCacheTTLByOutcome(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	old := synthNow
	synthNow = func() time.Time { return now }
	t.Cleanup(func() { synthNow = old })

	for _, tc := range []struct {
		name        string
		rateLimited bool
		// elapsed time → whether the next Do must run the suite again.
		steps []struct {
			after   time.Duration
			wantRun bool
		}
	}{
		{"rate limited", true, []struct {
			after   time.Duration
			wantRun bool
		}{
			{59 * time.Second, false},
			{2 * time.Second, true}, // 61 s since the first run
		}},
		{"clean", false, []struct {
			after   time.Duration
			wantRun bool
		}{
			{61 * time.Second, false}, // 61 s: past the rate-limited TTL, inside the normal one
			{3 * time.Minute, false},  // 4 min 1 s
			{2 * time.Minute, true},   // 6 min 1 s: past the normal TTL
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &synthGroup{entries: make(map[string]*synthEntry)}
			runs := 0
			run := func(context.Context, string) (*intelligence.IntelligenceDossier, error) {
				runs++
				return &intelligence.IntelligenceDossier{Device: "x", RateLimited: tc.rateLimited}, nil
			}
			now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
			if _, err := g.Do(context.Background(), "x", run); err != nil {
				t.Fatal(err)
			}
			for i, st := range tc.steps {
				now = now.Add(st.after)
				before := runs
				if _, err := g.Do(context.Background(), "x", run); err != nil {
					t.Fatal(err)
				}
				if ran := runs > before; ran != st.wantRun {
					t.Fatalf("step %d (+%v, total %v): suite ran=%v, want %v",
						i, st.after, now.Sub(time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)), ran, st.wantRun)
				}
			}
		})
	}
}

// An error result is still never cached (unchanged).
func TestSynthCacheErrorNotCached(t *testing.T) {
	g := &synthGroup{entries: make(map[string]*synthEntry)}
	runs := 0
	run := func(context.Context, string) (*intelligence.IntelligenceDossier, error) {
		runs++
		return nil, io.ErrUnexpectedEOF
	}
	for range 2 {
		if _, err := g.Do(context.Background(), "x", run); err == nil {
			t.Fatal("want the run error")
		}
	}
	if runs != 2 {
		t.Errorf("a failed run must not be cached: runs=%d want 2", runs)
	}
}
