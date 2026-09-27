package intelligence

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// windowRecorder wraps mockData and records every (from, to) window requested.
type windowRecorder struct {
	mockData
	mu      sync.Mutex
	windows [][2]string
}

func (r *windowRecorder) record(from, to string) {
	r.mu.Lock()
	r.windows = append(r.windows, [2]string{from, to})
	r.mu.Unlock()
}
func (r *windowRecorder) EventTotalWindow(_ context.Context, _ string, from, to string) (int, error) {
	r.record(from, to)
	return 5, nil
}
func (r *windowRecorder) EventTypeCountsWindow(_ context.Context, _ string, from, to string) (map[string]int, error) {
	r.record(from, to)
	return map[string]int{"Malfunction": 1}, nil
}
func (r *windowRecorder) FirmRecallTotalWindow(_ context.Context, _ string, from, to string) (int, error) {
	r.record(from, to)
	return 5, nil
}

func fixClock(t *testing.T) time.Time {
	t.Helper()
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	old := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = old })
	return now
}

func parseDay(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("20060102", s)
	if err != nil {
		t.Fatalf("bad day %q: %v", s, err)
	}
	return d
}

// inclusiveDays counts the days in the inclusive range [from, to].
func inclusiveDays(t *testing.T, w [2]string) int {
	return int(parseDay(t, w[1]).Sub(parseDay(t, w[0])).Hours()/24) + 1
}

// assertRecentPrior checks recent = w[0] ending today with recentDays days,
// prior = w[1] with priorDays days ending the day before recent starts.
func assertRecentPrior(t *testing.T, ws [][2]string, now time.Time, recentDays, priorDays int) {
	t.Helper()
	if len(ws) < 2 {
		t.Fatalf("got %d windows, want >= 2", len(ws))
	}
	recent, prior := ws[0], ws[1]
	if recent[1] != now.Format("20060102") {
		t.Errorf("recent window ends %s, want today %s", recent[1], now.Format("20060102"))
	}
	if n := inclusiveDays(t, recent); n != recentDays {
		t.Errorf("recent window %v covers %d days, want %d", recent, n, recentDays)
	}
	if n := inclusiveDays(t, prior); n != priorDays {
		t.Errorf("prior window %v covers %d days, want %d", prior, n, priorDays)
	}
	if !parseDay(t, prior[1]).AddDate(0, 0, 1).Equal(parseDay(t, recent[0])) {
		t.Errorf("windows not contiguous: prior ends %s, recent starts %s", prior[1], recent[0])
	}
}

func TestBUG01_TrendWindowsExactDays(t *testing.T) {
	now := fixClock(t)
	r := &windowRecorder{}
	if _, err := (&TelemetryAnalyzer{data: r}).AnalyzeTrend(context.Background(), "x", 30); err != nil {
		t.Fatal(err)
	}
	assertRecentPrior(t, r.windows, now, 30, 30)
}

func TestBUG01_SurgeWindowsExactDays(t *testing.T) {
	now := fixClock(t)
	r := &windowRecorder{}
	if _, err := (&AnomalyAnalyzer{data: r}).DetectSurge(context.Background(), "x", 30, 180); err != nil {
		t.Fatal(err)
	}
	assertRecentPrior(t, r.windows, now, 30, 180)
}

func TestBUG01_NewPatternWindowExactDays(t *testing.T) {
	now := fixClock(t)
	r := &windowRecorder{}
	if _, err := (&AnomalyAnalyzer{data: r}).DetectNewPattern(context.Background(), "x", 30); err != nil {
		t.Fatal(err)
	}
	recent, history := r.windows[0], r.windows[1]
	if n := inclusiveDays(t, recent); n != 30 || recent[1] != now.Format("20060102") {
		t.Errorf("recent window %v covers %d days, want 30 ending today", recent, n)
	}
	if !parseDay(t, history[1]).AddDate(0, 0, 1).Equal(parseDay(t, recent[0])) {
		t.Errorf("history ends %s, recent starts %s: not contiguous", history[1], recent[0])
	}
}

func TestBUG01_FirmRecallTrendWindowsExactDays(t *testing.T) {
	now := fixClock(t)
	r := &windowRecorder{}
	if _, err := (&ManufacturerAnalyzer{data: r}).AnalyzeRecallTrend(context.Background(), "f", 365); err != nil {
		t.Fatal(err)
	}
	assertRecentPrior(t, r.windows, now, 365, 365)
}

