package intelligence

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// pacemakerRootCauses is the real openFDA root_cause_description count for
// "pacemaker" (2026-10-12): 282 recalls, 98 of them design-type.
var pacemakerRootCauses = map[string]int{
	"Other": 71, "Device Design": 69, "Process control": 31,
	"Nonconforming Material/Component": 28, "Under Investigation by firm": 21,
	"Software design": 17, "Component design/selection": 12, "Process design": 10,
	"Unknown/Undetermined by firm": 6, "Employee error": 3,
	"Material/Component Contamination": 2, "No Marketing Application": 2,
	"Packaging": 2, "Packaging process control": 2, "Component change control": 1,
	"Labeling Change Control": 1, "Labeling mix-ups": 1, "Pending": 1,
	"Process change control": 1, "Software Manufacturing/Software Deployment": 1,
}

// errData fails the two compliance reads.
type errData struct{ mockData }

var errFeed = errors.New("recall feed down")

func (errData) RecallRootCauseCounts(context.Context, string) (map[string]int, error) {
	return nil, errFeed
}
func (errData) RecallStatusCounts(context.Context, string) (map[string]int, error) {
	return nil, errFeed
}

func TestRootCauseDesignShare(t *testing.T) {
	a := NewComplianceAnalyzer(mockData{rootCauses: pacemakerRootCauses})
	sig, err := a.AnalyzeRootCauseDesign(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	// Device Design 69 + Software design 17 + Component design/selection 12 = 98 of 282.
	if sig.SignalType != SignalRootCauseDesign || sig.Value != 0.35 || sig.Label != LabelMedium {
		t.Errorf("got %s %v/%q want ROOT_CAUSE_DESIGN 0.35/Medium", sig.SignalType, sig.Value, sig.Label)
	}
	if sig.ConfidenceLevel != ConfidenceHigh || sig.SampleSize != 282 || sig.SampleUnit != "recalls" {
		t.Errorf("sample: %s %d %q want HIGH 282 recalls", sig.ConfidenceLevel, sig.SampleSize, sig.SampleUnit)
	}
	if len(sig.SourceType) != 1 || sig.SourceType[0] != "openfda_device_recall" {
		t.Errorf("source=%v", sig.SourceType)
	}
	for _, want := range []string{"98 of 282", "Device Design", "Software design", "Component design/selection"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
}

func TestRootCauseDesignIgnoresNonDesignTerms(t *testing.T) {
	a := NewComplianceAnalyzer(mockData{rootCauses: map[string]int{
		"Process design": 40, "Other": 60,
	}})
	sig, err := a.AnalyzeRootCauseDesign(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0 || sig.Label != LabelLow {
		t.Errorf("got %v/%q want 0/Low (process design is not a design-type term)", sig.Value, sig.Label)
	}
}

func TestOpenRecallShare(t *testing.T) {
	a := NewComplianceAnalyzer(mockData{
		recallStatuses: map[string]int{"Ongoing": 60, "Terminated": 30, "Completed": 10},
	})
	sig, err := a.AnalyzeOpenRecallShare(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	if sig.SignalType != SignalOpenRecallShare || sig.Value != 0.6 || sig.Label != LabelHigh {
		t.Errorf("got %s %v/%q want OPEN_RECALL_SHARE 0.6/High", sig.SignalType, sig.Value, sig.Label)
	}
	if sig.ConfidenceLevel != ConfidenceMedium || sig.SampleSize != 100 {
		t.Errorf("sample: %s %d want MEDIUM 100", sig.ConfidenceLevel, sig.SampleSize)
	}
	if len(sig.SourceType) != 1 || sig.SourceType[0] != "openfda_recall" {
		t.Errorf("source=%v", sig.SourceType)
	}
	if !strings.Contains(sig.Reasoning, "60 of 100") {
		t.Errorf("reasoning: %s", sig.Reasoning)
	}
}

func TestComplianceTotalZeroIsNoData(t *testing.T) {
	a := NewComplianceAnalyzer(mockData{})
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"design": func() (*Signal, error) { return a.AnalyzeRootCauseDesign(ctx, "zzz") },
		"open":   func() (*Signal, error) { return a.AnalyzeOpenRecallShare(ctx, "zzz") },
	} {
		sig, err := f()
		if err != nil {
			t.Fatalf("%s: no data must not error: %v", name, err)
		}
		if sig.Label != LabelUnknown || sig.Value != 0 || sig.SampleBand != SampleNone {
			t.Errorf("%s no-data: %+v want Unknown/0/none", name, sig)
		}
	}
}

func TestComplianceDataErrorIsWrapped(t *testing.T) {
	a := NewComplianceAnalyzer(errData{})
	ctx := context.Background()
	_, err := a.AnalyzeRootCauseDesign(ctx, "x")
	if !errors.Is(err, errFeed) || !strings.HasPrefix(err.Error(), "compliance-design: ") {
		t.Errorf("design error=%v want wrapped errFeed with compliance-design prefix", err)
	}
	_, err = a.AnalyzeOpenRecallShare(ctx, "x")
	if !errors.Is(err, errFeed) || !strings.HasPrefix(err.Error(), "compliance-open: ") {
		t.Errorf("open error=%v want wrapped errFeed with compliance-open prefix", err)
	}
}

// TestLiveCompliance grounds Module 04 against the real API (MDI_LIVE=1).
func TestLiveCompliance(t *testing.T) {
	if os.Getenv("MDI_LIVE") == "" {
		t.Skip("set MDI_LIVE=1 to run live grounding")
	}
	a := NewComplianceAnalyzer(NewLiveData())
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"design": func() (*Signal, error) { return a.AnalyzeRootCauseDesign(ctx, "pacemaker") },
		"open":   func() (*Signal, error) { return a.AnalyzeOpenRecallShare(ctx, "pacemaker") },
	} {
		sig, err := f()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sig.Value < 0 || sig.Value > 1 {
			t.Errorf("%s: value %v out of [0,1]", name, sig.Value)
		}
		t.Logf("%s: %+v", name, sig)
	}
}
