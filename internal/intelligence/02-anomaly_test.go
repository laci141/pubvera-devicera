package intelligence

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func pinClock(t *testing.T) {
	t.Helper()
	old := timeNow
	timeNow = func() time.Time { return time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { timeNow = old })
}

func TestDetectSurge(t *testing.T) {
	pinClock(t)
	// Last 7 days: 50 reports (~7.1/day). Prior 70-day baseline: 20 (~0.29/day)
	// → ratio ~25x, saturates at 1.0.
	a := NewAnomalyAnalyzer(mockData{windows: map[string]int{
		"20260703-20260709": 50,
		"20260424-20260702": 20,
	}})
	sig, err := a.DetectSurge(context.Background(), "pacemaker", 7, 70)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 1.0 || sig.Label != LabelCritical {
		t.Errorf("got %v/%q want 1.0/Critical", sig.Value, sig.Label)
	}
	for _, want := range []string{"reports/day", "baseline", "reporting lag"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
	if sig.ConfidenceLevel != ConfidenceMedium { // 70 total
		t.Errorf("confidence=%q want MEDIUM", sig.ConfidenceLevel)
	}
}

func TestDetectSurgeStableIsLow(t *testing.T) {
	pinClock(t)
	// Same daily rate in both windows → ratio 1.0 → value 0.
	a := NewAnomalyAnalyzer(mockData{windows: map[string]int{
		"20260703-20260709": 7,
		"20260424-20260702": 70,
	}})
	sig, err := a.DetectSurge(context.Background(), "x", 7, 70)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0 || sig.Label != LabelLow {
		t.Errorf("stable rate: got %v/%q want 0/Low", sig.Value, sig.Label)
	}
}

func TestDetectSurgeValidation(t *testing.T) {
	a := NewAnomalyAnalyzer(mockData{})
	if _, err := a.DetectSurge(context.Background(), "x", 0, 70); err == nil {
		t.Error("recentDays < 1 must error")
	}
	if _, err := a.DetectSurge(context.Background(), "x", 30, 7); err == nil {
		t.Error("baseline shorter than recent must error")
	}
}

func TestDetectNewPattern(t *testing.T) {
	pinClock(t)
	a := NewAnomalyAnalyzer(mockData{typeWindows: map[string]map[string]int{
		"20260610-20260709": {"Death": 5, "Malfunction": 15}, // recent 30 days
		"19900101-20260609": {"Malfunction": 900},            // history: no deaths ever
	}})
	sig, err := a.DetectNewPattern(context.Background(), "pacemaker", 30)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0.25 { // 5 of 20 recent reports carry the unseen type
		t.Errorf("value=%v want 0.25", sig.Value)
	}
	if !strings.Contains(sig.Reasoning, "Death") || !strings.Contains(sig.Reasoning, "never seen before") {
		t.Errorf("reasoning must name the new type: %s", sig.Reasoning)
	}
}

func TestDetectNewPatternNoneIsLow(t *testing.T) {
	pinClock(t)
	a := NewAnomalyAnalyzer(mockData{typeWindows: map[string]map[string]int{
		"20260610-20260709": {"Malfunction": 60},
		"19900101-20260609": {"Malfunction": 900, "Injury": 10},
	}})
	sig, err := a.DetectNewPattern(context.Background(), "x", 30)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0 || sig.Label != LabelLow {
		t.Errorf("no new types: got %v/%q want 0/Low", sig.Value, sig.Label)
	}
	if !strings.Contains(sig.Reasoning, "no new pattern") {
		t.Errorf("reasoning: %s", sig.Reasoning)
	}
}

func TestDetectVolumeShift(t *testing.T) {
	pinClock(t)
	// Recent 30-day period: 40. Four prior periods: 10 each (avg 10) →
	// +300% → saturates at exactly 1.0.
	a := NewAnomalyAnalyzer(mockData{windows: map[string]int{
		"20260610-20260709": 40,
		"20260510-20260608": 10,
		"20260409-20260508": 10,
		"20260309-20260407": 10,
		"20260206-20260306": 10,
	}})
	sig, err := a.DetectVolumeShift(context.Background(), "pacemaker", 30)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 1.0 || sig.Label != LabelTop {
		t.Errorf("got %v/%q want 1.0/Top (+300%% saturates)", sig.Value, sig.Label)
	}
	for _, want := range []string{"above average", "+300%", "prior 4 periods"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
}

func TestAnomalyNoDataIsGracefulUnknown(t *testing.T) {
	pinClock(t)
	a := NewAnomalyAnalyzer(mockData{})
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"surge":        func() (*Signal, error) { return a.DetectSurge(ctx, "zzz", 7, 70) },
		"new-pattern":  func() (*Signal, error) { return a.DetectNewPattern(ctx, "zzz", 30) },
		"volume-shift": func() (*Signal, error) { return a.DetectVolumeShift(ctx, "zzz", 30) },
	} {
		sig, err := f()
		if err != nil {
			t.Fatalf("%s: no data must not error: %v", name, err)
		}
		if sig.Label != LabelUnknown || sig.Value != 0 || sig.ConfidenceLevel != ConfidenceLow {
			t.Errorf("%s no-data: %+v want Unknown/0/LOW", name, sig)
		}
		if !strings.Contains(sig.Reasoning, "not evidence of safety") {
			t.Errorf("%s: no-data reasoning must never imply safety", name)
		}
	}
}

// TestLiveAnomaly grounds Module 02 against the real MAUDE API (MDI_LIVE=1).
func TestLiveAnomaly(t *testing.T) {
	if os.Getenv("MDI_LIVE") == "" {
		t.Skip("set MDI_LIVE=1 to run live grounding")
	}
	a := NewAnomalyAnalyzer(NewLiveData())
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"surge":        func() (*Signal, error) { return a.DetectSurge(ctx, "pacemaker", 30, 180) },
		"new-pattern":  func() (*Signal, error) { return a.DetectNewPattern(ctx, "pacemaker", 90) },
		"volume-shift": func() (*Signal, error) { return a.DetectVolumeShift(ctx, "pacemaker", 90) },
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
