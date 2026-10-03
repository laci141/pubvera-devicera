package intelligence

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
	"github.com/laci141/medical-device-intelligence/internal/sources"
)

const eventSourceName = "openfda_device_event"

// countStub replaces http.DefaultTransport: every upstream is answered locally.
// api.fda.gov count=date_received gets the daily buckets, any other count gets a
// one-term distribution, a plain query gets total 0. No live call is possible.
type countStub struct {
	mu      sync.Mutex
	reqs    []*url.URL
	daily   string        // body for count=date_received
	status  int           // status for count=date_received (0 = 200)
	latency time.Duration // delay before answering count=date_received
}

func (s *countStub) RoundTrip(r *http.Request) (*http.Response, error) {
	u := *r.URL
	s.mu.Lock()
	s.reqs = append(s.reqs, &u)
	s.mu.Unlock()

	status, body := http.StatusOK, `{"meta":{"results":{"total":0}},"results":[]}`
	switch r.URL.Host {
	case "api.fda.gov":
		q := r.URL.Query()
		switch {
		case q.Get("count") == "date_received":
			time.Sleep(s.latency)
			body = s.daily
			if s.status != 0 {
				status, body = s.status, `{"error":{"code":"OVER_RATE_LIMIT","message":"You have exceeded your rate limit."}}`
			}
		case q.Get("count") != "":
			body = `{"results":[{"term":"Malfunction","count":5}]}`
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

// eventReqs splits the api.fda.gov /device/event.json requests the stub saw into
// date_received count requests and requests whose search carries a date range.
func (s *countStub) eventReqs() (counts, ranged int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.reqs {
		if u.Host != "api.fda.gov" || u.Path != "/device/event.json" {
			continue
		}
		q := u.Query()
		if q.Get("count") == "date_received" {
			counts++
		}
		if strings.Contains(q.Get("search"), "date_received:[") {
			ranged++
		}
	}
	return counts, ranged
}

// uniq keeps each run's term out of the singleton's 5-minute cache (-count=N).
func uniq(s string) string { return fmt.Sprintf("%s %d", s, time.Now().UnixNano()) }

func installStub(t *testing.T, stub *countStub) {
	t.Helper()
	restoreKey := cliutil.SetOpenFDAKey("")
	t.Cleanup(restoreKey)
	old := http.DefaultTransport
	http.DefaultTransport = stub
	t.Cleanup(func() { http.DefaultTransport = old })
}

// useEventSource swaps the registered MAUDE source and restores it afterwards.
func useEventSource(t *testing.T, s sources.Source) {
	t.Helper()
	orig, ok := sources.Get(eventSourceName)
	if !ok {
		t.Fatal("event source not registered")
	}
	sources.Register(s)
	t.Cleanup(func() { sources.Register(orig) })
}

// fxFetch is a Fetch-only event source over fixed daily buckets: its date-range
// total is inclusive on both ends, exactly like openFDA's [from TO to].
type fxFetch struct {
	buckets map[string]int
	mu      sync.Mutex
	fetches []sources.Query
}

func (f *fxFetch) Name() string                 { return eventSourceName }
func (f *fxFetch) IDField() string              { return "report_number" }
func (f *fxFetch) Health(context.Context) error { return nil }
func (f *fxFetch) Fetch(_ context.Context, q sources.Query) ([]sources.RawRecord, sources.Page, error) {
	f.mu.Lock()
	f.fetches = append(f.fetches, q)
	f.mu.Unlock()
	n := 0
	for d, c := range f.buckets {
		if d >= q.DateFrom && d <= q.DateTo {
			n += c
		}
	}
	return nil, sources.Page{Total: n}, nil
}

// fxCount adds DailyCounts over the same buckets.
type fxCount struct{ *fxFetch }

func (f fxCount) DailyCounts(context.Context, string) (sources.DailyCounts, error) {
	return sources.NewDailyCounts(f.buckets), nil
}

// recData records every EventTotalWindow window the real analyzers ask for.
type recData struct {
	Data
	mu      sync.Mutex
	windows [][2]string
}

func (r *recData) EventTotalWindow(ctx context.Context, d, from, to string) (int, error) {
	r.mu.Lock()
	r.windows = append(r.windows, [2]string{from, to})
	r.mu.Unlock()
	return r.Data.EventTotalWindow(ctx, d, from, to)
}

func (r *recData) distinct() [][2]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[[2]string]bool{}
	var out [][2]string
	for _, w := range r.windows {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// boundaryBuckets puts records on every window's first day, last day and the day
// before/after each (the one-day gaps), with distinct counts per day.
func boundaryBuckets(t *testing.T, ws [][2]string) map[string]int {
	t.Helper()
	b := map[string]int{}
	i := 0
	for _, w := range ws {
		for _, off := range []struct {
			day string
			d   int
		}{{w[0], -1}, {w[0], 0}, {w[0], 1}, {w[1], -1}, {w[1], 0}, {w[1], 1}} {
			i++
			b[parseDay(t, off.day).AddDate(0, 0, off.d).Format(day)] += 1 + i%9
		}
	}
	return b
}

// harvestWindows runs a whole Synthesize through a recording Data over an empty
// Fetch-only source and returns the windows the real analyzers used.
func harvestWindows(t *testing.T, term string) [][2]string {
	t.Helper()
	installStub(t, &countStub{daily: `{"results":[]}`})
	useEventSource(t, &fxFetch{buckets: map[string]int{}})
	rec := &recData{Data: NewLiveData()}
	if _, err := NewSynthesisAnalyzer(rec).Synthesize(context.Background(), term); err != nil {
		t.Fatal(err)
	}
	ws := rec.distinct()
	if len(ws) < 6 {
		t.Fatalf("expected the analyzers to use several windows, got %v", ws)
	}
	return ws
}

// W1: a whole Synthesize makes exactly one count=date_received request and none
// whose search carries a date range.
func TestW1SynthesizeMakesOneCountRequest(t *testing.T) {
	fixClock(t)
	stub := &countStub{daily: `{"results":[{"time":"20260601","count":3},{"time":"20260101","count":2}]}`}
	installStub(t, stub)
	if _, err := NewSynthesisAnalyzer(NewLiveData()).Synthesize(context.Background(), uniq("w1 pacemaker")); err != nil {
		t.Fatal(err)
	}
	counts, ranged := stub.eventReqs()
	if counts != 1 || ranged != 0 {
		t.Errorf("count=date_received requests=%d (want 1), date-range requests=%d (want 0)", counts, ranged)
	}
}

// W2: every window the real analyzers ask for has the same value on the count
// path as on the Fetch path, with records on each boundary and gap day.
func TestW2WindowValuesEqualFetchPath(t *testing.T) {
	fixClock(t)
	ws := harvestWindows(t, "w2 probe")
	buckets := boundaryBuckets(t, ws)

	ctx := context.Background()
	fetchSrc := &fxFetch{buckets: buckets}
	useEventSource(t, fetchSrc)
	want := make([]int, len(ws))
	for i, w := range ws {
		n, err := NewLiveData().EventTotalWindow(ctx, "w2 device", w[0], w[1])
		if err != nil {
			t.Fatal(err)
		}
		want[i] = n
	}
	if len(fetchSrc.fetches) != len(ws) {
		t.Fatalf("fetch path made %d fetches for %d windows", len(fetchSrc.fetches), len(ws))
	}

	useEventSource(t, fxCount{&fxFetch{buckets: buckets}})
	nonzero := 0
	for i, w := range ws {
		got, err := NewLiveData().EventTotalWindow(ctx, "w2 device", w[0], w[1])
		if err != nil {
			t.Fatal(err)
		}
		if got != want[i] {
			t.Errorf("window %v: count path %d, Fetch path %d", w, got, want[i])
		}
		if got > 0 {
			nonzero++
		}
	}
	if nonzero == 0 {
		t.Error("fixture is vacuous: every window is zero")
	}
}

// W3: the signals of a whole Synthesize are identical on both paths.
func TestW3SignalsEqualFetchPath(t *testing.T) {
	fixClock(t)
	ws := harvestWindows(t, "w3 probe")
	buckets := boundaryBuckets(t, ws)
	ctx := context.Background()

	run := func(src sources.Source) *IntelligenceDossier {
		installStub(t, &countStub{daily: `{"results":[]}`})
		useEventSource(t, src)
		d, err := NewSynthesisAnalyzer(NewLiveData()).Synthesize(ctx, "w3 device")
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	viaFetch := run(&fxFetch{buckets: buckets})
	viaCount := run(fxCount{&fxFetch{buckets: buckets}})

	have := map[string]bool{}
	for _, s := range viaCount.Signals {
		have[s.SignalType] = true
	}
	if !have[SignalVolumeShift] || !have[SignalLifecyclePhase] {
		t.Fatalf("volume-shift and lifecycle signals must be present, got %v", have)
	}
	if !reflect.DeepEqual(viaFetch.Signals, viaCount.Signals) {
		t.Errorf("signals differ:\nfetch: %+v\ncount: %+v", viaFetch.Signals, viaCount.Signals)
	}
	if viaFetch.AttentionIndex != viaCount.AttentionIndex || !reflect.DeepEqual(viaFetch.Notes, viaCount.Notes) {
		t.Errorf("index/notes differ: %v %v vs %v %v", viaFetch.AttentionIndex, viaFetch.Notes, viaCount.AttentionIndex, viaCount.Notes)
	}
}

// W4: a 429 on the count: every probe that reads a window reports the
// rate-limited class, the dossier is flagged, and the run made ONE count request.
func TestW4RateLimitedCountIsOneRequest(t *testing.T) {
	fixClock(t)
	stub := &countStub{status: http.StatusTooManyRequests, latency: 150 * time.Millisecond}
	installStub(t, stub)
	d, err := NewSynthesisAnalyzer(NewLiveData()).Synthesize(context.Background(), uniq("w4 rate limited"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.RateLimited {
		t.Error("d.RateLimited must be true")
	}
	for _, name := range []string{"anomaly/volume-shift", "lifecycle/phase"} {
		want := name + " unavailable: upstream rate limit reached"
		found := false
		for _, n := range d.Notes {
			if n == want {
				found = true
			}
			if strings.Contains(n, "://") || strings.Contains(n, "api.fda.gov") {
				t.Errorf("note carries an upstream URL: %q", n)
			}
		}
		if !found {
			t.Errorf("missing note %q in %q", want, d.Notes)
		}
	}
	if counts, _ := stub.eventReqs(); counts != 1 {
		t.Errorf("count=date_received requests=%d, want exactly 1", counts)
	}
}

// W5: the trend handler's call (through the registered singleton) and the dossier
// for the same term share one upstream count request.
func TestW5TrendAndDossierShareOneCountRequest(t *testing.T) {
	fixClock(t)
	stub := &countStub{daily: `{"results":[{"time":"20260601","count":3}]}`}
	installStub(t, stub)
	src, _ := sources.Get(eventSourceName)
	dc, ok := src.(sources.DailyCounter)
	if !ok {
		t.Fatal("registered event source must implement DailyCounter")
	}
	term := uniq("w5 shared")
	if _, err := dc.DailyCounts(context.Background(), term); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSynthesisAnalyzer(NewLiveData()).Synthesize(context.Background(), term); err != nil {
		t.Fatal(err)
	}
	if counts, ranged := stub.eventReqs(); counts != 1 || ranged != 0 {
		t.Errorf("trend + dossier: count requests=%d (want 1), date-range requests=%d (want 0)", counts, ranged)
	}
}

// W6: a source without DailyCounts keeps the Fetch path.
func TestW6FetchOnlySourceKeepsFetchFallback(t *testing.T) {
	f := &fxFetch{buckets: map[string]int{"20260105": 4, "20260110": 6}}
	useEventSource(t, f)
	n, err := NewLiveData().EventTotalWindow(context.Background(), "w6", "20260101", "20260131")
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Errorf("total=%d want 10", n)
	}
	if len(f.fetches) != 1 {
		t.Fatalf("Fetch calls=%d want 1", len(f.fetches))
	}
	q := f.fetches[0]
	if q.DateField != "date_received" || q.DateFrom != "20260101" || q.DateTo != "20260131" || q.Limit != 1 || q.Term != "w6" {
		t.Errorf("fallback query changed: %+v", q)
	}
}
