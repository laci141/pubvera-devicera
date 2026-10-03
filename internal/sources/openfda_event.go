package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
)

// OpenFDADeviceEvent is the LIVE openFDA MAUDE adapter. Endpoint:
// https://api.fda.gov/device/event.json. Keyless unless OPENFDA_API_KEY is set.
//
// Severity is derived from the real field event_type (values Death, Injury,
// Malfunction, Other) — NOT from a "serious_adverse_event_flag", which exists in
// the DRUG event schema but not here. This was confirmed against the live API
// before coding: "serious" == Death OR Injury (verified count 326105 for
// pacemaker == Death 16694 + Injury 309411).
type OpenFDADeviceEvent struct {
	client *cliutil.Client
	daily  dailyCache
}

func NewOpenFDADeviceEvent() *OpenFDADeviceEvent {
	return &OpenFDADeviceEvent{client: cliutil.NewOpenFDAClient("https://api.fda.gov")}
}

func (s *OpenFDADeviceEvent) Name() string { return "openfda_device_event" }

// IDField: MAUDE's stable per-report key.
func (s *OpenFDADeviceEvent) IDField() string { return "mdr_report_key" }

// nameClause matches the device by generic or brand name (parenthesized so it
// composes safely under AND).
func nameClause(term string) string {
	return "(" + cliutil.Or(
		cliutil.Phrase("device.generic_name", term),
		cliutil.Phrase("device.brand_name", term),
	) + ")"
}

// severityClause returns the event_type filter for a severity, or "" for all.
func severityClause(sev string) (string, error) {
	switch sev {
	case "", "all":
		return "", nil
	case "death":
		return cliutil.Phrase("event_type", "Death"), nil
	case "serious":
		// Serious == death or (serious) injury, per MDR reporting.
		return "(" + cliutil.Or(
			cliutil.Phrase("event_type", "Death"),
			cliutil.Phrase("event_type", "Injury"),
		) + ")", nil
	default:
		return "", fmt.Errorf("severity must be serious, death, or empty (got %q)", sev)
	}
}

func (s *OpenFDADeviceEvent) buildSearch(q Query) (string, error) {
	if q.Term == "" {
		return "", fmt.Errorf("event search requires a device term")
	}
	clauses := []string{nameClause(q.Term)}
	sev, err := severityClause(q.Severity)
	if err != nil {
		return "", err
	}
	if sev != "" {
		clauses = append(clauses, sev)
	}
	if q.DateFrom != "" && q.DateTo != "" {
		field := q.DateField
		if field == "" {
			field = "date_received"
		}
		clauses = append(clauses, cliutil.DateRange(field, q.DateFrom, q.DateTo))
	}
	return cliutil.And(clauses...), nil
}

func (s *OpenFDADeviceEvent) Fetch(ctx context.Context, q Query) ([]RawRecord, Page, error) {
	search, err := s.buildSearch(q)
	if err != nil {
		return nil, Page{}, err
	}
	params := paramsForSearch(q)
	params.Set("search", search)

	body, _, err := s.client.GetJSON(ctx, "/device/event.json", params)
	if err != nil {
		if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
			return nil, Page{}, nil // no matches → empty, not an error
		}
		return nil, Page{}, err
	}

	var env openFDAEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, Page{}, err
	}
	recs := make([]RawRecord, 0, len(env.Results))
	for _, r := range env.Results {
		id, _ := r[s.IDField()].(string)
		recs = append(recs, RawRecord{ID: id, Raw: r})
	}
	return recs, Page{Total: env.Meta.Results.Total, Returned: len(recs)}, nil
}

// CountEventTypes returns the server-side event_type distribution for the device
// term (no severity filter), so a breakdown reflects the whole result set rather
// than one page. Implements EventCounter.
func (s *OpenFDADeviceEvent) CountEventTypes(ctx context.Context, q Query) (map[string]int, error) {
	if q.Term == "" {
		return nil, fmt.Errorf("count requires a device term")
	}
	params := paramsForSearch(Query{Limit: 1})
	params.Del("limit")
	params.Set("search", nameClause(q.Term))
	params.Set("count", "event_type.exact")

	body, _, err := s.client.GetJSON(ctx, "/device/event.json", params)
	if err != nil {
		if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
			return map[string]int{}, nil
		}
		return nil, err
	}
	return parseCounts(body)
}

