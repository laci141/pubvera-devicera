package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/sources"
)

// eventRequests returns the api.fda.gov /device/event.json requests the stub saw.
func eventRequests(s *limitStub) []*url.URL {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*url.URL
	for _, u := range s.reqs {
		if u.Host == "api.fda.gov" && u.Path == "/device/event.json" {
			out = append(out, u)
		}
	}
	return out
}

// assertOneCountRequest: the trend endpoint asked openFDA exactly once, for the
// daily counts, with no date range in the search.
func assertOneCountRequest(t *testing.T, name string, s *limitStub) {
	t.Helper()
	reqs := eventRequests(s)
	if len(reqs) != 1 {
		t.Errorf("%s: want exactly 1 upstream request, got %d", name, len(reqs))
		return
	}
	q := reqs[0].Query()
	if q.Get("count") != "date_received" {
		t.Errorf("%s: the request must be count=date_received, got %s", name, reqs[0])
	}
	if strings.Contains(q.Get("search"), "date_received:[") {
		t.Errorf("%s: the search must carry no date range: %q", name, q.Get("search"))
	}
}

// T7: a 429 on the one count call. One upstream call, rate_limited:true, a fixed
// phrase in the body, the raw error only in the log.
func TestTrendCountRateLimited(t *testing.T) {
	res := serveLimited(t, "/api/trend", "trendcount ratelimited", http.StatusTooManyRequests, failAll)
	if res.status != http.StatusOK {
		t.Fatalf("status=%d want 200 (partial answer): %s", res.status, res.body)
	}
	assertOneCountRequest(t, "429", res.stub)
	assertNoUpstreamURL(t, "trend count 429", res.body)
	if res.json["rate_limited"] != true {
		t.Errorf("rate_limited:true missing: %s", res.body)
	}
	partial := strList(res.json["partial"])
	if len(partial) == 0 {
		t.Fatalf("a 429 must be reported in partial: %s", res.body)
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

// T8: a device with no events (openFDA 404) is ten zero rows, not a failure.
func TestTrendCountZeroEventDevice(t *testing.T) {
	res := serveLimited(t, "/api/trend", "trendcount zero events", http.StatusNotFound, failAll)
	if res.status != http.StatusOK {
		t.Fatalf("status=%d want 200: %s", res.status, res.body)
	}
	assertOneCountRequest(t, "404", res.stub)
	if _, ok := res.json["partial"]; ok {
		t.Errorf("a zero-event device must not carry partial: %s", res.body)
	}
	if _, ok := res.json["rate_limited"]; ok {
		t.Errorf("a zero-event device must not carry rate_limited: %s", res.body)
	}
	recs, _ := res.json["records"].([]any)
	if len(recs) != 10 {
		t.Fatalf("want 10 rows, got %d: %s", len(recs), res.body)
	}
	for _, r := range recs {
		if c := r.(map[string]any)["count"]; c != float64(0) {
			t.Errorf("want count 0, got %v in %v", c, r)
		}
	}
}

// T9: a non-rate-limit failure of the count (503) is ONE partial entry for all
// years, with no rate_limited. The real client retries a 503 twice (3 calls).
func TestTrendCountUnavailableOnePartialEntry(t *testing.T) {
	res := serveLimited(t, "/api/trend", "trendcount unavailable", http.StatusServiceUnavailable, failAll)
	if res.status != http.StatusOK {
		t.Fatalf("status=%d want 200 (partial answer): %s", res.status, res.body)
	}
	assertNoUpstreamURL(t, "trend count 503", res.body)
	if res.json["rate_limited"] == true {
		t.Errorf("a 503 must not set rate_limited: %s", res.body)
	}
	partial := strList(res.json["partial"])
	if len(partial) != 1 || partial[0] != "all years unavailable: upstream unavailable" {
		t.Errorf("partial=%q want [\"all years unavailable: upstream unavailable\"]", partial)
	}
	if recs, _ := res.json["records"].([]any); len(recs) != 0 {
		t.Errorf("no rows expected when the count failed: %s", res.body)
	}
	if got := len(eventRequests(res.stub)); got != 3 {
		t.Errorf("503 retries unchanged: want 3 attempts of the one count call, got %d", got)
	}
	for _, u := range eventRequests(res.stub) {
		if u.Query().Get("count") != "date_received" {
			t.Errorf("the request must be count=date_received, got %s", u)
			break
		}
	}
}

// fixtureEvent is an event source over a fixed set of daily buckets. Fetch
// answers the per-year date-range total exactly as openFDA would (inclusive
// range over YYYYMMDD); DailyCounts answers the one count call. Each is counted.
type fixtureEvent struct {
	buckets map[string]int
	fetches atomic.Int32
	counts  atomic.Int32
}

func (*fixtureEvent) Name() string                 { return "openfda_device_event" }
func (*fixtureEvent) IDField() string              { return "mdr_report_key" }
func (*fixtureEvent) Health(context.Context) error { return nil }
func (f *fixtureEvent) Fetch(_ context.Context, q sources.Query) ([]sources.RawRecord, sources.Page, error) {
	f.fetches.Add(1)
	total := 0
	for d, n := range f.buckets {
		if d >= q.DateFrom && d <= q.DateTo {
			total += n
		}
	}
	return nil, sources.Page{Total: total}, nil
}
func (f *fixtureEvent) DailyCounts(context.Context, string) (sources.DailyCounts, error) {
	f.counts.Add(1)
	return sources.NewDailyCounts(f.buckets), nil
}

// perYearOnly hides DailyCounts: the handler must take its per-year loop.
type perYearOnly struct{ sources.Source }

// T2: the rows from one daily count equal the rows from the per-year loop, on a
// fixture with records on Jan 1, Dec 31 and mid-year of every year in the
// range, plus records just outside it.
func TestTrendFromDailyCountsEqualsPerYearRows(t *testing.T) {
	year := time.Now().Year()
	buckets := map[string]int{
		fmt.Sprintf("%d1231", year-10): 1000, // the day before the first year
		fmt.Sprintf("%d0101", year+1):  2000, // the day after the last year
	}
	want := make([]any, 0, 10)
	for y := year - 9; y <= year; y++ {
		jan1, mid, dec31 := (y%7)+1, 10+(y%5), 100+(y%11)
		buckets[fmt.Sprintf("%d0101", y)] = jan1
		buckets[fmt.Sprintf("%d0615", y)] = mid
		buckets[fmt.Sprintf("%d1231", y)] = dec31
		want = append(want, map[string]any{"year": float64(y), "count": float64(jan1 + mid + dec31)})
	}

	counted := &fixtureEvent{buckets: buckets}
	withSources(t, map[string]sources.Source{"openfda_device_event": counted})
	viaCount := serveLimited(t, "/api/trend", "trend equality", http.StatusOK, func(*http.Request) bool { return false })

	loop := &fixtureEvent{buckets: buckets}
	withSources(t, map[string]sources.Source{"openfda_device_event": perYearOnly{loop}})
	viaLoop := serveLimited(t, "/api/trend", "trend equality", http.StatusOK, func(*http.Request) bool { return false })

	if counted.counts.Load() != 1 || counted.fetches.Load() != 0 {
		t.Errorf("count path: DailyCounts calls=%d Fetch calls=%d, want 1 and 0", counted.counts.Load(), counted.fetches.Load())
	}
	if loop.fetches.Load() != 10 {
		t.Errorf("fallback path: Fetch calls=%d, want 10", loop.fetches.Load())
	}
	if !reflect.DeepEqual(viaCount.json["records"], viaLoop.json["records"]) {
		t.Errorf("rows differ: count=%v loop=%v", viaCount.json["records"], viaLoop.json["records"])
	}
	if !reflect.DeepEqual(viaCount.json["records"], any(want)) {
		t.Errorf("rows are not the expected per-year sums: got %v want %v", viaCount.json["records"], want)
	}
	for _, k := range []string{"count", "note", "disclaimer"} {
		if !reflect.DeepEqual(viaCount.json[k], viaLoop.json[k]) {
			t.Errorf("%s differs: %v vs %v", k, viaCount.json[k], viaLoop.json[k])
		}
	}
	for _, k := range []string{"partial", "rate_limited"} {
		if _, ok := viaCount.json[k]; ok {
			t.Errorf("a clean answer must not carry %s: %s", k, viaCount.body)
		}
	}
}
