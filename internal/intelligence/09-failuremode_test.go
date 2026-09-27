package intelligence

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestTopProblemsRankedWithShares(t *testing.T) {
	a := NewFailureModeAnalyzer(mockData{problems: map[string]int{
		"Over-Sensing": 60, "Under-Sensing": 30, "Battery Issue": 10,
	}})
	top, err := a.TopProblems(context.Background(), "pacemaker", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].Problem != "Over-Sensing" || top[1].Problem != "Under-Sensing" {
		t.Fatalf("ranking wrong: %+v", top)
	}
	if top[0].Share != 0.6 || top[0].Count != 60 {
		t.Errorf("share/count wrong: %+v", top[0])
	}
}

func TestProblemConcentrationSingleModeIsCritical(t *testing.T) {
	a := NewFailureModeAnalyzer(mockData{problems: map[string]int{"Battery Issue": 500}})
	sig, err := a.AnalyzeProblemConcentration(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 1.0 || sig.Label != LabelCritical {
		t.Errorf("single mode: got %v/%q want 1.0/Critical", sig.Value, sig.Label)
	}
	for _, want := range []string{"Herfindahl 1.00", "Battery Issue (100%)", "labeling pattern"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
}

func TestProblemConcentrationDiffuseIsLow(t *testing.T) {
	// Four equal modes → HHI = 4*(0.25²) = 0.25 → Low.
	a := NewFailureModeAnalyzer(mockData{problems: map[string]int{
		"A": 25, "B": 25, "C": 25, "D": 25,
	}})
	sig, err := a.AnalyzeProblemConcentration(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0.25 || sig.Label != LabelLow {
		t.Errorf("diffuse: got %v/%q want 0.25/Low", sig.Value, sig.Label)
	}
}

func TestNewProblemModes(t *testing.T) {
	pinClock(t)
	now := timeNow().UTC()
	mid := now.AddDate(0, 0, -90+1)
	recentKey := mid.Format(day) + "-" + now.Format(day)
	historyKey := "19900101-" + mid.AddDate(0, 0, -1).Format(day)

	a := NewFailureModeAnalyzer(mockData{problemWindows: map[string]map[string]int{
		recentKey:  {"Over-Sensing": 15, "Thermal Runaway": 5}, // 5 of 20 are a new mode
		historyKey: {"Over-Sensing": 900, "Under-Sensing": 100},
	}})
	sig, err := a.AnalyzeNewProblemModes(context.Background(), "pacemaker", 90)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0.25 {
		t.Errorf("value=%v want 0.25", sig.Value)
	}
	for _, want := range []string{"5 of 20", "Thermal Runaway", "rare old mode can look new"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}

	// No unseen modes → 0/Low.
	a = NewFailureModeAnalyzer(mockData{problemWindows: map[string]map[string]int{
		recentKey:  {"Over-Sensing": 20},
		historyKey: {"Over-Sensing": 900},
	}})
	sig, err = a.AnalyzeNewProblemModes(context.Background(), "pacemaker", 90)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0 || !strings.Contains(sig.Reasoning, "no new way of failing") {
		t.Errorf("no new modes: got %v %q", sig.Value, sig.Reasoning)
	}
}

func TestFailureModeNoDataIsGracefulUnknown(t *testing.T) {
	pinClock(t)
	a := NewFailureModeAnalyzer(mockData{})
	ctx := context.Background()

	top, err := a.TopProblems(ctx, "zzz", 5)
	if err != nil || top != nil {
		t.Errorf("no problems: got %v err=%v want nil/nil", top, err)
	}
	for name, f := range map[string]func() (*Signal, error){
		"concentration": func() (*Signal, error) { return a.AnalyzeProblemConcentration(ctx, "zzz") },
		"new-modes":     func() (*Signal, error) { return a.AnalyzeNewProblemModes(ctx, "zzz", 90) },
	} {
		sig, err := f()
		if err != nil {
			t.Fatalf("%s: no data must not error: %v", name, err)
		}
		if sig.Label != LabelUnknown || sig.Value != 0 {
			t.Errorf("%s no-data: %+v want Unknown/0", name, sig)
		}
	}
	if _, err := a.AnalyzeNewProblemModes(ctx, "x", 0); err == nil {
		t.Error("recentDays < 1 must error")
	}
}

// TestLiveFailureMode grounds Module 09 against the real API (MDI_LIVE=1).
func TestLiveFailureMode(t *testing.T) {
	if os.Getenv("MDI_LIVE") == "" {
		t.Skip("set MDI_LIVE=1 to run live grounding")
	}
	a := NewFailureModeAnalyzer(NewLiveData())
	ctx := context.Background()

	top, err := a.TopProblems(ctx, "pacemaker", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) == 0 {
		t.Fatal("pacemaker should have problem terms")
	}
	b, _ := json.MarshalIndent(top, "", "  ")
	t.Logf("top-problems:\n%s", b)

	for name, f := range map[string]func() (*Signal, error){
		"concentration": func() (*Signal, error) { return a.AnalyzeProblemConcentration(ctx, "pacemaker") },
		"new-modes":     func() (*Signal, error) { return a.AnalyzeNewProblemModes(ctx, "pacemaker", 180) },
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
