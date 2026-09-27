package intelligence

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPublicationMomentumAccelerating(t *testing.T) {
	pinClock(t) // 2026-07-09 → recent 2022-2026, prior 2017-2021 for years=5
	a := NewResearchAnalyzer(mockData{pubWindows: map[string]int{
		"2022-2026": 300,
		"2017-2021": 200,
	}})
	sig, err := a.AnalyzePublicationMomentum(context.Background(), "pacemaker", 5)
	if err != nil {
		t.Fatal(err)
	}
	// slope (300-200)/200 = 0.5 → Medium.
	if sig.Value != 0.5 || sig.Label != LabelMedium {
		t.Errorf("got %v/%q want 0.5/Medium", sig.Value, sig.Label)
	}
	for _, want := range []string{"accelerating", "+50%", "partially indexed"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
	if sig.ConfidenceLevel != ConfidenceHigh { // 500 pubs
		t.Errorf("confidence=%q want HIGH", sig.ConfidenceLevel)
	}
}

func TestPublicationMomentumSlowingReadsLow(t *testing.T) {
	pinClock(t)
	a := NewResearchAnalyzer(mockData{pubWindows: map[string]int{
		"2022-2026": 100,
		"2017-2021": 200,
	}})
	sig, err := a.AnalyzePublicationMomentum(context.Background(), "x", 5)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0 || !strings.Contains(sig.Reasoning, "slowing") {
		t.Errorf("slowing: got %v %q", sig.Value, sig.Reasoning)
	}
}

func TestActiveResearchShare(t *testing.T) {
	a := NewResearchAnalyzer(mockData{
		trials: 400,
		trialStatuses: map[string]int{
			"RECRUITING|ACTIVE_NOT_RECRUITING|ENROLLING_BY_INVITATION": 88,
		},
	})
	sig, err := a.AnalyzeActiveResearch(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0.22 { // 88/400
		t.Errorf("value=%v want 0.22", sig.Value)
	}
	for _, want := range []string{"88 of 400", "not device trouble"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
}

func TestTrialAttrition(t *testing.T) {
	a := NewResearchAnalyzer(mockData{trialStatuses: map[string]int{
		"TERMINATED|WITHDRAWN|SUSPENDED": 47,
		"COMPLETED":                      153,
	}})
	sig, err := a.AnalyzeTrialAttrition(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0.24 { // 47/200 = 0.235 → 0.24
		t.Errorf("value=%v want 0.24", sig.Value)
	}
	for _, want := range []string{"47 of 200 decided", "funding and enrollment"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
}

func TestResearchNoDataIsGracefulUnknown(t *testing.T) {
	pinClock(t)
	a := NewResearchAnalyzer(mockData{})
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"momentum":  func() (*Signal, error) { return a.AnalyzePublicationMomentum(ctx, "zzz", 5) },
		"active":    func() (*Signal, error) { return a.AnalyzeActiveResearch(ctx, "zzz") },
		"attrition": func() (*Signal, error) { return a.AnalyzeTrialAttrition(ctx, "zzz") },
	} {
		sig, err := f()
		if err != nil {
			t.Fatalf("%s: no data must not error: %v", name, err)
		}
		if sig.Label != LabelUnknown || sig.Value != 0 {
			t.Errorf("%s no-data: %+v want Unknown/0", name, sig)
		}
	}
	if _, err := a.AnalyzePublicationMomentum(ctx, "x", 0); err == nil {
		t.Error("years < 1 must error")
	}
}

// TestLiveResearch grounds Module 10 against the real APIs (MDI_LIVE=1).
func TestLiveResearch(t *testing.T) {
	if os.Getenv("MDI_LIVE") == "" {
		t.Skip("set MDI_LIVE=1 to run live grounding")
	}
	a := NewResearchAnalyzer(NewLiveData())
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"momentum":  func() (*Signal, error) { return a.AnalyzePublicationMomentum(ctx, "pacemaker", 5) },
		"active":    func() (*Signal, error) { return a.AnalyzeActiveResearch(ctx, "pacemaker") },
		"attrition": func() (*Signal, error) { return a.AnalyzeTrialAttrition(ctx, "pacemaker") },
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
