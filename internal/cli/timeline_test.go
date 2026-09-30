package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/laci141/medical-device-intelligence/internal/sources"
)

// recordingRecalls is an enforcement source that records every query it gets.
type recordingRecalls struct {
	fakeSource
	got *[]sources.Query
}

func (f recordingRecalls) Fetch(ctx context.Context, q sources.Query) ([]sources.RawRecord, sources.Page, error) {
	*f.got = append(*f.got, q)
	return f.fakeSource.Fetch(ctx, q)
}

// recordingEventCounter is a MAUDE source that answers count queries from
// daily buckets and records both counts and (unwanted) record fetches.
type recordingEventCounter struct {
	buckets map[string]int
	err     error
	fields  *[]string
	queries *[]sources.Query
	fetches *int
}

func (f recordingEventCounter) Name() string    { return "openfda_device_event" }
func (f recordingEventCounter) IDField() string { return "mdr_report_key" }
func (f recordingEventCounter) Fetch(context.Context, sources.Query) ([]sources.RawRecord, sources.Page, error) {
	*f.fetches++
	return nil, sources.Page{}, nil
}
func (f recordingEventCounter) Health(context.Context) error { return nil }
func (f recordingEventCounter) CountField(_ context.Context, q sources.Query, field string) (map[string]int, error) {
	*f.fields = append(*f.fields, field)
	*f.queries = append(*f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	return f.buckets, nil
}

type timelineFakes struct {
	recallQs []sources.Query
	fields   []string
	countQs  []sources.Query
	fetches  int
}

// withTimelineSources installs a recall source and a counting MAUDE source.
func withTimelineSources(t *testing.T, recalls []sources.RawRecord, recallErr error, buckets map[string]int, countErr error) *timelineFakes {
	t.Helper()
	tf := &timelineFakes{}
	withSources(t, map[string]sources.Source{
		"openfda_device_enforcement": recordingRecalls{
			fakeSource: fakeSource{name: "openfda_device_enforcement", id: "recall_number", recs: recalls, err: recallErr},
			got:        &tf.recallQs,
		},
		"openfda_device_event": recordingEventCounter{
			buckets: buckets, err: countErr,
			fields: &tf.fields, queries: &tf.countQs, fetches: &tf.fetches,
		},
	})
	return tf
}

func recallRec(id, date string) sources.RawRecord {
	return sources.RawRecord{ID: id, Raw: map[string]any{
		"recall_initiation_date": date, "classification": "Class II", "product_description": "pacemaker " + id,
	}}
}

// threeMonths: daily buckets across Jan–Mar 2026 → 2026-01:5, 2026-02:4, 2026-03:6.
func threeMonths() map[string]int {
	return map[string]int{"20260105": 2, "20260120": 3, "20260210": 4, "20260301": 5, "20260331": 1}
}

func timelineJSON(t *testing.T, args ...string) ([]map[string]any, string) {
	t.Helper()
	out, errOut, code := run(cmdTimeline, append([]string{"--json"}, args...)...)
	if code != 0 {
		t.Fatalf("exit=%d want 0; stderr=%s", code, errOut)
	}
	var env struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	return env.Records, errOut
}

// rowLabel names a row for order checks: the month for month rows, the recall id otherwise.
func rowLabel(r map[string]any) string {
	if r["kind"] == "events_month" {
		return str(r["month"])
	}
	return str(r["source_id"])
}

// (a) Recalls are requested newest first (openFDA order is arbitrary without
// sort — Phase A: newest recall 59–529 days behind) with the default limit.
func TestTimelineRecallsRequestedNewestFirst(t *testing.T) {
	tf := withTimelineSources(t, nil, nil, threeMonths(), nil)
	timelineJSON(t, "pacemaker")
	if len(tf.recallQs) != 1 {
		t.Fatalf("recall requests=%d want 1", len(tf.recallQs))
	}
	q := tf.recallQs[0]
	if q.Sort != "recall_initiation_date:desc" {
		t.Errorf("recall Sort=%q want recall_initiation_date:desc", q.Sort)
	}
	if q.Limit != 25 || q.Term != "pacemaker" {
		t.Errorf("recall query=%+v want Term pacemaker, Limit 25", q)
	}
}

// (b) Events come from ONE count=date_received query over the full history,
// never from individual records (the newest 999 cover only 6 days).
func TestTimelineEventsAreOneCountQuery(t *testing.T) {
	tf := withTimelineSources(t, nil, nil, threeMonths(), nil)
	timelineJSON(t, "pacemaker")
	if tf.fetches != 0 {
		t.Errorf("event record fetches=%d want 0", tf.fetches)
	}
	if len(tf.fields) != 1 || tf.fields[0] != "date_received" {
		t.Fatalf("count fields=%v want [date_received]", tf.fields)
	}
	if q := tf.countQs[0]; q.Term != "pacemaker" || q.Limit != 0 || q.DateFrom != "" {
		t.Errorf("count query=%+v want Term pacemaker, no limit, no date bound", q)
	}
}

// (c) Daily buckets aggregate to one row per month; --months keeps the newest.
func TestTimelineMonthlyAggregation(t *testing.T) {
	withTimelineSources(t, nil, nil, threeMonths(), nil)
	rows, _ := timelineJSON(t, "pacemaker")
	want := []struct {
		month string
		count float64
	}{{"2026-03", 6}, {"2026-02", 4}, {"2026-01", 5}}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d: %v", len(rows), len(want), rows)
	}
	for i, w := range want {
		r := rows[i]
		if r["kind"] != "events_month" || r["month"] != w.month || r["count"] != w.count {
			t.Errorf("row %d = %v want events_month %s count %v", i, r, w.month, w.count)
		}
	}

	rows, _ = timelineJSON(t, "--months", "2", "pacemaker")
	if len(rows) != 2 || rows[0]["month"] != "2026-03" || rows[1]["month"] != "2026-02" {
		t.Errorf("--months 2 rows=%v want 2026-03, 2026-02", rows)
	}
}

