package intelligence

import (
	"context"
	"testing"
)

// The Sample fields say how many records a reading counted and what they
// were. They are the honest name for what ConfidenceLevel always measured.

func checkSample(t *testing.T, name string, sig *Signal, size int, unit, band string) {
	t.Helper()
	if sig.SampleSize != size || sig.SampleUnit != unit || sig.SampleBand != band {
		t.Errorf("%s sample = %d %q %q, want %d %q %q",
			name, sig.SampleSize, sig.SampleUnit, sig.SampleBand, size, unit, band)
	}
}

func TestSampleSeverity(t *testing.T) {
	a := NewTelemetryAnalyzer(mockData{eventTypes: map[string]int{"Death": 3, "Injury": 5, "Malfunction": 10}})
	sig, err := a.AnalyzeSeverity(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	checkSample(t, "severity 18", sig, 18, "MAUDE reports", SampleSmall)

	a = NewTelemetryAnalyzer(mockData{eventTypes: map[string]int{"Death": 300}})
	if sig, err = a.AnalyzeSeverity(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	checkSample(t, "severity 300", sig, 300, "MAUDE reports", SampleLarge)
}

// VOLUME's band comes from the peer baseline, not the device's own count, so
// the sample it reports is the peer device types.
func TestSampleVolumeCountsPeerTypes(t *testing.T) {
	a := NewTelemetryAnalyzer(mockData{
		eventTypes: map[string]int{"Malfunction": 751555}, p95: 469766, sample: 100,
	})
	sig, err := a.AnalyzeVolume(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	checkSample(t, "volume", sig, 100, "peer device types", SampleLarge)

	a = NewTelemetryAnalyzer(mockData{eventTypes: map[string]int{"Malfunction": 200}, p95: 300, sample: 50})
	if sig, err = a.AnalyzeVolume(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	checkSample(t, "volume 50 peers", sig, 50, "peer device types", SampleMedium)
}

// With no prior baseline there is nothing to scale against: the band is
// "none" however many recent reports there are.
func TestSampleVolumeShiftNoBaselineIsNone(t *testing.T) {
	pinClock(t)
	a := NewAnomalyAnalyzer(mockData{windows: map[string]int{"20260610-20260709": 400}})
	sig, err := a.DetectVolumeShift(context.Background(), "x", 30)
	if err != nil {
		t.Fatal(err)
	}
	checkSample(t, "volume-shift no baseline", sig, 400, "MAUDE reports, no prior baseline", SampleNone)
	if sig.ConfidenceLevel != ConfidenceLow {
		t.Errorf("deprecated confidence=%q want LOW (unchanged)", sig.ConfidenceLevel)
	}
}

func TestSampleRecallRecencyCountsFetchedRecords(t *testing.T) {
	pinClock(t)
	a := NewLifecycleAnalyzer(mockData{recallActions: []ComplianceAction{
		act("20190117", "Class I", "Z-OLD"),
		act("20260331", "Class II", "Z-NEW"),
	}})
	sig, err := a.AnalyzeRecallRecency(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	checkSample(t, "recall recency", sig, 2, "enforcement records fetched", SampleMedium)
}

func TestSampleNoDataIsNone(t *testing.T) {
	sig, err := NewTelemetryAnalyzer(mockData{}).AnalyzeSeverity(context.Background(), "zzz")
	if err != nil {
		t.Fatal(err)
	}
	checkSample(t, "no data", sig, 0, "records", SampleNone)
}

// Every dossier signal carries a unit and a band, and the band is the same
// grade the deprecated ConfidenceLevel states (the no-baseline case aside).
func TestSampleOnEveryDossierSignal(t *testing.T) {
	pinClock(t)
	d, err := NewSynthesisAnalyzer(synthesisFixture()).Synthesize(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	units := map[string]string{
		SignalSeverity:             "MAUDE reports",
		SignalVolume:               "peer device types",
		SignalVolumeShift:          "records", // empty windows: no data
		SignalRecallSeverity:       "recalls",
		SignalCorroboration:        "records across feeds (mixed kinds)",
		SignalEvidenceGap:          "MAUDE reports",
		SignalPeerSeverityDelta:    "MAUDE reports",
		SignalLifecyclePhase:       "records", // empty windows: no data
		SignalRecallRecency:        "enforcement records fetched",
		SignalIndependentReporting: "source-type tags",
		SignalMissingEventDates:    "MAUDE reports",
	}
	bandFor := map[string]string{ConfidenceHigh: SampleLarge, ConfidenceMedium: SampleMedium, ConfidenceLow: SampleSmall}
	if len(d.Signals) != len(units) {
		t.Fatalf("signals=%d want %d", len(d.Signals), len(units))
	}
	for _, s := range d.Signals {
		if s.SampleUnit != units[s.SignalType] {
			t.Errorf("%s unit=%q want %q", s.SignalType, s.SampleUnit, units[s.SignalType])
		}
		want := bandFor[s.ConfidenceLevel]
		if s.Label == LabelUnknown {
			want = SampleNone
		}
		if s.SampleBand != want {
			t.Errorf("%s band=%q want %q (confidence %s)", s.SignalType, s.SampleBand, want, s.ConfidenceLevel)
		}
	}
	// Spot-check the counts behind the fixture.
	for _, s := range d.Signals {
		switch s.SignalType {
		case SignalSeverity, SignalEvidenceGap, SignalPeerSeverityDelta, SignalMissingEventDates:
			if s.SampleSize != 100 {
				t.Errorf("%s size=%d want 100", s.SignalType, s.SampleSize)
			}
		case SignalCorroboration: // 100 events + 5 recalls + 10 trials + 100 pubs
			if s.SampleSize != 215 {
				t.Errorf("corroboration size=%d want 215", s.SampleSize)
			}
		case SignalRecallSeverity:
			if s.SampleSize != 5 {
				t.Errorf("recall severity size=%d want 5", s.SampleSize)
			}
		}
	}
}