// CountField returns the server-side value distribution of any field for the
// query. Unlike CountEventTypes, the device term is OPTIONAL: with an empty
// Term the distribution spans the whole endpoint (e.g. the per-device-type
// event volumes a baseline needs — verified live: count=device.generic_name.exact).
// Implements FieldCounter.
func (s *OpenFDADeviceEvent) CountField(ctx context.Context, q Query, field string) (map[string]int, error) {
	params := paramsForSearch(Query{Limit: 1})
	params.Del("limit")
	var clauses []string
	if q.Term != "" {
		clauses = append(clauses, nameClause(q.Term))
	}
	if q.DateFrom != "" && q.DateTo != "" {
		df := q.DateField
		if df == "" {
			df = "date_received"
		}
		clauses = append(clauses, cliutil.DateRange(df, q.DateFrom, q.DateTo))
	}
	if len(clauses) > 0 {
		params.Set("search", cliutil.And(clauses...))
	}
	params.Set("count", field)
	if q.Limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", q.Limit))
	}
	body, _, err := s.client.GetJSON(ctx, "/device/event.json", params)
	if err != nil {
		if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
			return map[string]int{}, nil
		}
		return nil, err
	}
	return parseCounts(body)
}

// DailyCounts is a device's MAUDE report count per date_received day over the
// whole history, ascending by day. It is stored compactly (one 8-byte pair per
// day, about 80 KB for 10,000 days) because it is cached per device.
type DailyCounts struct {
	buckets []dayCount
}

type dayCount struct {
	day, n int32 // day is YYYYMMDD as a number
}

// parseDay reads a compact YYYYMMDD date. Anything else is not a day.
func parseDay(s string) (int32, bool) {
	if len(s) != 8 {
		return 0, false
	}
	var d int32
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		d = d*10 + int32(s[i]-'0')
	}
	return d, true
}

// NewDailyCounts builds DailyCounts from YYYYMMDD -> count buckets (the shape
// CountField returns for a date field). Keys that are not a YYYYMMDD day are
// dropped.
func NewDailyCounts(buckets map[string]int) DailyCounts {
	b := make([]dayCount, 0, len(buckets))
	for k, n := range buckets {
		if d, ok := parseDay(k); ok {
			b = append(b, dayCount{day: d, n: int32(min(n, math.MaxInt32))})
		}
	}
	sort.Slice(b, func(i, j int) bool { return b[i].day < b[j].day })
	return DailyCounts{buckets: b}
}

// Len is the number of days that have at least one report.
func (d DailyCounts) Len() int { return len(d.buckets) }

// Total is the number of reports over every day.
func (d DailyCounts) Total() int {
	t := 0
	for _, b := range d.buckets {
		t += int(b.n)
	}
	return t
}

// Sum is the number of reports with from <= day <= to, both ends included, as
// the openFDA range date_received:[from TO to] counts them. from and to are
// YYYYMMDD; anything else, or from after to, sums to 0.
func (d DailyCounts) Sum(from, to string) int {
	f, okF := parseDay(from)
	t, okT := parseDay(to)
	if !okF || !okT || f > t {
		return 0
	}
	lo := sort.Search(len(d.buckets), func(i int) bool { return d.buckets[i].day >= f })
	hi := sort.Search(len(d.buckets), func(i int) bool { return d.buckets[i].day > t })
	sum := 0
	for _, b := range d.buckets[lo:hi] {
		sum += int(b.n)
	}
	return sum
}

const (
	// dailyCountsTTL is how long a device's daily counts are reused. The same
	// 5 minutes as a finished dossier (cli synthCacheTTL), so the trend and the
	// dossier agree for the life of one dossier.
	dailyCountsTTL = 5 * time.Minute
	// dailyCountsRunLimit bounds the detached lookup so a wedged upstream cannot
	// pin an entry, and its goroutine, open forever.
	dailyCountsRunLimit = 90 * time.Second
	// dailyCountsMaxEntries bounds how many devices are held at once.
	dailyCountsMaxEntries = 32
)

// dailyCache runs at most one daily-count lookup per device at a time and keeps
// a successful answer for dailyCountsTTL. Callers that arrive while a lookup is
// in flight join it. The zero value is ready to use.
type dailyCache struct {
	mu      sync.Mutex
	entries map[string]*dailyEntry
	now     func() time.Time // nil = time.Now; tests replace it
}

// dailyEntry is one device's lookup: in flight until done is closed, then
// holding the result. val, err and readyAt are written once, under the cache
// mutex, before done is closed.
type dailyEntry struct {
	done    chan struct{}
	val     DailyCounts
	err     error
	readyAt time.Time
}

