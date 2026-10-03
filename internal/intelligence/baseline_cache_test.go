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
)

// baseStub replaces http.DefaultTransport. It answers every upstream locally and
// counts the two device-independent baseline requests (no search term):
// count=event_type.exact and count=device.generic_name.exact.
type baseStub struct {
	mu      sync.Mutex
	reqs    []*url.URL
	emptyG  bool          // answer the global event_type count with 200 and no results
	emptyV  bool          // answer the generic_name count with 200 and no results
	globalS int           // status for the global event_type count (0 = 200)
	volumeS int           // status for the generic_name count (0 = 200)
	gate    chan struct{} // when set, baseline requests wait for it (or their context)
	arrived chan struct{} // receives one value per baseline request that arrived
}

func (s *baseStub) RoundTrip(r *http.Request) (*http.Response, error) {
	u := *r.URL
	s.mu.Lock()
	s.reqs = append(s.reqs, &u)
	s.mu.Unlock()

	q := r.URL.Query()
	status, body := http.StatusOK, `{"meta":{"results":{"total":0}},"results":[]}`
	if r.URL.Host == "api.fda.gov" {
		baseline := q.Get("search") == "" && (q.Get("count") == "event_type.exact" || q.Get("count") == "device.generic_name.exact")
		if baseline && s.arrived != nil {
			s.arrived <- struct{}{}
		}
		if baseline && s.gate != nil {
			select {
			case <-s.gate:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		switch {
		case q.Get("count") == "device.generic_name.exact" && q.Get("search") == "":
			var b strings.Builder
			b.WriteString(`{"results":[`)
			for i := 0; i < 100; i++ {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"term":"type %d","count":%d}`, i, 1000+i*37)
			}
			b.WriteString(`]}`)
			body = b.String()
			if s.volumeS != 0 {
				status, body = s.volumeS, `{"error":{"code":"X","message":"x"}}`
			}
			if s.emptyV {
				body = `{"results":[]}`
			}
		case q.Get("count") == "event_type.exact" && q.Get("search") == "":
			body = `{"results":[{"term":"Malfunction","count":9000},{"term":"Injury","count":3000},{"term":"Death","count":300}]}`
			if s.globalS != 0 {
				status, body = s.globalS, `{"error":{"code":"X","message":"x"}}`
			}
			if s.emptyG {
				body = `{"results":[]}`
			}
		case q.Get("count") == "event_type.exact":
			body = `{"results":[{"term":"Malfunction","count":50},{"term":"Injury","count":20},{"term":"Death","count":2}]}`
		case q.Get("count") == "date_received":
			body = `{"results":[{"time":"20260601","count":3},{"time":"20260101","count":2}]}`
		case q.Get("count") != "":
			body = `{"results":[{"term":"Malfunction","count":5}]}`
		}
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

// baselineReqs returns how many global event_type and generic_name count
// requests (no search term) the stub saw.
func (s *baseStub) baselineReqs() (global, volume int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.reqs {
		if u.Host != "api.fda.gov" || u.Path != "/device/event.json" {
			continue
		}
		q := u.Query()
		if q.Get("search") != "" {
			continue
		}
		switch q.Get("count") {
		case "event_type.exact":
			global++
		case "device.generic_name.exact":
			volume++
		}
	}
	return global, volume
}

// freshBaselines empties the process-wide cache before and after a test and
// restores the clock.
func freshBaselines(t *testing.T) {
	t.Helper()
	baselines.reset()
	oldNow := baselines.now
	t.Cleanup(func() {
		baselines.reset()
		baselines.now = oldNow
	})
}

func newBaseStub(t *testing.T) *baseStub {
	t.Helper()
	stub := &baseStub{}
	installStub(t, &countStub{}) // key + transport restore; replaced just below
	installBase(t, stub)
	return stub
}

func installBase(t *testing.T, stub *baseStub) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = stub
	t.Cleanup(func() { http.DefaultTransport = old })
}

// G1: two Synthesize runs for two different devices inside the TTL: each baseline
// request goes upstream exactly once.
func TestG1TwoDevicesShareBaselineRequests(t *testing.T) {
	fixClock(t)
	freshBaselines(t)
	stub := newBaseStub(t)
	a := NewSynthesisAnalyzer(NewLiveData())
	for _, dev := range []string{"g1 pacemaker", "g1 stent"} {
		if _, err := a.Synthesize(context.Background(), uniq(dev)); err != nil {
			t.Fatal(err)
		}
	}
	if g, v := stub.baselineReqs(); g != 1 || v != 1 {
		t.Errorf("baseline requests: global event_type=%d (want 1), generic_name=%d (want 1)", g, v)
	}
}

// G2: after 6 h the next call refetches; just before, it does not.
func TestG2RefetchAfterTTL(t *testing.T) {
	freshBaselines(t)
	stub := newBaseStub(t)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	baselines.now = func() time.Time { return now }
	d := NewLiveData()
	ctx := context.Background()
	call := func() {
		t.Helper()
		if _, err := d.GlobalEventTypeCounts(ctx); err != nil {
			t.Fatal(err)
		}
		if _, _, err := d.VolumeBaseline(ctx); err != nil {
			t.Fatal(err)
		}
	}
	call()
	now = now.Add(6*time.Hour - time.Second)
	call()
	if g, v := stub.baselineReqs(); g != 1 || v != 1 {
		t.Fatalf("inside the TTL: global=%d volume=%d, want 1 and 1", g, v)
	}
	now = now.Add(2 * time.Second)
	call()
	if g, v := stub.baselineReqs(); g != 2 || v != 2 {
		t.Errorf("after 6 h: global=%d volume=%d, want 2 and 2 (refetch)", g, v)
	}
}

// G3: a 429 and a 503 are never cached; the next call asks again.
func TestG3ErrorsAreNotCached(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			freshBaselines(t)
			stub := newBaseStub(t)
			stub.globalS, stub.volumeS = status, status
			d := NewLiveData()
			ctx := context.Background()
			for i := 0; i < 2; i++ {
				if _, err := d.GlobalEventTypeCounts(ctx); err == nil {
					t.Fatal("global: expected an error")
				}
				if _, _, err := d.VolumeBaseline(ctx); err == nil {
					t.Fatal("volume: expected an error")
				}
			}
			if g, v := stub.baselineReqs(); g < 2 || v < 2 {
				t.Errorf("status %d: global=%d volume=%d, want both >= 2 (errors must not be cached)", status, g, v)
			}
			// the failure clears: once upstream is healthy the value is served
			stub.globalS, stub.volumeS = 0, 0
			if m, err := d.GlobalEventTypeCounts(ctx); err != nil || len(m) == 0 {
				t.Errorf("after recovery: %v %v", m, err)
			}
		})
	}
}

// G4: N concurrent callers make one upstream request per baseline.
func TestG4ConcurrentCallersShareOneRequest(t *testing.T) {
	freshBaselines(t)
	stub := newBaseStub(t)
	stub.gate = make(chan struct{})
	stub.arrived = make(chan struct{}, 64)
	d := NewLiveData()
	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := d.GlobalEventTypeCounts(context.Background())
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, _, err := d.VolumeBaseline(context.Background())
			errs <- err
		}()
	}
	// let every caller reach the cache while the first request is held open
	time.Sleep(200 * time.Millisecond)
	close(stub.gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if g, v := stub.baselineReqs(); g != 1 || v != 1 {
		t.Errorf("%d concurrent callers: global=%d volume=%d upstream requests, want 1 and 1", n, g, v)
	}
}

// G5: the first caller is cancelled; a joiner still gets the value.
func TestG5CancelledFirstCallerDoesNotFailJoiner(t *testing.T) {
	freshBaselines(t)
	stub := newBaseStub(t)
	stub.gate = make(chan struct{})
	stub.arrived = make(chan struct{}, 8)
	d := NewLiveData()

	ctx1, cancel1 := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := d.GlobalEventTypeCounts(ctx1)
		first <- err
	}()
	<-stub.arrived // the upstream request is in flight

	type res struct {
		m   map[string]int
		err error
	}
	joined := make(chan res, 1)
	go func() {
		m, err := d.GlobalEventTypeCounts(context.Background())
		joined <- res{m, err}
	}()
	time.Sleep(100 * time.Millisecond) // the joiner is now waiting on the same flight
	cancel1()
	if err := <-first; err == nil {
		t.Error("the cancelled first caller should see its own context error")
	}
	close(stub.gate)
	select {
	case r := <-joined:
		if r.err != nil || len(r.m) == 0 {
			t.Errorf("joiner: %v, %v (want the value)", r.m, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("joiner never returned")
	}
	if g, _ := stub.baselineReqs(); g != 1 {
		t.Errorf("global requests=%d, want 1", g)
	}
}

// G6: signals, attention index and notes are identical on a cache miss and a
// cache hit for the same fixture, and the cached baseline really feeds them.
func TestG6CachedRunEqualsUncachedRun(t *testing.T) {
	fixClock(t)
	freshBaselines(t)
	stub := newBaseStub(t)
	term := uniq("g6 device")
	run := func() *IntelligenceDossier {
		d, err := NewSynthesisAnalyzer(NewLiveData()).Synthesize(context.Background(), term)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	cold := run() // fills the cache
	warm := run() // served from it
	if g, v := stub.baselineReqs(); g != 1 || v != 1 {
		t.Fatalf("the warm run must come from the cache: global=%d volume=%d", g, v)
	}
	if len(cold.Signals) == 0 {
		t.Fatal("fixture is vacuous: no signals")
	}
	if !reflect.DeepEqual(cold.Signals, warm.Signals) {
		t.Errorf("signals differ:\ncold: %+v\nwarm: %+v", cold.Signals, warm.Signals)
	}
	if cold.AttentionIndex != warm.AttentionIndex || !reflect.DeepEqual(cold.Notes, warm.Notes) {
		t.Errorf("index/notes differ: %v %v vs %v %v", cold.AttentionIndex, cold.Notes, warm.AttentionIndex, warm.Notes)
	}
	// the value itself: a hit must equal a fresh fetch, not an empty/stale map
	d := NewLiveData()
	hit, err := d.GlobalEventTypeCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"Malfunction": 9000, "Injury": 3000, "Death": 300}; !reflect.DeepEqual(hit, want) {
		t.Errorf("cached global counts = %v, want %v", hit, want)
	}
	p95, sample, err := d.VolumeBaseline(context.Background())
	if err != nil || sample != 100 || p95 != 1000+94*37 {
		t.Errorf("cached volume baseline = p95 %d sample %d err %v, want p95 %d sample 100", p95, sample, err, 1000+94*37)
	}
}

// G8: an empty successful baseline is returned but not kept: the next call asks
// upstream again and returns the real value.
func TestG8EmptyResultIsNotCached(t *testing.T) {
	freshBaselines(t)
	stub := newBaseStub(t)
	stub.emptyG, stub.emptyV = true, true
	d := NewLiveData()
	ctx := context.Background()

	m, err := d.GlobalEventTypeCounts(ctx)
	if err != nil || len(m) != 0 {
		t.Fatalf("empty global answer: %v, %v (want an empty map and no error)", m, err)
	}
	p95, sample, err := d.VolumeBaseline(ctx)
	if err != nil || sample != 0 || p95 != 0 {
		t.Fatalf("empty volume answer: p95 %d sample %d err %v (want 0, 0, nil)", p95, sample, err)
	}

	stub.emptyG, stub.emptyV = false, false
	m, err = d.GlobalEventTypeCounts(ctx)
	if err != nil || !reflect.DeepEqual(m, map[string]int{"Malfunction": 9000, "Injury": 3000, "Death": 300}) {
		t.Errorf("global after the empty answer: %v, %v (want the real value)", m, err)
	}
	p95, sample, err = d.VolumeBaseline(ctx)
	if err != nil || sample != 100 || p95 != 1000+94*37 {
		t.Errorf("volume after the empty answer: p95 %d sample %d err %v (want p95 %d sample 100)", p95, sample, err, 1000+94*37)
	}
	if g, v := stub.baselineReqs(); g != 2 || v != 2 {
		t.Errorf("upstream requests: global=%d volume=%d, want 2 and 2 (the empty answer must not be cached)", g, v)
	}
}
