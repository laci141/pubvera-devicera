package intelligence

import (
	"context"
	"fmt"
	"math"
)

// ResearchAnalyzer is Module 10: the research pipeline around a device —
// whether the literature is accelerating, how much of the trial portfolio is
// still active, and how often trials stop early. Research volume measures
// scientific attention; attention follows market size, novelty, and funding
// at least as much as clinical concern.
type ResearchAnalyzer struct {
	data Data
}

// Signal type names for Module 10.
const (
	SignalPublicationMomentum = "PUBLICATION_MOMENTUM"
	SignalActiveResearch      = "ACTIVE_RESEARCH"
	SignalTrialAttrition      = "TRIAL_ATTRITION"
)

// CT.gov overall-status groupings.
var (
	activeStatuses  = []string{"RECRUITING", "ACTIVE_NOT_RECRUITING", "ENROLLING_BY_INVITATION"}
	stoppedStatuses = []string{"TERMINATED", "WITHDRAWN", "SUSPENDED"}
	doneStatuses    = []string{"COMPLETED"}
)

// AnalyzePublicationMomentum compares PubMed output in the last `years`
// publication years against the `years` before that. Only growth raises the
// value (+100% saturates). The current year is partially indexed, so recent
// momentum is understated — stated in the reasoning.
func (a *ResearchAnalyzer) AnalyzePublicationMomentum(ctx context.Context, device string, years int) (*Signal, error) {
	if years < 1 {
		return nil, fmt.Errorf("momentum: years must be >= 1 (got %d)", years)
	}
	thisYear := timeNow().UTC().Year()
	recentFrom, recentTo := thisYear-years+1, thisYear
	priorFrom, priorTo := thisYear-2*years+1, thisYear-years
	src := []string{"pubmed"}

	recent, err := a.data.PublicationCountWindow(ctx, device, recentFrom, recentTo)
	if err != nil {
		return nil, fmt.Errorf("momentum: recent: %w", err)
	}
	prior, err := a.data.PublicationCountWindow(ctx, device, priorFrom, priorTo)
	if err != nil {
		return nil, fmt.Errorf("momentum: prior: %w", err)
	}
	switch {
	case recent == 0 && prior == 0:
		return noData(SignalPublicationMomentum, fmt.Sprintf("no PubMed publications in %d-%d", priorFrom, recentTo), src), nil
	case prior == 0:
		return &Signal{
			SignalType:      SignalPublicationMomentum,
			Value:           1.0,
			Label:           labelFor(1.0),
			Reasoning:       fmt.Sprintf("new literature: %d publications in %d-%d with none in %d-%d (no baseline to scale against)", recent, recentFrom, recentTo, priorFrom, priorTo),
			ConfidenceLevel: ConfidenceLow,
			SourceType:      src,
		}, nil
	}
	slope := (float64(recent) - float64(prior)) / float64(prior)
	value := math.Min(1.0, math.Max(0, slope))
	direction := "steady"
	if slope > 0.1 {
		direction = "accelerating"
	} else if slope < -0.1 {
		direction = "slowing"
	}
	return &Signal{
		SignalType: SignalPublicationMomentum,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%s: %d publications in %d-%d vs %d in %d-%d (%+.0f%%; +100%% saturates at 1.0); the current year is partially indexed, so recent momentum is understated",
			direction, recent, recentFrom, recentTo, prior, priorFrom, priorTo, slope*100),
		ConfidenceLevel: confidenceForSample(recent + prior),
		SourceType:      src,
	}, nil
}

// AnalyzeActiveResearch reads the share of the device's trial portfolio that
// is currently active (recruiting, enrolling, or running).
func (a *ResearchAnalyzer) AnalyzeActiveResearch(ctx context.Context, device string) (*Signal, error) {
	total, err := a.data.TrialTotal(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("active-research: total: %w", err)
	}
	src := []string{"clinicaltrials"}
	if total == 0 {
		return noData(SignalActiveResearch, "no device-intervention trials on ClinicalTrials.gov for this term", src), nil
	}
	active, err := a.data.TrialStatusTotal(ctx, device, activeStatuses)
	if err != nil {
		return nil, fmt.Errorf("active-research: active: %w", err)
	}
	value := math.Min(1.0, float64(active)/float64(total))
	return &Signal{
		SignalType: SignalActiveResearch,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%d of %d registered device trials are active (recruiting/enrolling/running); an active pipeline signals ongoing scientific attention, not device trouble",
			active, total),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, nil
}

// AnalyzeTrialAttrition reads how often the device's trials stop early:
// stopped (terminated/withdrawn/suspended) as a share of decided trials
// (stopped + completed). Trials stop for funding and enrollment reasons at
// least as often as for safety — stated in the reasoning.
func (a *ResearchAnalyzer) AnalyzeTrialAttrition(ctx context.Context, device string) (*Signal, error) {
	stopped, err := a.data.TrialStatusTotal(ctx, device, stoppedStatuses)
	if err != nil {
		return nil, fmt.Errorf("attrition: stopped: %w", err)
	}
	completed, err := a.data.TrialStatusTotal(ctx, device, doneStatuses)
	if err != nil {
		return nil, fmt.Errorf("attrition: completed: %w", err)
	}
	src := []string{"clinicaltrials"}
	decided := stopped + completed
	if decided == 0 {
		return noData(SignalTrialAttrition, "no completed or stopped device trials to measure attrition against", src), nil
	}
	value := float64(stopped) / float64(decided)
	return &Signal{
		SignalType: SignalTrialAttrition,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%d of %d decided trials stopped early (terminated/withdrawn/suspended) vs %d completed; trials stop for funding and enrollment reasons at least as often as for safety",
			stopped, decided, completed),
		ConfidenceLevel: confidenceForSample(decided),
		SourceType:      src,
	}, nil
}
