package intelligence

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

// CorrelationAnalyzer is Module 03: cross-source readings. Where Modules
// 01-02 read one feed (MAUDE), this module asks how the sources relate:
//
//   - RecallSeverity: the FDA recall-class mix (Class I is the most serious).
//   - Corroboration: how many independent public sources hold records at all.
//   - EvidenceGap: heavy adverse-event volume with thin clinical literature.
//
// Same guardrails: normalized values with the scaling stated in the
// reasoning, confidence from sample size, graceful Unknown, never a verdict.
type CorrelationAnalyzer struct {
	data Data
}

// Signal type names for Module 03.
const (
	SignalRecallSeverity = "RECALL_SEVERITY"
	SignalCorroboration  = "CORROBORATION"
	SignalEvidenceGap    = "EVIDENCE_GAP"
)

// Recall class weights: FDA Class I = reasonable probability of serious harm
// or death, Class II = temporary/reversible harm, Class III = unlikely harm.
const (
	weightRecallClass1 = 1.0
	weightRecallClass2 = 0.5
	weightRecallClass3 = 0.2
)

// AnalyzeRecallSeverity reads the recall classification mix. Value is the
// weighted mean recall class (Class I=1.0, II=0.5, III=0.2), the recall
// counterpart of Module 01's MAUDE severity.
func (a *CorrelationAnalyzer) AnalyzeRecallSeverity(ctx context.Context, device string) (*Signal, error) {
	counts, err := a.data.RecallClassCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("recall-severity: %w", err)
	}
	c1, c2, c3 := counts["Class I"], counts["Class II"], counts["Class III"]
	total := c1 + c2 + c3
	src := []string{"openfda_recall"}
	if total == 0 {
		return noData(SignalRecallSeverity, "no classified recalls found for this device term", src), nil
	}
	value := (float64(c1)*weightRecallClass1 + float64(c2)*weightRecallClass2 +
		float64(c3)*weightRecallClass3) / float64(total)
	return &Signal{
		SignalType: SignalRecallSeverity,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%d recalls: %d Class I, %d Class II, %d Class III; weighted mean class (I=1.0, II=0.5, III=0.2)",
			total, c1, c2, c3),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, nil
}

// AnalyzeCorroboration counts how many of the four independent public feeds
// (MAUDE, recalls, trials, publications) hold at least one record for the
// term. Value = feeds with data / 4. High corroboration means the device is
// widely documented — a well-populated record, not a hazard reading.
func (a *CorrelationAnalyzer) AnalyzeCorroboration(ctx context.Context, device string) (*Signal, error) {
	events, err := a.data.EventTypeCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("corroboration: events: %w", err)
	}
	eventTotal := 0
	for _, n := range events {
		eventTotal += n
	}
	recalls, err := a.data.RecallTotal(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("corroboration: recalls: %w", err)
	}
	trials, err := a.data.TrialTotal(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("corroboration: trials: %w", err)
	}
	pubs, err := a.data.PublicationTotal(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("corroboration: publications: %w", err)
	}

	src := []string{"openfda_maude", "openfda_recall", "clinicaltrials", "pubmed"}
	feeds := map[string]int{
		"MAUDE events": eventTotal, "recalls": recalls,
		"device trials": trials, "publications": pubs,
	}
	var with, without []string
	for name, n := range feeds {
		if n > 0 {
			with = append(with, fmt.Sprintf("%s (%d)", name, n))
		} else {
			without = append(without, name)
		}
	}
	sort.Strings(with)
	sort.Strings(without)
	if len(with) == 0 {
		return noData(SignalCorroboration, "no records in any of the four public feeds", src), nil
	}
	value := float64(len(with)) / 4
	reasoning := fmt.Sprintf("%d of 4 public feeds hold records: %s", len(with), strings.Join(with, ", "))
	if len(without) > 0 {
		reasoning += "; empty: " + strings.Join(without, ", ")
	}
	return &Signal{
		SignalType:      SignalCorroboration,
		Value:           round2(value),
		Label:           labelFor(value),
		Reasoning:       reasoning + " — corroboration measures documentation breadth, not hazard",
		ConfidenceLevel: confidenceForSample(eventTotal + recalls + trials + pubs),
		SourceType:      src,
	}, nil
}

// evidenceGapSaturation: 50+ evidence records (trials+publications) per 1000
// adverse events reads as no gap; 0 evidence with any adverse volume reads 1.0.
const evidenceGapSaturation = 50.0

// AnalyzeEvidenceGap flags heavy adverse-event volume with thin clinical
// literature. Value = 1 - min(1, evidencePer1000Events/50).
func (a *CorrelationAnalyzer) AnalyzeEvidenceGap(ctx context.Context, device string) (*Signal, error) {
	events, err := a.data.EventTypeCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("evidence-gap: events: %w", err)
	}
	eventTotal := 0
	for _, n := range events {
		eventTotal += n
	}
	trials, err := a.data.TrialTotal(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("evidence-gap: trials: %w", err)
	}
	pubs, err := a.data.PublicationTotal(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("evidence-gap: publications: %w", err)
	}
	src := []string{"openfda_maude", "clinicaltrials", "pubmed"}
	if eventTotal == 0 {
		return noData(SignalEvidenceGap, "no MAUDE reports, so there is no adverse volume to gap against", src), nil
	}
	evidence := trials + pubs
	per1000 := float64(evidence) / (float64(eventTotal) / 1000.0)

	// evidenceGapPlausibilityLimit: if per-1000 exceeds 200× the saturation
	// level the search term is almost certainly a medical condition rather than
	// a device name. Measured: stent=429 (highest device), cancer=3638663
	// (lowest disease) — an 8500× gap makes 10000 a safe threshold.
	const evidenceGapPlausibilityLimit = evidenceGapSaturation * 200

	if per1000 > evidenceGapPlausibilityLimit {
		return noData(SignalEvidenceGap, fmt.Sprintf(
			"evidence ratio implausibly high (%.0f per 1000 MAUDE events): "+
				"the search term likely describes a medical condition rather than "+
				"a device, so PubMed literature and MAUDE event counts measure "+
				"different things",
			per1000), src), nil
	}

	value := math.Max(0, 1-math.Min(1, per1000/evidenceGapSaturation))
	return &Signal{
		SignalType: SignalEvidenceGap,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%d evidence records (%d trials + %d publications) against %d MAUDE events = %.1f per 1000 events (50+/1000 reads as no gap); a gap flags thin literature, not proven harm",
			evidence, trials, pubs, eventTotal, per1000),
		ConfidenceLevel: confidenceForSample(eventTotal),
		SourceType:      src,
	}, nil
}