// (d) Month rows are keyed to the month's LAST day, so each month row sits
// directly above that month's recalls; a recall on the last day ties and goes
// under its month. Only the newest month carries the release-lag note.
func TestTimelineMergedOrderAndLagNote(t *testing.T) {
	recalls := []sources.RawRecord{
		recallRec("R-JAN31", "20260131"), recallRec("R-MAR", "20260315"), recallRec("R-FEB", "20260201"),
	}
	withTimelineSources(t, recalls, nil, threeMonths(), nil)
	rows, _ := timelineJSON(t, "pacemaker")
	var got []string
	for _, r := range rows {
		got = append(got, rowLabel(r))
	}
	want := "2026-03 R-MAR 2026-02 R-FEB 2026-01 R-JAN31"
	if strings.Join(got, " ") != want {
		t.Fatalf("order=%v want %s", got, want)
	}
	if rows[0]["date"] != "20260331" || rows[2]["date"] != "20260228" {
		t.Errorf("month dates=%v,%v want last day 20260331,20260228", rows[0]["date"], rows[2]["date"])
	}
	if rows[1]["kind"] != "recall" {
		t.Errorf("recall kind=%v", rows[1]["kind"])
	}
	if n := str(rows[0]["note"]); !strings.Contains(n, "may be incomplete (MAUDE release lag)") {
		t.Errorf("newest month note=%q", n)
	}
	for _, i := range []int{2, 4} {
		if n := str(rows[i]["note"]); n != "" {
			t.Errorf("older month %v carries note %q", rows[i]["month"], n)
		}
	}
}

// (e) One failed half still prints the other half plus a one-line note.
func TestTimelinePartialFailure(t *testing.T) {
	withTimelineSources(t, []sources.RawRecord{recallRec("R-MAR", "20260315")}, nil, nil, errors.New("HTTP 500"))
	rows, errOut := timelineJSON(t, "pacemaker")
	if len(rows) != 1 || rows[0]["source_id"] != "R-MAR" {
		t.Errorf("rows=%v want the recall only", rows)
	}
	if !strings.Contains(errOut, "monthly event counts unavailable") {
		t.Errorf("stderr=%q want monthly-counts note", errOut)
	}

	withTimelineSources(t, nil, errors.New("HTTP 500"), threeMonths(), nil)
	rows, errOut = timelineJSON(t, "pacemaker")
	if len(rows) != 3 || rows[0]["kind"] != "events_month" {
		t.Errorf("rows=%v want 3 month rows", rows)
	}
	if !strings.Contains(errOut, "recalls unavailable") {
		t.Errorf("stderr=%q want recalls note", errOut)
	}
}

// (f) openFDA caps limit; above 999 is a usage error, 999 itself is fine.
func TestTimelineLimitCap(t *testing.T) {
	withTimelineSources(t, nil, nil, threeMonths(), nil)
	_, errOut, code := run(cmdTimeline, "--limit", "1000", "pacemaker")
	if code != 2 || !strings.Contains(errOut, "999") {
		t.Errorf("--limit 1000: exit=%d stderr=%q want 2 and a message naming 999", code, errOut)
	}
	if _, _, code := run(cmdTimeline, "--limit", "999", "pacemaker"); code != 0 {
		t.Errorf("--limit 999: exit=%d want 0", code)
	}
}
