package intelligence

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// memoStub replaces http.DefaultTransport. It records every api.fda.gov request
// and answers locally; no live call is possible.
type memoStub struct {
	mu          sync.Mutex
	reqs        []*url.URL
	eventStatus int           // status for count=event_type.exact with a device clause (0 = 200)
	delay       time.Duration // delay before answering that count, so concurrent probes overlap
}

func (s *memoStub) RoundTrip(r *http.Request) (*http.Response, error) {
	u := *r.URL
	s.mu.Lock()
	s.reqs = append(s.reqs, &u)
	s.mu.Unlock()

	status, body := http.StatusOK, `{"meta":{"results":{"total":0}},"results":[]}`
	switch r.URL.Host {
	case "api.fda.gov":
		q := r.URL.Query()
		switch {
		case q.Get("count") == "event_type.exact" && q.Get("search") != "":
			time.Sleep(s.delay)
			body = `{"results":[{"term":"Malfunction","count":50},{"term":"Injury","count":20},{"term":"Death","count":2}]}`
			if s.eventStatus != 0 {
				status, body = s.eventStatus, `{"error":{"code":"OVER_RATE_LIMIT","message":"You have exceeded your rate limit."}}`
			}
		case q.Get("count") == "date_received":
			body = `{"results":[{"time":"20260601","count":3}]}`
		case q.Get("count") != "":
			body = `{"results":[{"term":"Malfunction","count":5}]}`
		case r.URL.Path == "/device/enforcement.json":
			body = `{"meta":{"results":{"total":7}},"results":[]}`
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

// deviceReqs counts, for one device term, the event_type count requests and the
// RecallTotal requests (enforcement, limit=1, no count) the stub saw.
func (s *memoStub) deviceReqs(term string) (eventType, recall int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.reqs {
		if u.Host != "api.fda.gov" {
			continue
		}
		q := u.Query()
		if !strings.Contains(q.Get("search"), `"`+term+`"`) {
			continue
		}
		switch {
		case u.Path == "/device/event.json" && q.Get("count") == "event_type.exact":
			eventType++
		case u.Path == "/device/enforcement.json" && q.Get("count") == "" && q.Get("limit") == "1":
			recall++
		}
	}
	return eventType, recall
}

func installMemoStub(t *testing.T, stub *memoStub) {
	t.Helper()
	installStub(t, &countStub{}) // key + transport restore; replaced just below
	old := http.DefaultTransport
	http.DefaultTransport = stub
	t.Cleanup(func() { http.DefaultTransport = old })
}

// R1: one Synthesize sends exactly one event_type count and one RecallTotal.
func TestR1OneRunOneEventTypeCountOneRecallTotal(t *testing.T) {
	fixClock(t)
	stub := &memoStub{}
	installMemoStub(t, stub)
	term := "r1pump" + uniq("")[1:]
	if _, err := NewSynthesisAnalyzer(NewLiveData()).Synthesize(context.Background(), term); err != nil {
		t.Fatal(err)
	}
	et, rc := stub.deviceReqs(term)
	if et != 1 || rc != 1 {
		t.Errorf("event_type=%d recall=%d, want 1 and 1", et, rc)
	}
}

// R2: the memo lives for one run only; a second run asks upstream again.
func TestR2EachRunAsksAgain(t *testing.T) {
	fixClock(t)
	stub := &memoStub{}
	installMemoStub(t, stub)
	term := "r2pump" + uniq("")[1:]
	a := NewSynthesisAnalyzer(NewLiveData())
	for i := 0; i < 2; i++ {
		if _, err := a.Synthesize(context.Background(), term); err != nil {
			t.Fatal(err)
		}
	}
	et, rc := stub.deviceReqs(term)
	if et != 2 || rc != 2 {
		t.Errorf("two runs: event_type=%d recall=%d, want 2 and 2", et, rc)
	}
}

// R3: the dossier equals the one the unwrapped data produces.
func TestR3SignalsAndNotesIdenticalWithMemo(t *testing.T) {
	pinClock(t)
	for name, data := range map[string]Data{"full": synthesisFixture(), "partial": flakyData{synthesisFixture()}} {
		raw := &SynthesisAnalyzer{data: data}
		ps := raw.probes()
		want := raw.runProbes(context.Background(), "pacemaker", ps)
		d, err := NewSynthesisAnalyzer(data).Synthesize(context.Background(), "pacemaker")
		if err != nil {
			t.Fatal(err)
		}
		var sigs []Signal
		var notes []string
		for i, p := range ps {
			if want[i].err != nil {
				msg := strings.SplitN(want[i].err.Error(), "\n", 2)[0]
				_ = msg
				notes = append(notes, p.name)
				continue
			}
			sigs = append(sigs, *want[i].sig)
		}
		if !reflect.DeepEqual(sigs, d.Signals) {
			t.Errorf("%s: signals differ\n got  %+v\n want %+v", name, d.Signals, sigs)
		}
		if len(d.Notes) != len(notes) {
			t.Errorf("%s: notes=%v want one per failed probe %v", name, d.Notes, notes)
		}
		for i, n := range notes {
			if i < len(d.Notes) && !strings.HasPrefix(d.Notes[i], n+" unavailable") {
				t.Errorf("%s: note %d=%q want prefix %q", name, i, d.Notes[i], n)
			}
		}
		if name == "full" && d.SignalsMeasured != 3 {
			t.Errorf("measured=%d want 3", d.SignalsMeasured)
		}
	}
}

// R4: a 429 on the event_type count: every probe that reads it reports the
// rate-limited class, the dossier is flagged, and the run made ONE request.
func TestR4RateLimitedEventTypeCountIsOneRequest(t *testing.T) {
	fixClock(t)
	stub := &memoStub{eventStatus: http.StatusTooManyRequests, delay: 50 * time.Millisecond}
	installMemoStub(t, stub)
	term := "r4pump" + uniq("")[1:]
	d, err := NewSynthesisAnalyzer(NewLiveData()).Synthesize(context.Background(), term)
	if err != nil {
		t.Fatal(err)
	}
	if !d.RateLimited {
		t.Error("d.RateLimited must be true")
	}
	// Six probes read EventTypeCounts; volume (telemetry) and corroboration also
	// need other feeds first, so judge by the count of rate-limited notes.
	limited := 0
	for _, n := range d.Notes {
		if strings.Contains(n, "://") || strings.Contains(n, "api.fda.gov") {
			t.Errorf("note carries an upstream URL: %q", n)
		}
		if strings.HasSuffix(n, "unavailable: upstream rate limit reached") {
			limited++
		}
	}
	if limited < 4 {
		t.Errorf("rate-limited notes=%d, want every probe that uses the count: %q", limited, d.Notes)
	}
	if et, _ := stub.deviceReqs(term); et != 1 {
		t.Errorf("event_type requests=%d, want exactly 1", et)
	}
}

// R5: concurrent probes share one flight. The stub answers slowly, so every
// probe that starts before the first answer lands must join it.
func TestR5ConcurrentProbesShareOneFlight(t *testing.T) {
	fixClock(t)
	stub := &memoStub{delay: 300 * time.Millisecond}
	installMemoStub(t, stub)
	term := "r5pump" + uniq("")[1:]
	if _, err := NewSynthesisAnalyzer(NewLiveData()).Synthesize(context.Background(), term); err != nil {
		t.Fatal(err)
	}
	if et, _ := stub.deviceReqs(term); et != 1 {
		t.Errorf("event_type requests=%d, want exactly 1 (shared flight)", et)
	}
}

// countingData counts the calls that reach the wrapped Data.
type countingData struct {
	mockData
	mu     sync.Mutex
	events map[string]int
	recall map[string]int
}

func (c *countingData) EventTypeCounts(_ context.Context, device string) (map[string]int, error) {
	c.mu.Lock()
	c.events[device]++
	c.mu.Unlock()
	return map[string]int{"Death": len(device)}, nil
}

func (c *countingData) RecallTotal(_ context.Context, device string) (int, error) {
	c.mu.Lock()
	c.recall[device]++
	c.mu.Unlock()
	return len(device), nil
}

// R6: a different argument is a different key, a repeat is not.
func TestR6DifferentArgumentIsDifferentKey(t *testing.T) {
	cd := &countingData{events: map[string]int{}, recall: map[string]int{}}
	m := newRunMemo(cd)
	ctx := context.Background()
	for _, dev := range []string{"pump", "pump", "stent", "stent"} {
		got, err := m.EventTypeCounts(ctx, dev)
		if err != nil || got["Death"] != len(dev) {
			t.Fatalf("EventTypeCounts(%q)=%v,%v", dev, got, err)
		}
		if n, err := m.RecallTotal(ctx, dev); err != nil || n != len(dev) {
			t.Fatalf("RecallTotal(%q)=%v,%v", dev, n, err)
		}
	}
	for _, dev := range []string{"pump", "stent"} {
		if cd.events[dev] != 1 || cd.recall[dev] != 1 {
			t.Errorf("%q: upstream event=%d recall=%d, want 1 and 1", dev, cd.events[dev], cd.recall[dev])
		}
	}
	// The two methods never share a key, and a caller's map is its own copy.
	a, _ := m.EventTypeCounts(ctx, "pump")
	a["Death"] = -1
	if b, _ := m.EventTypeCounts(ctx, "pump"); b["Death"] != len("pump") {
		t.Errorf("a caller changed the shared answer: %v", b)
	}
}
