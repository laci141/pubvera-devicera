package intelligence

import (
	"context"
	"fmt"
	"math"
)

// ManufacturerAnalyzer is Module 05: firm-level enforcement readings, the
// manufacturer counterpart of Module 04's device view. All three signals read
// the openFDA enforcement feed filtered by recalling_firm; the firm string
// matches as a phrase, so subsidiaries with different registered names are
// separate firms — stated in every reasoning.
type ManufacturerAnalyzer struct {
	data Data
}

// Signal type names for Module 05.
const (
	SignalFirmRecallSeverity = "FIRM_RECALL_SEVERITY"
	SignalFirmRecallTrend    = "FIRM_RECALL_TREND"
	SignalFirmOpenRecalls    = "FIRM_OPEN_RECALLS"
)

// AnalyzeRecallSeverity reads the firm's recall classification mix (weighted
// class mean, I=1.0/II=0.5/III=0.2 — same scale as Modules 03/04).
func (a *ManufacturerAnalyzer) AnalyzeRecallSeverity(ctx context.Context, firm string) (*Signal, error) {
	counts, err := a.data.FirmRecallClassCounts(ctx, firm)
	if err != nil {
		return nil, fmt.Errorf("firm-severity: %w", err)
	}
	c1, c2, c3 := counts["Class I"], counts["Class II"], counts["Class III"]
	total := c1 + c2 + c3
	src := []string{"openfda_recall"}
	if total == 0 {
		return noData(SignalFirmRecallSeverity, "no recalls on record for this firm name (exact-phrase match; subsidiaries register separately)", src), nil
	}
	value := (float64(c1)*weightRecallClass1 + float64(c2)*weightRecallClass2 +
		float64(c3)*weightRecallClass3) / float64(total)
	return &Signal{
		SignalType: SignalFirmRecallSeverity,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"firm has %d recalls: %d Class I, %d Class II, %d Class III; weighted mean class (I=1.0, II=0.5, III=0.2); firm name matches as a phrase, subsidiaries count separately",
			total, c1, c2, c3),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, nil
}

// AnalyzeRecallTrend compares the firm's recall count in the last periodDays
// against the periodDays before that. Only growth raises the value; the
// slope scale matches Module 01's trend (100% growth = 1.0).
func (a *ManufacturerAnalyzer) AnalyzeRecallTrend(ctx context.Context, firm string, periodDays int) (*Signal, error) {
	if periodDays < 1 {
		return nil, fmt.Errorf("firm-trend: periodDays must be >= 1 (got %d)", periodDays)
	}
	now := timeNow().UTC()
	mid := now.AddDate(0, 0, -periodDays+1) // inclusive [mid, now] = periodDays days
	old := mid.AddDate(0, 0, -periodDays)
	src := []string{"openfda_recall"}

	recent, err := a.data.FirmRecallTotalWindow(ctx, firm, mid.Format(day), now.Format(day))
	if err != nil {
		return nil, fmt.Errorf("firm-trend: recent: %w", err)
	}
	prior, err := a.data.FirmRecallTotalWindow(ctx, firm, old.Format(day), mid.AddDate(0, 0, -1).Format(day))
	if err != nil {
		return nil, fmt.Errorf("firm-trend: prior: %w", err)
	}
	switch {
	case recent == 0 && prior == 0:
		return noData(SignalFirmRecallTrend, fmt.Sprintf("no recalls for this firm in either %d-day window", periodDays), src), nil
	case prior == 0:
		return &Signal{
			SignalType:      SignalFirmRecallTrend,
			Value:           1.0,
			Label:           labelFor(1.0),
			Reasoning:       fmt.Sprintf("new activity: %d recalls in the last %d days with none in the prior %d days (no baseline to scale against)", recent, periodDays, periodDays),
			ConfidenceLevel: ConfidenceLow,
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
		SignalType: SignalFirmRecallTrend,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%s: %d recalls in the last %d days vs %d in the prior %d days (%+.0f%%; +100%% saturates at 1.0)",
			direction, recent, periodDays, prior, periodDays, slope*100),
		ConfidenceLevel: confidenceForSample(recent + prior),
		SourceType:      src,
	}, nil
}

// AnalyzeOpenRecalls reads the firm's recall status mix: the share of all
// recalls still marked Ongoing — the firm's open enforcement load.
func (a *ManufacturerAnalyzer) AnalyzeOpenRecalls(ctx context.Context, firm string) (*Signal, error) {
	counts, err := a.data.FirmRecallStatusCounts(ctx, firm)
	if err != nil {
		return nil, fmt.Errorf("firm-open: %w", err)
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	src := []string{"openfda_recall"}
	if total == 0 {
		return noData(SignalFirmOpenRecalls, "no recalls on record for this firm name (exact-phrase match; subsidiaries register separately)", src), nil
	}
	ongoing := counts["Ongoing"]
	value := float64(ongoing) / float64(total)
	return &Signal{
		SignalType: SignalFirmOpenRecalls,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%d of %d recalls are still Ongoing (%d completed/terminated); an open recall is unresolved enforcement workload, not a harm reading",
			ongoing, total, total-ongoing),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, nil
}
