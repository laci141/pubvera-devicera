package intelligence

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// timeParseCompact parses a compact YYYYMMDD date.
func timeParseCompact(s string) (time.Time, error) {
	return time.Parse("20060102", s)
}

// LifecycleAnalyzer is Module 08: where the device sits in its public-record
// life. It reads the MAUDE decade as five 2-year windows (oldest → newest)
// plus the enforcement record's dates. The newest window is systematically
// undercounted by reporting lag — stated wherever it matters.
type LifecycleAnalyzer struct {
	data Data
}

// Signal type names for Module 08.
const (
	SignalRecordNovelty  = "RECORD_NOVELTY"
	SignalLifecyclePhase = "LIFECYCLE_PHASE"
	SignalRecallRecency  = "RECALL_RECENCY"
)

// lifecycleWindows is how many 2-year windows the decade view uses.
const lifecycleWindows = 5

// decadeWindows returns MAUDE totals for five 2-year windows, oldest first.
func (a *LifecycleAnalyzer) decadeWindows(ctx context.Context, device string) ([]int, error) {
	now := timeNow().UTC()
	out := make([]int, lifecycleWindows)
	for i := 0; i < lifecycleWindows; i++ {
		// i=0 → oldest window, i=4 → the most recent two years.
		to := now.AddDate(0, 0, -(lifecycleWindows-1-i)*730)
		from := to.AddDate(0, 0, -730+1)
		if i < lifecycleWindows-1 {
			to = to.AddDate(0, 0, -1)
			from = from.AddDate(0, 0, -1)
		}
		n, err := a.data.EventTotalWindow(ctx, device, from.Format(day), to.Format(day))
		if err != nil {
			return nil, err
		}
		out[i] = n
	}
	return out, nil
}

// AnalyzeRecordNovelty reads how young the device's MAUDE record is. Value =
// (windows before the first activity) / total windows: a record active across
// the whole decade reads 0.0; one that only appeared in the newest window
// reads 0.8. A young record means thin history, not safety.
func (a *LifecycleAnalyzer) AnalyzeRecordNovelty(ctx context.Context, device string) (*Signal, error) {
	wins, err := a.decadeWindows(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("novelty: %w", err)
	}
	src := []string{"openfda_maude"}
	first := -1
	total := 0
	for i, n := range wins {
		total += n
		if n > 0 && first == -1 {
			first = i
		}
	}
	if first == -1 {
		return noData(SignalRecordNovelty, "no MAUDE reports anywhere in the last decade", src), nil
	}
	value := float64(first) / float64(lifecycleWindows)
	yearsActive := (lifecycleWindows - first) * 2
	return &Signal{
		SignalType: SignalRecordNovelty,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"MAUDE activity spans the last ~%d years of the 10-year view (decade windows: %s); a young record is thin history, not evidence of safety",
			yearsActive, winString(wins)),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, nil
}

// AnalyzeLifecyclePhase classifies the trajectory across the decade windows:
// emerging (first activity in the newest window), growing (newest ≥ 1.2x
// previous), declining (newest < 0.6x the decade peak), else mature. Value is
// the instability |newest-previous|/previous capped at 1.0 — distance from a
// steady state, not a hazard.
func (a *LifecycleAnalyzer) AnalyzeLifecyclePhase(ctx context.Context, device string) (*Signal, error) {
	wins, err := a.decadeWindows(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("phase: %w", err)
	}
	src := []string{"openfda_maude"}
	total := 0
	peak := 0
	for _, n := range wins {
		total += n
		if n > peak {
			peak = n
		}
	}
	if total == 0 {
		return noData(SignalLifecyclePhase, "no MAUDE reports anywhere in the last decade", src), nil
	}
	newest := wins[lifecycleWindows-1]
	prev := wins[lifecycleWindows-2]

	phase := "mature"
	switch {
	case prev == 0 && newest > 0:
		phase = "emerging"
	case float64(newest) >= 1.2*float64(prev):
		phase = "growing"
	case float64(newest) < 0.6*float64(peak):
		phase = "declining"
	}
	instability := 1.0
	if prev > 0 {
		instability = math.Min(1.0, math.Abs(float64(newest)-float64(prev))/float64(prev))
	}
	return &Signal{
		SignalType: SignalLifecyclePhase,
		Value:      round2(instability),
		Label:      labelFor(instability),
		Reasoning: fmt.Sprintf(
			"phase: %s (decade windows oldest→newest: %s; newest vs previous %d vs %d, decade peak %d); value is trajectory instability, not hazard; reporting lag undercounts the newest window",
			phase, winString(wins), newest, prev, peak),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, nil
}

// timelineFetch is how many enforcement records back the action lists.
const timelineFetch = 25

// recallRecencyScaleDays: a recall today reads 1.0, fading linearly to 0.0 at
// five years with no further recalls.
const recallRecencyScaleDays = 1825.0

// AnalyzeRecallRecency reads how fresh the enforcement record is: days since
// the newest recall, scaled over five years.
func (a *LifecycleAnalyzer) AnalyzeRecallRecency(ctx context.Context, device string) (*Signal, error) {
	actions, err := a.data.RecallActions(ctx, device, timelineFetch)
	if err != nil {
		return nil, fmt.Errorf("recency: %w", err)
	}
	src := []string{"openfda_recall"}
	last := ""
	for _, act := range actions {
		if act.Date > last {
			last = act.Date
		}
	}
	if last == "" {
		return noData(SignalRecallRecency, "no dated recalls on record for this device term", src), nil
	}
	lastT, err := timeParseCompact(last)
	if err != nil {
		return nil, fmt.Errorf("recency: bad date %q: %w", last, err)
	}
	days := timeNow().UTC().Sub(lastT).Hours() / 24
	value := math.Max(0, 1-days/recallRecencyScaleDays)
	// The newest-recall date is a direct feed fact; confidence reflects how
	// well-populated the record behind it is, not a statistical sample.
	conf := ConfidenceMedium
	if len(actions) >= 5 {
		conf = ConfidenceHigh
	}
	return &Signal{
		SignalType: SignalRecallRecency,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"newest recall initiated %s (%.0f days ago; today=1.0 fading to 0.0 at five years); recency of enforcement activity, not a harm reading",
			last, days),
		ConfidenceLevel: conf,
		SourceType:      src,
	}, nil
}

// winString renders the window counts oldest→newest for a reasoning line.
func winString(wins []int) string {
	parts := make([]string, len(wins))
	for i, n := range wins {
		parts[i] = fmt.Sprintf("%d", n)
	}
	return strings.Join(parts, " → ")
}
