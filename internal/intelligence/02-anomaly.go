package intelligence

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

// AnomalyAnalyzer is Module 02: trend-BREAKING readings, where Module 01's
// trend is the smooth view. Three detectors, all over MAUDE report dates:
//
//   - Surge: the last few days' daily report rate vs the baseline daily rate.
//   - NewPattern: event types present recently but absent from the history.
//   - VolumeShift: the recent period's volume vs the average of the prior
//     periods of the same length.
//
// Same guardrails as every module: normalized values with the scaling stated
// in the reasoning, confidence from sample size, graceful Unknown on no data,
// and the reporting-lag caveat wherever a recent window is involved.
type AnomalyAnalyzer struct {
	data Data
}

const day = "20060102"

// surgeScale: a recent rate 5x the baseline rate saturates the signal at 1.0.
const surgeScale = 4.0

// DetectSurge compares the daily report rate of the last recentDays against
// the daily rate of the preceding baselineDays.
func (a *AnomalyAnalyzer) DetectSurge(ctx context.Context, device string, recentDays, baselineDays int) (*Signal, error) {
	if recentDays < 1 || baselineDays < recentDays {
		return nil, fmt.Errorf("surge: need recentDays >= 1 and baselineDays >= recentDays (got %d, %d)", recentDays, baselineDays)
	}
	now := timeNow().UTC()
	mid := now.AddDate(0, 0, -recentDays+1) // inclusive [mid, now] = recentDays days
	old := mid.AddDate(0, 0, -baselineDays)
	src := []string{"openfda_maude"}

	recent, err := a.data.EventTotalWindow(ctx, device, mid.Format(day), now.Format(day))
	if err != nil {
		return nil, fmt.Errorf("surge: recent: %w", err)
	}
	baseline, err := a.data.EventTotalWindow(ctx, device, old.Format(day), mid.AddDate(0, 0, -1).Format(day))
	if err != nil {
		return nil, fmt.Errorf("surge: baseline: %w", err)
	}
	if recent == 0 && baseline == 0 {
		return noData(SignalSurge, fmt.Sprintf("no MAUDE reports in the last %d days or the %d-day baseline", recentDays, baselineDays), src), nil
	}
	recentRate := float64(recent) / float64(recentDays)
	baseRate := float64(baseline) / float64(baselineDays)
	if baseRate == 0 {
		return &Signal{
			SignalType:      SignalSurge,
			Value:           1.0,
			Label:           labelFor(1.0),
			Reasoning:       fmt.Sprintf("new activity: %d reports in the last %d days after a silent %d-day baseline (no rate to scale against)", recent, recentDays, baselineDays),
			ConfidenceLevel: ConfidenceLow,
			SourceType:      src,
		}, nil
	}
	ratio := recentRate / baseRate
	value := math.Min(1.0, math.Max(0, (ratio-1)/surgeScale))
	return &Signal{
		SignalType: SignalSurge,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%.1f reports/day over the last %d days vs %.1f/day baseline (%.1fx; 5x saturates at 1.0); reporting lag undercounts the recent window",
			recentRate, recentDays, baseRate, ratio),
		ConfidenceLevel: confidenceForSample(recent + baseline),
		SourceType:      src,
	}, nil
}

