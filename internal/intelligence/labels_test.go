package intelligence

import (
	"context"
	"testing"
)

// A capped VOLUME reading means the device is among the most-reported types:
// more public-record activity, not more concern. It must not borrow the
// concern word "Critical".
func TestVolumeAtCapReadsTopNotCritical(t *testing.T) {
	a := NewTelemetryAnalyzer(mockData{
		eventTypes: map[string]int{"Malfunction": 900}, p95: 300, sample: 100,
	})
	sig, err := a.AnalyzeVolume(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 1.0 {
		t.Fatalf("value=%v want 1.0 (capped)", sig.Value)
	}
	if sig.Label == "Critical" {
		t.Errorf("VOLUME at 1.0 labelled %q: an activity reading must not use the concern word", sig.Label)
	}
	if sig.Label != "Top" {
		t.Errorf("VOLUME at 1.0: label=%q want Top", sig.Label)
	}
}

// Every direction buckets on labelFor's thresholds; only the words differ.
func TestLabelForDirectionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		v                          float64
		concern, activity, quality string
	}{
		{0.0, LabelLow, LabelQuiet, LabelPoor},
		{0.3, LabelLow, LabelQuiet, LabelPoor},
		{0.31, LabelMedium, LabelModerate, LabelFair},
		{0.5, LabelMedium, LabelModerate, LabelFair},
		{0.51, LabelHigh, LabelBusy, LabelGood},
		{0.7, LabelHigh, LabelBusy, LabelGood},
		{0.71, LabelCritical, LabelTop, LabelStrong},
		{1.0, LabelCritical, LabelTop, LabelStrong},
	} {
		if got := labelFor(tc.v); got != tc.concern {
			t.Errorf("labelFor(%v)=%q want %q", tc.v, got, tc.concern)
		}
		if got := labelForDirection(directionConcern, tc.v); got != tc.concern {
			t.Errorf("concern(%v)=%q want %q", tc.v, got, tc.concern)
		}
		if got := labelForDirection(directionActivity, tc.v); got != tc.activity {
			t.Errorf("activity(%v)=%q want %q", tc.v, got, tc.activity)
		}
		if got := labelForDirection(directionQuality, tc.v); got != tc.quality {
			t.Errorf("quality(%v)=%q want %q", tc.v, got, tc.quality)
		}
	}
}

// INDEPENDENT_REPORTING is a quality reading where higher is better: 80%
// independent reporters is a strong record, not a critical one.
func TestIndependentReportingHighReadsStrong(t *testing.T) {
	a := NewReportingAnalyzer(mockData{reporterTypes: map[string]int{
		"Health Professional":    80,
		"Company representation": 20,
	}})
	sig, err := a.AnalyzeIndependentReporting(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value != 0.8 {
		t.Fatalf("value=%v want 0.8", sig.Value)
	}
	if sig.Label != "Strong" {
		t.Errorf("INDEPENDENT_REPORTING at 0.8: label=%q want Strong", sig.Label)
	}
}
