package intelligence

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// FailureModeAnalyzer is Module 09: WHAT goes wrong, not how often. It reads
// MAUDE's product_problems distribution — the reported failure vocabulary of
// a device. The count API returns the head of the distribution (top ~100
// problem terms), so shares are of the counted head, stated in reasonings.
// MAUDE problems are reporter-selected labels: a concentration is a labeling
// pattern as much as an engineering fact.
type FailureModeAnalyzer struct {
	data Data
}

// Signal type names for Module 09.
const (
	SignalProblemConcentration = "PROBLEM_CONCENTRATION"
	SignalNewProblemModes      = "NEW_PROBLEM_MODES"
)

// ProblemShare is one ranked failure mode.
type ProblemShare struct {
	Problem string  `json:"problem"`
	Count   int     `json:"count"`
	Share   float64 `json:"share"` // of the counted distribution head
}

// TopProblems returns the device's n most-reported problem terms with their
// shares of the counted distribution.
func (a *FailureModeAnalyzer) TopProblems(ctx context.Context, device string, n int) ([]ProblemShare, error) {
	if n < 1 {
		n = 10
	}
	counts, err := a.data.ProblemCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("top-problems: %w", err)
	}
	total := 0
	for _, c := range counts {
		total += c
	}
	if total == 0 {
		return nil, nil
	}
	out := make([]ProblemShare, 0, len(counts))
	for p, c := range counts {
		out = append(out, ProblemShare{Problem: p, Count: c, Share: round2(float64(c) / float64(total))})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Problem < out[j].Problem
	})
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// AnalyzeProblemConcentration reads how dominated the failure vocabulary is
// by few modes: the Herfindahl index (sum of squared shares) of the problem
// distribution. 1.0 = a single failure mode; near 0 = diffuse.
func (a *FailureModeAnalyzer) AnalyzeProblemConcentration(ctx context.Context, device string) (*Signal, error) {
	counts, err := a.data.ProblemCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("concentration: %w", err)
	}
	total := 0
	for _, c := range counts {
		total += c
	}
	src := []string{"openfda_maude"}
	if total == 0 {
		return noData(SignalProblemConcentration, "no product_problems recorded in MAUDE for this device term", src), nil
	}
	hhi := 0.0
	for _, c := range counts {
		share := float64(c) / float64(total)
		hhi += share * share
	}
	top, _ := a.TopProblems(ctx, device, 3)
	names := make([]string, 0, len(top))
	for _, p := range top {
		names = append(names, fmt.Sprintf("%s (%.0f%%)", p.Problem, p.Share*100))
	}
	return &Signal{
		SignalType: SignalProblemConcentration,
		Value:      round2(hhi),
		Label:      labelFor(hhi),
		Reasoning: fmt.Sprintf(
			"Herfindahl %.2f across %d problem terms (%d reports in the counted head); top: %s; problems are reporter-selected labels — concentration is a labeling pattern as much as an engineering fact",
			hhi, len(counts), total, strings.Join(names, ", ")),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, nil
}

// AnalyzeNewProblemModes finds problem terms reported in the last recentDays
// that never appeared before that window — a new way of failing. Value is
// the share of recent problem reports carrying an unseen term.
func (a *FailureModeAnalyzer) AnalyzeNewProblemModes(ctx context.Context, device string, recentDays int) (*Signal, error) {
	if recentDays < 1 {
		return nil, fmt.Errorf("new-modes: recentDays must be >= 1 (got %d)", recentDays)
	}
	now := timeNow().UTC()
	mid := now.AddDate(0, 0, -recentDays+1) // inclusive [mid, now] = recentDays days
	src := []string{"openfda_maude"}

	recent, err := a.data.ProblemCountsWindow(ctx, device, mid.Format(day), now.Format(day))
	if err != nil {
		return nil, fmt.Errorf("new-modes: recent: %w", err)
	}
	history, err := a.data.ProblemCountsWindow(ctx, device, "19900101", mid.AddDate(0, 0, -1).Format(day))
	if err != nil {
		return nil, fmt.Errorf("new-modes: history: %w", err)
	}
	recentTotal := 0
	for _, c := range recent {
		recentTotal += c
	}
	if recentTotal == 0 {
		return noData(SignalNewProblemModes, fmt.Sprintf("no problem-coded MAUDE reports in the last %d days", recentDays), src), nil
	}
	var newModes []string
	newCount := 0
	for p, c := range recent {
		if history[p] == 0 {
			newModes = append(newModes, p)
			newCount += c
		}
	}
	if len(newModes) == 0 {
		return &Signal{
			SignalType:      SignalNewProblemModes,
			Value:           0,
			Label:           LabelLow,
			Reasoning:       fmt.Sprintf("all %d recent problem reports use failure modes already in the history — no new way of failing; history is the counted head (~top 100 terms), so a rare old mode can look new", recentTotal),
			ConfidenceLevel: confidenceForSample(recentTotal),
			SourceType:      src,
		}, nil
	}
	sort.Strings(newModes)
	shown := newModes
	if len(shown) > 5 {
		shown = shown[:5]
	}
	value := float64(newCount) / float64(recentTotal)
	return &Signal{
		SignalType: SignalNewProblemModes,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%d of %d recent problem reports carry modes unseen before this window (%d new terms, e.g. %s); history is the counted head (~top 100 terms), so a rare old mode can look new",
			newCount, recentTotal, len(newModes), strings.Join(shown, ", ")),
		ConfidenceLevel: confidenceForSample(recentTotal),
		SourceType:      src,
	}, nil
}
