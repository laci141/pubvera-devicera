package intelligence

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// mockData is a canned read-only Data view.
type mockData struct {
	eventTypes     map[string]int
	recalls        int
	windows        map[string]int            // keyed by "from-to"
	typeWindows    map[string]map[string]int // keyed by "from-to"
	p95            int
	sample         int
	recallClasses  map[string]int
	trials         int
	pubs           int
	recallActions  []ComplianceAction
	firmClasses    map[string]int
	firmStatuses   map[string]int
	firmWindows    map[string]int // keyed by "from-to"
	typeVolumes    map[string]int
	globalTypes    map[string]int
	globalClasses  map[string]int
	meshTerms      []string
	condDevices    map[string][]string       // condition -> intervention names
	deviceWindows  map[string]map[string]int // device -> "from-to" -> total
	problems       map[string]int
	problemWindows map[string]map[string]int // "from-to" -> problem counts
	pubWindows     map[string]int            // "fromYear-toYear" -> count
	trialStatuses  map[string]int            // joined status list -> total
	reporterTypes  map[string]int
	missingTotals  map[string]int // field -> total missing
	makerCounts    map[string]int
}

func (m mockData) EventTypeCounts(context.Context, string) (map[string]int, error) {
	return m.eventTypes, nil
}
func (m mockData) RecallTotal(context.Context, string) (int, error) { return m.recalls, nil }
func (m mockData) EventTotalWindow(_ context.Context, device string, from, to string) (int, error) {
	if dw, ok := m.deviceWindows[device]; ok {
		return dw[from+"-"+to], nil
	}
	return m.windows[from+"-"+to], nil
}
func (m mockData) VolumeBaseline(context.Context) (int, int, error) { return m.p95, m.sample, nil }
func (m mockData) EventTypeCountsWindow(_ context.Context, _ string, from, to string) (map[string]int, error) {
	return m.typeWindows[from+"-"+to], nil
}
func (m mockData) RecallClassCounts(context.Context, string) (map[string]int, error) {
	return m.recallClasses, nil
}
func (m mockData) TrialTotal(context.Context, string) (int, error)       { return m.trials, nil }
func (m mockData) PublicationTotal(context.Context, string) (int, error) { return m.pubs, nil }
func (m mockData) RecallActions(context.Context, string, int) ([]ComplianceAction, error) {
	return m.recallActions, nil
}
func (m mockData) FirmRecallClassCounts(context.Context, string) (map[string]int, error) {
	return m.firmClasses, nil
}
func (m mockData) FirmRecallStatusCounts(context.Context, string) (map[string]int, error) {
	return m.firmStatuses, nil
}
func (m mockData) FirmRecallTotalWindow(_ context.Context, _ string, from, to string) (int, error) {
	return m.firmWindows[from+"-"+to], nil
}
func (m mockData) DeviceTypeVolumes(context.Context) (map[string]int, error) {
	return m.typeVolumes, nil
}
func (m mockData) GlobalEventTypeCounts(context.Context) (map[string]int, error) {
	return m.globalTypes, nil
}
func (m mockData) GlobalRecallClassCounts(context.Context) (map[string]int, error) {
	return m.globalClasses, nil
}
func (m mockData) DeviceMeshTerms(context.Context, string, int) ([]string, error) {
	return m.meshTerms, nil
}
func (m mockData) DevicesForCondition(_ context.Context, term string, _ int) ([]string, error) {
	return m.condDevices[term], nil
}
func (m mockData) ProblemCounts(context.Context, string) (map[string]int, error) {
	return m.problems, nil
}
func (m mockData) ProblemCountsWindow(_ context.Context, _ string, from, to string) (map[string]int, error) {
	return m.problemWindows[from+"-"+to], nil
}
func (m mockData) PublicationCountWindow(_ context.Context, _ string, fromYear, toYear int) (int, error) {
	return m.pubWindows[fmt.Sprintf("%d-%d", fromYear, toYear)], nil
}
func (m mockData) TrialStatusTotal(_ context.Context, _ string, statuses []string) (int, error) {
	return m.trialStatuses[strings.Join(statuses, "|")], nil
}
func (m mockData) ReporterSourceCounts(context.Context, string) (map[string]int, error) {
	return m.reporterTypes, nil
}
func (m mockData) EventTotalMissing(_ context.Context, _ string, field string) (int, error) {
	return m.missingTotals[field], nil
}
func (m mockData) ManufacturerNameCounts(context.Context, string) (map[string]int, error) {
	return m.makerCounts, nil
}