func TestBUG01_ClusterTrendWindowsExactDays(t *testing.T) {
	now := fixClock(t)
	r := &windowRecorder{mockData: mockData{
		trials:      10,
		meshTerms:   []string{"Heart Failure"},
		condDevices: map[string][]string{"Heart Failure": {"pump b", "pump c"}},
	}}
	sig, err := (&ClusterAnalyzer{data: r}).AnalyzeClusterRisk(context.Background(), "pump a")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.windows) < 2 {
		t.Fatalf("no windows recorded (signal: %+v)", sig)
	}
	for i := 0; i+1 < len(r.windows); i += 2 {
		assertRecentPrior(t, r.windows[i:i+2], now, 365, 365)
	}
}

func TestBUG02_VolumeIgnoresRecalls(t *testing.T) {
	base := mockData{eventTypes: map[string]int{"Malfunction": 100}, p95: 1000, sample: 100}
	a := base
	a.recalls = 0
	b := base
	b.recalls = 500
	sa, err := (&TelemetryAnalyzer{data: a}).AnalyzeVolume(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	sb, err := (&TelemetryAnalyzer{data: b}).AnalyzeVolume(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if sa.Value != sb.Value {
		t.Errorf("volume index changed with recalls: %v (R=0) vs %v (R=500)", sa.Value, sb.Value)
	}
	if sa.Value != 0.1 {
		t.Errorf("value=%v want 0.1 (100 events / p95 1000)", sa.Value)
	}
}

func TestBUG03_NotYetRecruitingIsNotActive(t *testing.T) {
	all := []string{"RECRUITING", "ACTIVE_NOT_RECRUITING", "ENROLLING_BY_INVITATION",
		"NOT_YET_RECRUITING", "COMPLETED", "TERMINATED"}
	r := &statusCounter{mockData: mockData{trials: len(all)}, studies: all}
	sig, err := (&ResearchAnalyzer{data: r}).AnalyzeActiveResearch(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if want := round2(3.0 / 6.0); sig.Value != want {
		t.Errorf("value=%v want %v (3 active of 6); reasoning: %s", sig.Value, want, sig.Reasoning)
	}
	if !strings.Contains(sig.Reasoning, "3 of 6") {
		t.Errorf("reasoning %q should report 3 of 6 active", sig.Reasoning)
	}
}

// statusCounter answers TrialStatusTotal from one study per listed status.
type statusCounter struct {
	mockData
	studies []string
}

func (s *statusCounter) TrialStatusTotal(_ context.Context, _ string, statuses []string) (int, error) {
	n := 0
	for _, st := range s.studies {
		for _, want := range statuses {
			if st == want {
				n++
			}
		}
	}
	return n, nil
}

func (r *windowRecorder) ProblemCountsWindow(_ context.Context, _ string, from, to string) (map[string]int, error) {
	r.record(from, to)
	return map[string]int{"Over-Sensing": 1}, nil
}

func TestBUG01_NewProblemModesWindowExactDays(t *testing.T) {
	now := fixClock(t)
	r := &windowRecorder{}
	if _, err := (&FailureModeAnalyzer{data: r}).AnalyzeNewProblemModes(context.Background(), "x", 90); err != nil {
		t.Fatal(err)
	}
	if len(r.windows) < 2 {
		t.Fatalf("got %d windows, want 2", len(r.windows))
	}
	recent, history := r.windows[0], r.windows[1]
	if n := inclusiveDays(t, recent); n != 90 || recent[1] != now.Format("20060102") {
		t.Errorf("recent window %v covers %d days, want 90 ending today", recent, n)
	}
	if !parseDay(t, history[1]).AddDate(0, 0, 1).Equal(parseDay(t, recent[0])) {
		t.Errorf("history ends %s, recent starts %s: not contiguous", history[1], recent[0])
	}
}

// DESIGN-03: CRITICAL is a Devicera rule over FDA recall records. The JSON
// value stays "CRITICAL" (API contract) but the reasoning must say it is not
// an FDA status.
func TestComplianceCriticalIsLabelledDeviceraRule(t *testing.T) {
	a := NewComplianceAnalyzer(mockData{
		recallClasses: map[string]int{"Class I": 4},
		recallActions: []ComplianceAction{act("20250301", "Class I", "Z-9")},
	})
	st, err := a.CheckFDAStatus(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"status":"CRITICAL"`) {
		t.Errorf("JSON status changed: %s", raw)
	}
	for _, want := range []string{"Devicera rule", "not an FDA status", "4 Class I"} {
		if !strings.Contains(st.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, st.Reasoning)
		}
	}
}
