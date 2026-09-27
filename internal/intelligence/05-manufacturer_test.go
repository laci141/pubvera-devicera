package intelligence

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestFirmRecallSeverity(t *testing.T) {
	a := NewManufacturerAnalyzer(mockData{
		firmClasses: map[string]int{"Class I": 10, "Class II": 30},
	})
	sig, err := a.AnalyzeRecallSeverity(context.Background(), "Acme")
	if err != nil {
		t.Fatal(err)
	}
	// (10*1.0 + 30*0.5)/40 = 0.62(5) → 0.63 rounded
	if sig.Value != 0.63 || sig.Label != LabelHigh {
		t.Errorf("got %v/%q want 0.63/High", sig.Value, sig.Label)
	}
	if !strings.Contains(sig.Reasoning, "subsidiaries count separately") {
		t.Errorf("reasoning must state the phrase-match caveat: %s", sig.Reasoning)
	}
}

func TestFirmRecallTrendGrowth(t *testing.T) {
	pinClock(t)
	// Build the window keys with the same AddDate arithmetic the module uses,
	// so the mock cannot drift from the implementation's date math.
	const periodDays = 181
	now := timeNow().UTC()
	mid := now.AddDate(0, 0, -periodDays+1)
	old := mid.AddDate(0, 0, -periodDays)
	recentKey := mid.Format(day) + "-" + now.Format(day)
	priorKey := old.Format(day) + "-" + mid.AddDate(0, 0, -1).Format(day)

	a := NewManufacturerAnalyzer(mockData{firmWindows: map[string]int{
		recentKey: 18,
		priorKey:  12,
	}})
	sig, err := a.AnalyzeRecallTrend(context.Background(), "Acme", periodDays)
	if err != nil {
		t.Fatal(err)
	}
	// slope (18-12)/12 = 0.5 → value 0.5, Medium, "increasing"
	if sig.Value != 0.5 || sig.Label != LabelMedium {
		t.Errorf("got %v/%q want 0.5/Medium", sig.Value, sig.Label)
	}
	if !strings.Contains(sig.Reasoning, "increasing") || !strings.Contains(sig.Reasoning, "+50%") {
		t.Errorf("reasoning: %s", sig.Reasoning)
	}
}

func TestFirmOpenRecalls(t *testing.T) {
	a := NewManufacturerAnalyzer(mockData{
		firmStatuses: map[string]int{"Ongoing": 60, "Terminated": 30, "Completed": 10},
	})
	sig, err := a.AnalyzeOpenRecalls(context.Background(), "Acme")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0.6 || sig.Label != LabelHigh {
		t.Errorf("got %v/%q want 0.6/High", sig.Value, sig.Label)
	}
	for _, want := range []string{"60 of 100", "not a harm reading"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
}

func TestManufacturerNoDataIsGracefulUnknown(t *testing.T) {
	pinClock(t)
	a := NewManufacturerAnalyzer(mockData{})
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"severity": func() (*Signal, error) { return a.AnalyzeRecallSeverity(ctx, "NoSuchFirm") },
		"trend":    func() (*Signal, error) { return a.AnalyzeRecallTrend(ctx, "NoSuchFirm", 365) },
		"open":     func() (*Signal, error) { return a.AnalyzeOpenRecalls(ctx, "NoSuchFirm") },
	} {
		sig, err := f()
		if err != nil {
			t.Fatalf("%s: no data must not error: %v", name, err)
		}
		if sig.Label != LabelUnknown || sig.Value != 0 {
			t.Errorf("%s no-data: %+v want Unknown/0", name, sig)
		}
	}
	if _, err := a.AnalyzeRecallTrend(ctx, "x", 0); err == nil {
		t.Error("periodDays < 1 must error")
	}
}

// TestLiveManufacturer grounds Module 05 against the real API (MDI_LIVE=1).
func TestLiveManufacturer(t *testing.T) {
	if os.Getenv("MDI_LIVE") == "" {
		t.Skip("set MDI_LIVE=1 to run live grounding")
	}
	a := NewManufacturerAnalyzer(NewLiveData())
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"severity": func() (*Signal, error) { return a.AnalyzeRecallSeverity(ctx, "Medtronic") },
		"trend":    func() (*Signal, error) { return a.AnalyzeRecallTrend(ctx, "Medtronic", 365) },
		"open":     func() (*Signal, error) { return a.AnalyzeOpenRecalls(ctx, "Medtronic") },
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