func TestAnalyzeSeverity(t *testing.T) {
	a := NewTelemetryAnalyzer(mockData{
		eventTypes: map[string]int{"Death": 3, "Injury": 5, "Malfunction": 10},
	})
	sig, err := a.AnalyzeSeverity(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	// (3*1.0 + 5*0.6 + 10*0.3) / 18 = 9/18 = 0.5 exactly.
	if sig.Value != 0.5 {
		t.Errorf("value=%v want 0.5", sig.Value)
	}
	if sig.Label != LabelMedium {
		t.Errorf("label=%q want Medium (0.5 is not >0.5)", sig.Label)
	}
	for _, want := range []string{"3 deaths", "5 injuries", "10 malfunctions", "death=1.0"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
	if sig.ConfidenceLevel != ConfidenceLow { // 18 reports < 50
		t.Errorf("confidence=%q want LOW for 18 reports", sig.ConfidenceLevel)
	}
	if len(sig.SourceType) == 0 || sig.SourceType[0] != "openfda_maude" {
		t.Errorf("source types wrong: %v", sig.SourceType)
	}
}

func TestAnalyzeSeverityAllDeathsIsCritical(t *testing.T) {
	a := NewTelemetryAnalyzer(mockData{eventTypes: map[string]int{"Death": 300}})
	sig, err := a.AnalyzeSeverity(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 1.0 || sig.Label != LabelCritical || sig.ConfidenceLevel != ConfidenceHigh {
		t.Errorf("got value=%v label=%q conf=%q want 1.0/Critical/HIGH", sig.Value, sig.Label, sig.ConfidenceLevel)
	}
}

func TestAnalyzeVolume(t *testing.T) {
	a := NewTelemetryAnalyzer(mockData{
		eventTypes: map[string]int{"Malfunction": 200},
		recalls:    0,
		p95:        300,
		sample:     50,
	})
	sig, err := a.AnalyzeVolume(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0.67 { // 200/300 rounded
		t.Errorf("value=%v want 0.67", sig.Value)
	}
	if sig.Label != LabelHigh {
		t.Errorf("label=%q want High", sig.Label)
	}
	if sig.ConfidenceLevel != ConfidenceMedium { // baseline sample 50 < 100
		t.Errorf("confidence=%q want MEDIUM for 50-type baseline", sig.ConfidenceLevel)
	}
	for _, want := range []string{"200 MAUDE events", "0 recalls", "p95 volume 300", "50 most-reported"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
	if sig.Value > 1.0 {
		t.Error("value must be capped at 1.0")
	}
}

func TestAnalyzeVolumeCapsAtOne(t *testing.T) {
	a := NewTelemetryAnalyzer(mockData{
		eventTypes: map[string]int{"Malfunction": 900}, recalls: 100, p95: 300, sample: 100,
	})
	sig, err := a.AnalyzeVolume(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 1.0 || sig.Label != LabelCritical {
		t.Errorf("got %v/%q want 1.0/Critical (capped)", sig.Value, sig.Label)
	}
}

func TestAnalyzeTrendSurge(t *testing.T) {
	old := timeNow
	timeNow = func() time.Time { return time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { timeNow = old })

	a := NewTelemetryAnalyzer(mockData{windows: map[string]int{
		"20260609-20260709": 50, // recent 30 days
		"20260510-20260608": 10, // prior 30 days
	}})
	sig, err := a.AnalyzeTrend(context.Background(), "pacemaker", 30)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 1.0 { // slope (50-10)/10 = 4.0, capped at 1.0
		t.Errorf("value=%v want 1.0 (capped surge)", sig.Value)
	}
	if sig.Label != LabelCritical {
		t.Errorf("label=%q want Critical", sig.Label)
	}
	for _, want := range []string{"increasing", "50 reports", "+400%", "reporting lag"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
	if sig.ConfidenceLevel != ConfidenceMedium { // 60 total reports
		t.Errorf("confidence=%q want MEDIUM", sig.ConfidenceLevel)
	}
}

func TestAnalyzeTrendDeclineReadsLow(t *testing.T) {
	old := timeNow
	timeNow = func() time.Time { return time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { timeNow = old })

	a := NewTelemetryAnalyzer(mockData{windows: map[string]int{
		"20260609-20260709": 5,
		"20260510-20260608": 10,
	}})
	sig, err := a.AnalyzeTrend(context.Background(), "x", 30)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0 || sig.Label != LabelLow {
		t.Errorf("decline: got %v/%q want 0/Low", sig.Value, sig.Label)
	}
	if !strings.Contains(sig.Reasoning, "declining") {
		t.Errorf("reasoning should state the direction: %s", sig.Reasoning)
	}
}

func TestAnalyzeTrendNewSignalNoBaseline(t *testing.T) {
	old := timeNow
	timeNow = func() time.Time { return time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { timeNow = old })

	a := NewTelemetryAnalyzer(mockData{windows: map[string]int{"20260609-20260709": 7}})
	sig, err := a.AnalyzeTrend(context.Background(), "x", 30)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 1.0 || sig.ConfidenceLevel != ConfidenceLow {
		t.Errorf("new signal: got %v/%q want 1.0/LOW", sig.Value, sig.ConfidenceLevel)
	}
	if !strings.Contains(sig.Reasoning, "no baseline") {
		t.Errorf("reasoning should state the missing baseline: %s", sig.Reasoning)
	}
}

func TestNoDataIsGracefulUnknown(t *testing.T) {
	a := NewTelemetryAnalyzer(mockData{}) // empty everything
	ctx := context.Background()

	sev, err := a.AnalyzeSeverity(ctx, "zzznope")
	if err != nil {
		t.Fatalf("no data must not error: %v", err)
	}
	if sev.Label != LabelUnknown || sev.Value != 0 || sev.ConfidenceLevel != ConfidenceLow {
		t.Errorf("severity no-data: %+v want Unknown/0/LOW", sev)
	}
	if !strings.Contains(sev.Reasoning, "not evidence of safety") {
		t.Error("no-data reasoning must never imply safety")
	}

	vol, err := a.AnalyzeVolume(ctx, "zzznope")
	if err != nil || vol.Label != LabelUnknown {
		t.Errorf("volume no-data: %+v err=%v want Unknown", vol, err)
	}
	trend, err := a.AnalyzeTrend(ctx, "zzznope", 30)
	if err != nil || trend.Label != LabelUnknown {
		t.Errorf("trend no-data: %+v err=%v want Unknown", trend, err)
	}

	if _, err := a.AnalyzeTrend(ctx, "x", 0); err == nil {
		t.Error("recentDays < 1 must error explicitly")
	}
}

// TestLiveTelemetry grounds the module against the real APIs. Gated behind
// MDI_LIVE=1 so the normal suite stays hermetic.
func TestLiveTelemetry(t *testing.T) {
	if os.Getenv("MDI_LIVE") == "" {
		t.Skip("set MDI_LIVE=1 to run live grounding")
	}
	a := NewTelemetryAnalyzer(NewLiveData())
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"severity": func() (*Signal, error) { return a.AnalyzeSeverity(ctx, "pacemaker") },
		"volume":   func() (*Signal, error) { return a.AnalyzeVolume(ctx, "pacemaker") },
		"trend":    func() (*Signal, error) { return a.AnalyzeTrend(ctx, "pacemaker", 90) },
	} {
		sig, err := f()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sig.Value < 0 || sig.Value > 1 {
			t.Errorf("%s: value %v out of [0,1]", name, sig.Value)
		}
		if sig.Reasoning == "" || sig.ConfidenceLevel == "" || len(sig.SourceType) == 0 {
			t.Errorf("%s: incomplete signal %+v", name, sig)
		}
		b, _ := json.MarshalIndent(sig, "", "  ")
		t.Logf("%s:\n%s", name, b)
	}
}
