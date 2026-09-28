package intelligence

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// decadeKeys mirrors decadeWindows' date arithmetic (Module 05 lesson: mocks
// must compute keys with the implementation's own math, never by hand).
func decadeKeys() []string {
	now := timeNow().UTC()
	keys := make([]string, lifecycleWindows)
	for i := 0; i < lifecycleWindows; i++ {
		to := now.AddDate(0, 0, -(lifecycleWindows-1-i)*730)
		from := to.AddDate(0, 0, -730+1)
		if i < lifecycleWindows-1 {
			to = to.AddDate(0, 0, -1)
			from = from.AddDate(0, 0, -1)
		}
		keys[i] = from.Format(day) + "-" + to.Format(day)
	}
	return keys
}

func decadeMock(counts ...int) mockData {
	keys := decadeKeys()
	w := map[string]int{}
	for i, n := range counts {
		w[keys[i]] = n
	}
	return mockData{windows: w}
}

func TestRecordNoveltyEstablished(t *testing.T) {
	pinClock(t)
	a := NewLifecycleAnalyzer(decadeMock(50, 60, 70, 80, 90))
	sig, err := a.AnalyzeRecordNovelty(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0 || sig.Label != LabelLow {
		t.Errorf("decade-long record: got %v/%q want 0/Low", sig.Value, sig.Label)
	}
	if !strings.Contains(sig.Reasoning, "~10 years") {
		t.Errorf("reasoning: %s", sig.Reasoning)
	}
}

func TestRecordNoveltyNewDevice(t *testing.T) {
	pinClock(t)
	a := NewLifecycleAnalyzer(decadeMock(0, 0, 0, 0, 25))
	sig, err := a.AnalyzeRecordNovelty(context.Background(), "newdevice")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0.8 || sig.Label != LabelCritical {
		t.Errorf("brand-new record: got %v/%q want 0.8/Critical", sig.Value, sig.Label)
	}
	if !strings.Contains(sig.Reasoning, "not evidence of safety") {
		t.Errorf("young record must carry the thin-history caveat: %s", sig.Reasoning)
	}
}

func TestLifecyclePhaseDeclining(t *testing.T) {
	pinClock(t)
	// Peak 1000 mid-decade, newest 100 (<0.6*peak) → declining.
	a := NewLifecycleAnalyzer(decadeMock(500, 1000, 800, 400, 100))
	sig, err := a.AnalyzeLifecyclePhase(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sig.Reasoning, "phase: declining") {
		t.Errorf("want declining: %s", sig.Reasoning)
	}
	// Instability |100-400|/400 = 0.75.
	if sig.Value != 0.75 {
		t.Errorf("instability=%v want 0.75", sig.Value)
	}
	if !strings.Contains(sig.Reasoning, "reporting lag") {
		t.Error("newest-window caveat missing")
	}
}

func TestLifecyclePhaseGrowing(t *testing.T) {
	pinClock(t)
	a := NewLifecycleAnalyzer(decadeMock(10, 20, 40, 100, 200))
	sig, err := a.AnalyzeLifecyclePhase(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sig.Reasoning, "phase: growing") {
		t.Errorf("want growing: %s", sig.Reasoning)
	}
	if sig.Value != 1.0 { // |200-100|/100 = 1.0
		t.Errorf("instability=%v want 1.0", sig.Value)
	}
}

func TestRecallRecency(t *testing.T) {
	pinClock(t) // now = 2026-07-09
	a := NewLifecycleAnalyzer(mockData{recallActions: []ComplianceAction{
		act("20190117", "Class I", "Z-OLD"),
		act("20260331", "Class II", "Z-NEW"), // 100 days before the pinned now
	}})
	sig, err := a.AnalyzeRecallRecency(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	// The pinned clock is 12:00 UTC, so the gap is 100.5 days:
	// 1 - 100.5/1825 = 0.9449 → 0.94.
	if sig.Value != 0.94 || sig.Label != LabelTop {
		t.Errorf("got %v/%q want 0.94/Top", sig.Value, sig.Label)
	}
	for _, want := range []string{"20260331", "100 days ago", "not a harm reading"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
	if sig.ConfidenceLevel != ConfidenceMedium { // 2 recalls < 5
		t.Errorf("confidence=%q want MEDIUM", sig.ConfidenceLevel)
	}
}

func TestLifecycleNoDataIsGracefulUnknown(t *testing.T) {
	pinClock(t)
	a := NewLifecycleAnalyzer(mockData{})
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"novelty": func() (*Signal, error) { return a.AnalyzeRecordNovelty(ctx, "zzz") },
		"phase":   func() (*Signal, error) { return a.AnalyzeLifecyclePhase(ctx, "zzz") },
		"recency": func() (*Signal, error) { return a.AnalyzeRecallRecency(ctx, "zzz") },
	} {
		sig, err := f()
		if err != nil {
			t.Fatalf("%s: no data must not error: %v", name, err)
		}
		if sig.Label != LabelUnknown || sig.Value != 0 {
			t.Errorf("%s no-data: %+v want Unknown/0", name, sig)
		}
	}
}

// TestLiveLifecycle grounds Module 08 against the real APIs (MDI_LIVE=1).
func TestLiveLifecycle(t *testing.T) {
	if os.Getenv("MDI_LIVE") == "" {
		t.Skip("set MDI_LIVE=1 to run live grounding")
	}
	a := NewLifecycleAnalyzer(NewLiveData())
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"novelty": func() (*Signal, error) { return a.AnalyzeRecordNovelty(ctx, "pacemaker") },
		"phase":   func() (*Signal, error) { return a.AnalyzeLifecyclePhase(ctx, "pacemaker") },
		"recency": func() (*Signal, error) { return a.AnalyzeRecallRecency(ctx, "pacemaker") },
	} {
		sig, err := f()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sig.Value < 0 || sig.Value > 1 {
			t.Errorf("%s: value %v out of [0,1]", name, sig.Value)
		}
		b, _ := json.MarshalIndent(sig, "", "  ")
		t.Logf("%s:\n%s", name, b)
	}
}
