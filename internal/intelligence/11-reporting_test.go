package intelligence

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestIndependentReportingMergesCasings(t *testing.T) {
	a := NewReportingAnalyzer(mockData{reporterTypes: map[string]int{
		"Health Professional":    300,
		"Company representation": 235,
		"COMPANY REPRESENTATIVE": 185,
		"Consumer":               60,
		"Other":                  20,
	}})
	sig, err := a.AnalyzeIndependentReporting(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	// independent = 300+60 = 360 of 800 = 0.45.
	if sig.Value != 0.45 || sig.Label != LabelFair {
		t.Errorf("got %v/%q want 0.45/Fair", sig.Value, sig.Label)
	}
	for _, want := range []string{"360 of 800", "420 company-filed", "data provenance, not the device"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
}

func TestMissingEventDates(t *testing.T) {
	a := NewReportingAnalyzer(mockData{
		eventTypes:    map[string]int{"Malfunction": 700, "Injury": 300},
		missingTotals: map[string]int{"date_of_event": 150},
	})
	sig, err := a.AnalyzeMissingEventDates(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0.15 || sig.Label != LabelLow {
		t.Errorf("got %v/%q want 0.15/Low", sig.Value, sig.Label)
	}
	for _, want := range []string{"150 of 1000", "weakens time-based readings"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
}

func TestMakerConcentrationMergesCasings(t *testing.T) {
	a := NewReportingAnalyzer(mockData{makerCounts: map[string]int{
		"MEDTRONIC, INC.": 40,
		"Medtronic, Inc.": 40, // same maker, different casing → merged
		"ACME LTD":        20,
	}})
	sig, err := a.AnalyzeMakerConcentration(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	// Merged: MEDTRONIC 80 (0.8), ACME 20 (0.2) → HHI 0.64+0.04 = 0.68 High.
	if sig.Value != 0.68 || sig.Label != LabelHigh {
		t.Errorf("got %v/%q want 0.68/High", sig.Value, sig.Label)
	}
	for _, want := range []string{"2 reporting manufacturers", "MEDTRONIC, INC. (80%)", "one maker's product line"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
}

func TestReportingNoDataIsGracefulUnknown(t *testing.T) {
	a := NewReportingAnalyzer(mockData{})
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"reporter-mix":  func() (*Signal, error) { return a.AnalyzeIndependentReporting(ctx, "zzz") },
		"missing-dates": func() (*Signal, error) { return a.AnalyzeMissingEventDates(ctx, "zzz") },
		"maker-conc":    func() (*Signal, error) { return a.AnalyzeMakerConcentration(ctx, "zzz") },
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

// TestLiveReporting grounds Module 11 against the real API (MDI_LIVE=1).
func TestLiveReporting(t *testing.T) {
	if os.Getenv("MDI_LIVE") == "" {
		t.Skip("set MDI_LIVE=1 to run live grounding")
	}
	a := NewReportingAnalyzer(NewLiveData())
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"reporter-mix":  func() (*Signal, error) { return a.AnalyzeIndependentReporting(ctx, "pacemaker") },
		"missing-dates": func() (*Signal, error) { return a.AnalyzeMissingEventDates(ctx, "pacemaker") },
		"maker-conc":    func() (*Signal, error) { return a.AnalyzeMakerConcentration(ctx, "pacemaker") },
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
