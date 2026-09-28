package intelligence

import (
	"context"
	"fmt"
	"math"
	"time"
)

// timeNow is an indirection so trend windows are testable.
var timeNow = time.Now

// TelemetryAnalyzer is Module 01: record-level signal readings for a device
// term — severity mix, record volume, and reporting trend. Every method
// degrades gracefully: no data yields a Label "Unknown" Signal with LOW
// confidence, never a panic and never a fabricated value.
type TelemetryAnalyzer struct {
	data Data
}

// Severity weights per MAUDE event_type. Death dominates, Injury is serious,
// Malfunction is a device problem without a reported harm. The weighted mean
// therefore spans 0.3 (all malfunctions) to 1.0 (all deaths).
const (
	weightDeath       = 1.0
	weightInjury      = 0.6
	weightMalfunction = 0.3
)

// AnalyzeSeverity reads the MAUDE event_type mix. Value is the weighted mean
// severity of all reports: (deaths*1.0 + injuries*0.6 + malfunctions*0.3 +
// other*0) / total.
func (a *TelemetryAnalyzer) AnalyzeSeverity(ctx context.Context, device string) (*Signal, error) {
	counts, err := a.data.EventTypeCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("severity: %w", err)
	}
	deaths := counts["Death"]
	injuries := counts["Injury"]
	malfunctions := counts["Malfunction"]
	total := 0
	for _, n := range counts {
		total += n
	}
	src := []string{"openfda_maude"}
	if total == 0 {
		return noData(SignalSeverity, "no MAUDE reports found for this device term", src), nil
	}
	value := (float64(deaths)*weightDeath + float64(injuries)*weightInjury +
		float64(malfunctions)*weightMalfunction) / float64(total)
	return withSample(&Signal{
		SignalType: SignalSeverity,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%d MAUDE reports: %d deaths, %d injuries, %d malfunctions; weighted mean severity (death=1.0, injury=0.6, malfunction=0.3)",
			total, deaths, injuries, malfunctions),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, total, unitMAUDE), nil
}

// AnalyzeVolume compares the device's MAUDE event volume against the p95
// event volume of the most-reported device types. Value = events / p95,
// capped at 1.0. The baseline counts MAUDE events only, so recalls are kept
// out of the numerator and reported separately.
func (a *TelemetryAnalyzer) AnalyzeVolume(ctx context.Context, device string) (*Signal, error) {
	counts, err := a.data.EventTypeCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("volume: events: %w", err)
	}
	events := 0
	for _, n := range counts {
		events += n
	}
	recalls, err := a.data.RecallTotal(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("volume: recalls: %w", err)
	}
	src := []string{"openfda_maude", "openfda_recall"}
	volume := events
	if volume == 0 {
		return noData(SignalVolume, fmt.Sprintf("no MAUDE reports found for this device term (%d recalls, not part of the volume index)", recalls), src), nil
	}
	p95, sample, err := a.data.VolumeBaseline(ctx)
	if err != nil {
		return nil, fmt.Errorf("volume: baseline: %w", err)
	}
	if p95 <= 0 {
		return noData(SignalVolume, "volume baseline unavailable (p95 is zero)", src), nil
	}
	value := math.Min(1.0, float64(volume)/float64(p95))
	conf := ConfidenceMedium
	if sample >= 100 {
		conf = ConfidenceHigh
	}
	if sample < 20 {
		conf = ConfidenceLow
	}
	// The band comes from the peer baseline, so that is the sample reported.
	return withSample(&Signal{
		SignalType: SignalVolume,
		Value:      round2(value),
		Label:      labelForDirection(directionActivity, value),
		Reasoning: fmt.Sprintf(
			"%d MAUDE events vs p95 volume %d (MAUDE events) across the %d most-reported device types; %d recalls reported separately (not in the index)",
			volume, p95, sample, recalls),
		ConfidenceLevel: conf,
		SourceType:      src,
	}, sample, "peer device types"), nil
}

// AnalyzeTrend compares MAUDE report counts in the last recentDays against
// the recentDays before that. Value is the positive growth slope
// (recent-prior)/prior capped at 1.0 — only growth raises the signal; a
// decline reads Low, with the direction stated in the reasoning. Reporting
// lag systematically undercounts the recent window, so growth is understated
// and never proof of a real-world surge on its own.
func (a *TelemetryAnalyzer) AnalyzeTrend(ctx context.Context, device string, recentDays int) (*Signal, error) {
	if recentDays < 1 {
		return nil, fmt.Errorf("trend: recentDays must be >= 1 (got %d)", recentDays)
	}
	now := timeNow().UTC()
	mid := now.AddDate(0, 0, -recentDays+1) // inclusive [mid, now] = recentDays days
	old := mid.AddDate(0, 0, -recentDays)
	const day = "20060102"

	recent, err := a.data.EventTotalWindow(ctx, device, mid.Format(day), now.Format(day))
	if err != nil {
		return nil, fmt.Errorf("trend: recent window: %w", err)
	}
	prior, err := a.data.EventTotalWindow(ctx, device, old.Format(day), mid.AddDate(0, 0, -1).Format(day))
	if err != nil {
		return nil, fmt.Errorf("trend: prior window: %w", err)
	}
	src := []string{"openfda_maude"}

	switch {
	case recent == 0 && prior == 0:
		return noData(SignalTrend,
			fmt.Sprintf("no MAUDE reports in either %d-day window", recentDays), src), nil
	case prior == 0:
		return &Signal{
			SignalType:      SignalTrend,
			Value:           1.0,
			Label:           labelFor(1.0),
			Reasoning:       fmt.Sprintf("new signal: %d reports in the last %d days with none in the prior %d days (no baseline to scale against)", recent, recentDays, recentDays),
			ConfidenceLevel: ConfidenceLow, // no prior baseline
			SourceType:      src,
		}, nil
	}

	slope := (float64(recent) - float64(prior)) / float64(prior)
	value := math.Min(1.0, math.Max(0, slope))
	direction := "stable"
	if slope > 0.1 {
		direction = "increasing"
	} else if slope < -0.1 {
		direction = "declining"
	}
	return &Signal{
		SignalType: SignalTrend,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%s: %d reports in the last %d days vs %d in the prior %d days (%+.0f%%); reporting lag undercounts the recent window",
			direction, recent, recentDays, prior, recentDays, slope*100),
		ConfidenceLevel: confidenceForSample(recent + prior),
		SourceType:      src,
	}, nil
}

// noData is the graceful empty reading (guardrail 7): explicit Unknown, zero
// value, LOW confidence, sample band "none" — absence of records is stated,
// never scored as safe.
func noData(signalType, reasoning string, src []string) *Signal {
	return &Signal{
		SignalType:      signalType,
		Value:           0,
		Label:           LabelUnknown,
		Reasoning:       reasoning + "; absence of records is not evidence of safety",
		ConfidenceLevel: ConfidenceLow,
		SampleSize:      0,
		SampleUnit:      "records",
		SampleBand:      SampleNone,
		SourceType:      src,
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
