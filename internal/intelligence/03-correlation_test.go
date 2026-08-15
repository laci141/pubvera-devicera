package intelligence

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestAnalyzeRecallSeverity(t *testing.T) {
	a := NewCorrelationAnalyzer(mockData{
		recallClasses: map[string]int{"Class I": 31, "Class II": 164, "Class III": 2},
	})
	sig, err := a.AnalyzeRecallSeverity(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	// (31*1.0 + 164*0.5 + 2*0.2)/197 = 113.4/197 ≈ 0.58
	if sig.Value != 0.58 {
		t.Errorf("value=%v want 0.58", sig.Value)
	}
	if sig.Label != LabelHigh {
		t.Errorf("label=%q want High", sig.Label)
	}
	for _, want := range []string{"31 Class I", "164 Class II", "I=1.0"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
	if sig.ConfidenceLevel != ConfidenceMedium { // 197 < 200
		t.Errorf("confidence=%q want MEDIUM for 197 recalls", sig.ConfidenceLevel)
	}
}

func TestAnalyzeCorroboration(t *testing.T) {
	a := NewCorrelationAnalyzer(mockData{
		eventTypes: map[string]int{"Malfunction": 100},
		recalls:    5,
		trials:     0, // one empty feed
		pubs:       40,
	})
	sig, err := a.AnalyzeCorroboration(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0.75 { // 3 of 4 feeds
		t.Errorf("value=%v want 0.75", sig.Value)
	}
	for _, want := range []string{"3 of 4", "empty: device trials", "not hazard"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
}

func TestAnalyzeEvidenceGap(t *testing.T) {
	// 10,000 events with only 100 evidence records → 10 per 1000 events →
	// value = 1 - 10/50 = 0.8 Critical (a real gap).
	a := NewCorrelationAnalyzer(mockData{
		eventTypes: map[string]int{"Malfunction": 10000},
		trials:     40,
		pubs:       60,
	})
	sig, err := a.AnalyzeEvidenceGap(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0.8 || sig.Label != LabelCritical {
		t.Errorf("got %v/%q want 0.8/Critical", sig.Value, sig.Label)
	}
	for _, want := range []string{"10.0 per 1000", "thin literature, not proven harm"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}

	// Rich literature: 1000 events, 200 evidence records → 200/1000 → no gap.
	a = NewCorrelationAnalyzer(mockData{
		eventTypes: map[string]int{"Malfunction": 1000}, trials: 100, pubs: 100,
	})
	sig, err = a.AnalyzeEvidenceGap(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0 || sig.Label != LabelLow {
		t.Errorf("rich evidence: got %v/%q want 0/Low", sig.Value, sig.Label)
	}

	t.Run("implausible ratio returns Unknown", func(t *testing.T) {
		// A disease-like term: 20M publications against a single MAUDE event.
		a := NewCorrelationAnalyzer(mockData{
			eventTypes: map[string]int{"Malfunction": 1},
			trials:     0,
			pubs:       20000000,
		})
		sig, err := a.AnalyzeEvidenceGap(context.Background(), "cancer")
		if err != nil {
			t.Fatal(err)
		}
		if sig.Label != LabelUnknown {
			t.Errorf("got %q want Unknown", sig.Label)
		}
		if !strings.Contains(sig.Reasoning, "implausibly high") {
			t.Errorf("reasoning missing %q: %s", "implausibly high", sig.Reasoning)
		}
	})
}

func TestCorrelationNoDataIsGracefulUnknown(t *testing.T) {
	a := NewCorrelationAnalyzer(mockData{})
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"recall-severity": func() (*Signal, error) { return a.AnalyzeRecallSeverity(ctx, "zzz") },
		"corroboration":   func() (*Signal, error) { return a.AnalyzeCorroboration(ctx, "zzz") },
		"evidence-gap":    func() (*Signal, error) { return a.AnalyzeEvidenceGap(ctx, "zzz") },
	} {
		sig, err := f()
		if err != nil {
			t.Fatalf("%s: no data must not error: %v", name, err)
		}
		if sig.Label != LabelUnknown || sig.Value != 0 {
			t.Errorf("%s no-data: %+v want Unknown/0", name, sig)
		}
		if !strings.Contains(sig.Reasoning, "not evidence of safety") {
			t.Errorf("%s: no-data reasoning must never imply safety", name)
		}
	}
}

// TestLiveCorrelation grounds Module 03 against the real APIs (MDI_LIVE=1).
func TestLiveCorrelation(t *testing.T) {
	if os.Getenv("MDI_LIVE") == "" {
		t.Skip("set MDI_LIVE=1 to run live grounding")
	}
	a := NewCorrelationAnalyzer(NewLiveData())
	ctx := context.Background()
	for name, f := range map[string]func() (*Signal, error){
		"recall-severity": func() (*Signal, error) { return a.AnalyzeRecallSeverity(ctx, "pacemaker") },
		"corroboration":   func() (*Signal, error) { return a.AnalyzeCorroboration(ctx, "pacemaker") },
		"evidence-gap":    func() (*Signal, error) { return a.AnalyzeEvidenceGap(ctx, "pacemaker") },
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