func (c *dailyCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// finishedLocked reports whether e's lookup has ended.
func finishedLocked(e *dailyEntry) bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// sweepLocked drops every finished entry that failed or has passed its TTL, so
// a failure is never served and an old answer cannot linger.
func (c *dailyCache) sweepLocked() {
	now := c.clock()
	for k, e := range c.entries {
		if finishedLocked(e) && (e.err != nil || now.Sub(e.readyAt) >= dailyCountsTTL) {
			delete(c.entries, k)
		}
	}
}

// makeRoomLocked evicts the oldest finished entries until one more fits.
// Lookups still in flight are never evicted.
func (c *dailyCache) makeRoomLocked() {
	for len(c.entries) >= dailyCountsMaxEntries {
		oldest, found := "", false
		var at time.Time
		for k, e := range c.entries {
			if finishedLocked(e) && (!found || e.readyAt.Before(at)) {
				oldest, at, found = k, e.readyAt, true
			}
		}
		if !found {
			return
		}
		delete(c.entries, oldest)
	}
}

// do returns the cached answer for key, joins a lookup in flight, or starts one.
//
// The lookup is detached from the first caller's context: if that browser tab
// goes away, the callers still waiting must not inherit the cancellation. A
// timeout puts a ceiling back on. Only the waiting is tied to ctx.
func (c *dailyCache) do(ctx context.Context, key string, fetch func(context.Context) (DailyCounts, error)) (DailyCounts, error) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*dailyEntry)
	}
	c.sweepLocked()
	e := c.entries[key]
	if e == nil {
		c.makeRoomLocked()
		e = &dailyEntry{done: make(chan struct{})}
		c.entries[key] = e
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dailyCountsRunLimit)
		go func() {
			defer cancel()
			v, err := fetch(runCtx)
			c.mu.Lock()
			e.val, e.err, e.readyAt = v, err, c.clock()
			c.mu.Unlock()
			close(e.done)
		}()
	}
	c.mu.Unlock()

	select {
	case <-e.done:
		return e.val, e.err
	case <-ctx.Done():
		// This caller gave up; the lookup continues for whoever else waits.
		return DailyCounts{}, ctx.Err()
	}
}

// DailyCounts returns the device's report count per received day over the whole
// history, from ONE count=date_received request: the device clause only, no date
// range and no limit (the count is not capped at 1,000 buckets; measured live
// 2026-10-03: 5,853 to 9,919 buckets, ~57 bytes each). openFDA 404 ("no
// matches") is an empty result with a nil error, as CountField returns it.
//
// Answers are shared: concurrent callers for the same device make one request,
// and a success is reused for dailyCountsTTL. A failure, 429 included, is never
// kept, so the next caller asks again. Implements DailyCounter.
func (s *OpenFDADeviceEvent) DailyCounts(ctx context.Context, term string) (DailyCounts, error) {
	// Normalised the way Phrase writes the term into the query.
	key := strings.ReplaceAll(strings.TrimSpace(term), `"`, "")
	if key == "" {
		return DailyCounts{}, fmt.Errorf("daily counts require a device term")
	}
	return s.daily.do(ctx, key, func(ctx context.Context) (DailyCounts, error) {
		buckets, err := s.CountField(ctx, Query{Term: key}, "date_received")
		if err != nil {
			return DailyCounts{}, err
		}
		dc := NewDailyCounts(buckets)
		if len(buckets) > 0 && dc.Len() == 0 {
			// Answered, but nothing in the shape of a day: not "no events".
			return DailyCounts{}, fmt.Errorf("daily counts: no dated bucket in the response")
		}
		return dc, nil
	})
}

// TotalMissingField returns the server-side total of a device's reports where
// a field is absent (openFDA _missing_ filter, verified live 2026-07-09:
// _missing_:date_of_event → 110259 for pacemaker).
func (s *OpenFDADeviceEvent) TotalMissingField(ctx context.Context, term, field string) (int, error) {
	if term == "" || field == "" {
		return 0, fmt.Errorf("missing-field total requires a term and a field")
	}
	params := paramsForSearch(Query{Limit: 1})
	params.Set("search", cliutil.And(nameClause(term), "_missing_:"+field))
	body, _, err := s.client.GetJSON(ctx, "/device/event.json", params)
	if err != nil {
		if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
			return 0, nil
		}
		return 0, err
	}
	var env openFDAEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return 0, err
	}
	return env.Meta.Results.Total, nil
}

func (s *OpenFDADeviceEvent) Health(ctx context.Context) error {
	_, _, err := s.client.GetJSON(ctx, "/device/event.json", paramsForSearch(Query{Limit: 1}))
	if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
		return nil
	}
	return err
}

func init() { Register(NewOpenFDADeviceEvent()) }