// DetectNewPattern looks for event types reported in the last recentDays that
// never appeared before that window. Value is the share of recent reports
// carrying a historically unseen type.
func (a *AnomalyAnalyzer) DetectNewPattern(ctx context.Context, device string, recentDays int) (*Signal, error) {
	if recentDays < 1 {
		return nil, fmt.Errorf("new-pattern: recentDays must be >= 1 (got %d)", recentDays)
	}
	now := timeNow().UTC()
	mid := now.AddDate(0, 0, -recentDays+1) // inclusive [mid, now] = recentDays days
	src := []string{"openfda_maude"}

	recent, err := a.data.EventTypeCountsWindow(ctx, device, mid.Format(day), now.Format(day))
	if err != nil {
		return nil, fmt.Errorf("new-pattern: recent: %w", err)
	}
	// History = everything strictly before the recent window.
	history, err := a.data.EventTypeCountsWindow(ctx, device, "19900101", mid.AddDate(0, 0, -1).Format(day))
	if err != nil {
		return nil, fmt.Errorf("new-pattern: history: %w", err)
	}
	recentTotal := 0
	for _, n := range recent {
		recentTotal += n
	}
	if recentTotal == 0 {
		return noData(SignalNewPattern, fmt.Sprintf("no MAUDE reports in the last %d days", recentDays), src), nil
	}
	var newTypes []string
	newCount := 0
	for t, n := range recent {
		if history[t] == 0 {
			newTypes = append(newTypes, t)
			newCount += n
		}
	}
	if len(newTypes) == 0 {
		return &Signal{
			SignalType:      SignalNewPattern,
			Value:           0,
			Label:           LabelLow,
			Reasoning:       fmt.Sprintf("all %d recent reports use event types already present in the history — no new pattern", recentTotal),
			ConfidenceLevel: confidenceForSample(recentTotal),
			SourceType:      src,
		}, nil
	}
	sort.Strings(newTypes)
	value := float64(newCount) / float64(recentTotal)
	return &Signal{
		SignalType: SignalNewPattern,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%d of %d recent reports carry event types never seen before this window: %s",
			newCount, recentTotal, strings.Join(newTypes, ", ")),
		ConfidenceLevel: confidenceForSample(recentTotal),
		SourceType:      src,
	}, nil
}

// volumeShiftPeriods: the recent period is compared against the average of
// this many preceding periods of the same length.
const volumeShiftPeriods = 4

// volumeShiftScale: +300% vs the period average saturates the signal at 1.0.
const volumeShiftScale = 3.0

// DetectVolumeShift compares the last periodDays' volume against the average
// of the preceding volumeShiftPeriods periods of the same length.
func (a *AnomalyAnalyzer) DetectVolumeShift(ctx context.Context, device string, periodDays int) (*Signal, error) {
	if periodDays < 1 {
		return nil, fmt.Errorf("volume-shift: periodDays must be >= 1 (got %d)", periodDays)
	}
	now := timeNow().UTC()
	src := []string{"openfda_maude"}

	window := func(i int) (int, error) { // i=0 recent, 1..N prior periods
		to := now.AddDate(0, 0, -i*periodDays)
		from := to.AddDate(0, 0, -periodDays+1)
		if i > 0 {
			to = to.AddDate(0, 0, -1)
			from = from.AddDate(0, 0, -1)
		}
		return a.data.EventTotalWindow(ctx, device, from.Format(day), to.Format(day))
	}

	recent, err := window(0)
	if err != nil {
		return nil, fmt.Errorf("volume-shift: recent: %w", err)
	}
	priorSum := 0
	for i := 1; i <= volumeShiftPeriods; i++ {
		n, err := window(i)
		if err != nil {
			return nil, fmt.Errorf("volume-shift: period %d: %w", i, err)
		}
		priorSum += n
	}
	if recent == 0 && priorSum == 0 {
		return noData(SignalVolumeShift, fmt.Sprintf("no MAUDE reports across %d periods of %d days", volumeShiftPeriods+1, periodDays), src), nil
	}
	avg := float64(priorSum) / float64(volumeShiftPeriods)
	if avg == 0 {
		return &Signal{
			SignalType:      SignalVolumeShift,
			Value:           1.0,
			Label:           labelFor(1.0),
			Reasoning:       fmt.Sprintf("new activity: %d reports in the last %d days after %d silent prior periods (no average to scale against)", recent, periodDays, volumeShiftPeriods),
			ConfidenceLevel: ConfidenceLow,
			SourceType:      src,
		}, nil
	}
	shift := float64(recent)/avg - 1
	value := math.Min(1.0, math.Max(0, shift/volumeShiftScale))
	direction := "stable"
	if shift > 0.1 {
		direction = "above average"
	} else if shift < -0.1 {
		direction = "below average"
	}
	return &Signal{
		SignalType: SignalVolumeShift,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%s: %d reports in the last %d days vs %.1f average over the prior %d periods (%+.0f%%; +300%% saturates at 1.0); reporting lag undercounts the recent window",
			direction, recent, periodDays, avg, volumeShiftPeriods, shift*100),
		ConfidenceLevel: confidenceForSample(recent + priorSum),
		SourceType:      src,
	}, nil
}
