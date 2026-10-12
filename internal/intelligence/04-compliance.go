package intelligence

import (
	"context"
	"fmt"
)

// ComplianceAnalyzer is Module 04: how the device's recalls read in the
// regulatory record. Two shares, both concern readings: how many recalls trace
// to a design-type root cause, and how many enforcement records are still open.
type ComplianceAnalyzer struct {
	data Data
}

// Signal type names for Module 04.
const (
	SignalRootCauseDesign = "ROOT_CAUSE_DESIGN"
	SignalOpenRecallShare = "OPEN_RECALL_SHARE"
)

// designRootCauses are the openFDA root_cause_description terms counted as
// design-type, chosen from the real "pacemaker" count (20 distinct terms,
// 282 recalls): the cause is how the device, its software or a component was
// designed. Process design (a manufacturing process) and Software
// Manufacturing/Software Deployment (production, not design) are excluded.
var designRootCauses = map[string]bool{
	"Device Design":              true,
	"Software design":            true,
	"Component design/selection": true,
}

// designRootCauseList is designRootCauses in the order the reasoning states it.
const designRootCauseList = "Device Design, Software design, Component design/selection"

// AnalyzeRootCauseDesign reads the share of the device's recalls whose root
// cause is design-type (one openFDA recall count call).
func (a *ComplianceAnalyzer) AnalyzeRootCauseDesign(ctx context.Context, device string) (*Signal, error) {
	counts, err := a.data.RecallRootCauseCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("compliance-design: %w", err)
	}
	total, design := 0, 0
	for term, n := range counts {
		total += n
		if designRootCauses[term] {
			design += n
		}
	}
	src := []string{"openfda_device_recall"}
	if total == 0 {
		return noData(SignalRootCauseDesign, "no recalls with a root cause on record for this device term", src), nil
	}
	value := float64(design) / float64(total)
	return withSample(&Signal{
		SignalType: SignalRootCauseDesign,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%d of %d recalls have a design-type root cause (%s); the share says where firms place the cause, not whether the device is unsafe",
			design, total, designRootCauseList),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, total, "recalls"), nil
}

// AnalyzeOpenRecallShare reads the share of the device's enforcement records
// still marked Ongoing (one openFDA enforcement count call).
func (a *ComplianceAnalyzer) AnalyzeOpenRecallShare(ctx context.Context, device string) (*Signal, error) {
	counts, err := a.data.RecallStatusCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("compliance-open: %w", err)
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	src := []string{"openfda_recall"}
	if total == 0 {
		return noData(SignalOpenRecallShare, "no enforcement records on record for this device term", src), nil
	}
	ongoing := counts["Ongoing"]
	value := float64(ongoing) / float64(total)
	return withSample(&Signal{
		SignalType: SignalOpenRecallShare,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%d of %d enforcement records are still Ongoing; an open recall is unresolved enforcement workload, not a harm reading",
			ongoing, total),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, total, "enforcement records"), nil
}
