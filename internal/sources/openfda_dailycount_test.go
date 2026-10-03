package sources

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
)

// countStub replaces http.DefaultTransport. respond picks the answer for each
// request; block, when set, holds every request until it is closed or the
// request's own context ends (so a cancelled context shows up as an error).
type countStub struct {
	mu      sync.Mutex
	reqs    []*url.URL
	respond func(n int, r *http.Request) (int, string)
	block   chan struct{}
	arrived chan struct{} // receives one value per request that reached the stub
}

func (s *countStub) RoundTrip(r *http.Request) (*http.Response, error) {
	u := *r.URL
	s.mu.Lock()
	s.reqs = append(s.reqs, &u)
	n := len(s.reqs)
	s.mu.Unlock()
	if s.arrived != nil {
		s.arrived <- struct{}{}
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	status, body := s.respond(n, r)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func (s *countStub) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

const stubDaily = `{"results":[{"time":"20240101","count":3},{"time":"20241231","count":4}]}`

func okDaily(int, *http.Request) (int, string) { return http.StatusOK, stubDaily }

// newCountSource is an event source over the stub, keyless, with a 1 ms retry
// backoff so a 503 does not slow the test, and a controllable clock.
func newCountSource(t *testing.T, stub *countStub) (*OpenFDADeviceEvent, *time.Time) {
	t.Helper()
	restoreKey := cliutil.SetOpenFDAKey("")
	t.Cleanup(restoreKey)
	old := http.DefaultTransport
	http.DefaultTransport = stub
	t.Cleanup(func() { http.DefaultTransport = old })

	client := cliutil.NewOpenFDAClient("https://api.fda.gov")
	client.Backoff = time.Millisecond
	s := &OpenFDADeviceEvent{client: client}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	s.daily.now = func() time.Time { return now }
	return s, &now
}

var _ DailyCounter = (*OpenFDADeviceEvent)(nil)

func TestEventImplementsDailyCounter(t *testing.T) {
	src, ok := Get("openfda_device_event")
	if !ok {
		t.Fatal("event source must self-register")
	}
	if _, ok := src.(DailyCounter); !ok {
		t.Error("event source must implement DailyCounter")
	}
}

// T1: one count=date_received request, the device clause only: no date range,
// no limit, nothing else.
func TestDailyCountsRequestShape(t *testing.T) {
	stub := &countStub{respond: okDaily}
	s, _ := newCountSource(t, stub)

	dc, err := s.DailyCounts(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	if got := stub.calls(); got != 1 {
		t.Fatalf("want exactly 1 upstream request, got %d", got)
	}
	u := stub.reqs[0]
	if u.Path != "/device/event.json" {
		t.Errorf("path=%q", u.Path)
	}
	q := u.Query()
	if q.Get("count") != "date_received" {
		t.Errorf("count=%q want date_received", q.Get("count"))
	}
	if want := `(device.generic_name:"pacemaker" OR device.brand_name:"pacemaker")`; q.Get("search") != want {
		t.Errorf("search=%q want %q", q.Get("search"), want)
	}
	if _, ok := q["limit"]; ok {
		t.Errorf("a limit must not be sent (it would cap the buckets): %s", u)
	}
	if strings.Contains(u.RawQuery, "date_received%3A%5B") {
		t.Errorf("a date range must not be sent: %s", u)
	}
	if len(q) != 2 {
		t.Errorf("only count and search expected, got %v", q)
	}
	if dc.Total() != 7 || dc.Len() != 2 {
		t.Errorf("parsed total=%d len=%d want 7 and 2", dc.Total(), dc.Len())
	}
}

func TestDailyCountsSumIsInclusiveOnBothEnds(t *testing.T) {
	dc := NewDailyCounts(map[string]int{
		"20161231": 1, "20170101": 2, "20170615": 4, "20171231": 8, "20180101": 16,
		"bogus": 99, "2017": 99,
	})
	if dc.Len() != 5 {
		t.Fatalf("keys that are not YYYYMMDD must be dropped: len=%d want 5", dc.Len())
	}
	if got := dc.Sum("20170101", "20171231"); got != 14 {
		t.Errorf("Sum(2017) = %d want 14 (Jan 1 and Dec 31 included, neighbours excluded)", got)
	}
	if got := dc.Sum("20170102", "20171230"); got != 4 {
		t.Errorf("Sum(Jan 2..Dec 30) = %d want 4", got)
	}
	if got := dc.Sum("20190101", "20191231"); got != 0 {
		t.Errorf("empty range = %d want 0", got)
	}
	if got := dc.Sum("20171231", "20170101"); got != 0 {
		t.Errorf("reversed range = %d want 0", got)
	}
}

// openFDA answered but with no dated bucket at all: that is not "no events".
func TestDailyCountsRejectsUndatedBuckets(t *testing.T) {
	stub := &countStub{respond: func(int, *http.Request) (int, string) {
		return http.StatusOK, `{"results":[{"term":"Injury","count":5}]}`
	}}
	s, _ := newCountSource(t, stub)
	if _, err := s.DailyCounts(context.Background(), "x"); err == nil {
		t.Fatal("a response with no dated bucket must be an error, not an empty result")
	}
}

func TestDailyCountsEmptyTermIsRejectedWithoutARequest(t *testing.T) {
	stub := &countStub{respond: okDaily}
	s, _ := newCountSource(t, stub)
	if _, err := s.DailyCounts(context.Background(), "  "); err == nil {
		t.Fatal("an empty term must error")
	}
	if stub.calls() != 0 {
		t.Errorf("no request expected, got %d", stub.calls())
	}
}

// 404 is openFDA's "no matches": an empty, successful result.
func TestDailyCounts404IsEmptyNotAnError(t *testing.T) {
	stub := &countStub{respond: func(int, *http.Request) (int, string) {
		return http.StatusNotFound, `{"error":{"code":"NOT_FOUND","message":"No matches found!"}}`
	}}
	s, _ := newCountSource(t, stub)
	dc, err := s.DailyCounts(context.Background(), "no such device")
	if err != nil {
		t.Fatalf("404 must be an empty result, got error %v", err)
	}
	if dc.Len() != 0 || dc.Total() != 0 || dc.Sum("19000101", "29991231") != 0 {
		t.Errorf("want an empty result, got len=%d total=%d", dc.Len(), dc.Total())
	}
}

// T3: N concurrent callers for the same device share ONE upstream request.
func TestDailyCountsConcurrentCallersShareOneRequest(t *testing.T) {
	stub := &countStub{respond: okDaily, block: make(chan struct{}), arrived: make(chan struct{}, 64)}
	s, _ := newCountSource(t, stub)

	const callers = 8
	var wg sync.WaitGroup
	errs := make([]error, callers)
	totals := make([]int, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dc, err := s.DailyCounts(context.Background(), "pacemaker")
			errs[i], totals[i] = err, dc.Total()
		}()
	}
	<-stub.arrived                     // the first request is at the stub, held there
	time.Sleep(100 * time.Millisecond) // every other caller has had time to join or to send its own
	close(stub.block)
	wg.Wait()

	if got := stub.calls(); got != 1 {
		t.Errorf("%d concurrent callers made %d upstream requests, want 1", callers, got)
	}
	for i := range callers {
		if errs[i] != nil || totals[i] != 7 {
			t.Errorf("caller %d: total=%d err=%v want 7 and nil", i, totals[i], errs[i])
		}
	}
}

// T4: an answer is reused inside the TTL and fetched again after it.
func TestDailyCountsTTLExpiryRefetches(t *testing.T) {
	stub := &countStub{respond: okDaily}
	s, now := newCountSource(t, stub)
	ctx := context.Background()

	if _, err := s.DailyCounts(ctx, "pacemaker"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(dailyCountsTTL - time.Second)
	if _, err := s.DailyCounts(ctx, "pacemaker"); err != nil {
		t.Fatal(err)
	}
	if got := stub.calls(); got != 1 {
		t.Fatalf("inside the TTL: %d requests, want 1", got)
	}
	*now = now.Add(2 * time.Second) // now TTL + 1 s after the first answer
	if _, err := s.DailyCounts(ctx, "pacemaker"); err != nil {
		t.Fatal(err)
	}
	if got := stub.calls(); got != 2 {
		t.Errorf("after the TTL: %d requests, want 2 (a refetch)", got)
	}
}

// T5: a 429 and a 503 are not cached; the next call asks again, and only a
// success is kept.
func TestDailyCountsErrorsAreNotCached(t *testing.T) {
	status := http.StatusTooManyRequests
	stub := &countStub{respond: func(int, *http.Request) (int, string) {
		if status != http.StatusOK {
			return status, `{"error":{"code":"X","message":"x"}}`
		}
		return http.StatusOK, stubDaily
	}}
	s, _ := newCountSource(t, stub)
	ctx := context.Background()

	if _, err := s.DailyCounts(ctx, "pacemaker"); !cliutil.IsRateLimited(err) {
		t.Fatalf("want a rate-limit error, got %v", err)
	}
	if got := stub.calls(); got != 1 {
		t.Fatalf("a 429 without Retry-After is 1 request, got %d", got)
	}

	status = http.StatusServiceUnavailable
	if _, err := s.DailyCounts(ctx, "pacemaker"); err == nil || cliutil.IsRateLimited(err) {
		t.Fatalf("want a plain unavailable error, got %v", err)
	}
	if got := stub.calls(); got != 4 { // 1 + 3 attempts: the 429 was not remembered
		t.Fatalf("a 503 is retried to 3 attempts after the 429: want 4 requests in total, got %d", got)
	}

	status = http.StatusOK
	dc, err := s.DailyCounts(ctx, "pacemaker")
	if err != nil || dc.Total() != 7 {
		t.Fatalf("after the failures: total=%d err=%v want 7 and nil", dc.Total(), err)
	}
	if got := stub.calls(); got != 5 {
		t.Fatalf("the success needed one request, got %d in total", got-4)
	}
	if _, err := s.DailyCounts(ctx, "pacemaker"); err != nil {
		t.Fatal(err)
	}
	if got := stub.calls(); got != 5 {
		t.Errorf("the success must be cached: want 5 requests in total, got %d", got)
	}
}

// T6: the first caller leaves; the lookup keeps going for the one still waiting.
func TestDailyCountsFirstCallerCancelledSecondStillGetsTheResult(t *testing.T) {
	stub := &countStub{respond: okDaily, block: make(chan struct{}), arrived: make(chan struct{}, 8)}
	s, _ := newCountSource(t, stub)

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	errA := make(chan error, 1)
	go func() {
		_, err := s.DailyCounts(ctxA, "pacemaker")
		errA <- err
	}()
	<-stub.arrived // A's request is at the stub, held there

	type result struct {
		total int
		err   error
	}
	resB := make(chan result, 1)
	go func() {
		dc, err := s.DailyCounts(context.Background(), "pacemaker")
		resB <- result{dc.Total(), err}
	}()
	time.Sleep(100 * time.Millisecond) // B has joined the flight

	cancelA()
	select {
	case err := <-errA:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the cancelled caller should see context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled caller did not return")
	}

	close(stub.block)
	select {
	case r := <-resB:
		if r.err != nil || r.total != 7 {
			t.Errorf("second caller: total=%d err=%v want 7 and nil", r.total, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second caller did not get a result")
	}
	if got := stub.calls(); got != 1 {
		t.Errorf("want 1 upstream request, got %d", got)
	}
}

// The cache holds a bounded number of devices; finished and expired entries are
// swept, so a long-running process cannot grow without limit.
func TestDailyCountsCacheIsBounded(t *testing.T) {
	stub := &countStub{respond: okDaily}
	s, now := newCountSource(t, stub)
	ctx := context.Background()

	for i := range dailyCountsMaxEntries * 3 {
		if _, err := s.DailyCounts(ctx, "device "+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(time.Second)
	}
	s.daily.mu.Lock()
	n := len(s.daily.entries)
	s.daily.mu.Unlock()
	if n > dailyCountsMaxEntries {
		t.Errorf("%d cached devices, want at most %d", n, dailyCountsMaxEntries)
	}

	*now = now.Add(dailyCountsTTL + time.Minute)
	if _, err := s.DailyCounts(ctx, "one more"); err != nil {
		t.Fatal(err)
	}
	s.daily.mu.Lock()
	n = len(s.daily.entries)
	s.daily.mu.Unlock()
	if n != 1 {
		t.Errorf("expired entries must be swept: %d left, want 1", n)
	}
}
