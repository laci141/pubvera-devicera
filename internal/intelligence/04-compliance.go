package intelligence

import (
	"context"
	"fmt"
	"sort"
)

// ComplianceAnalyzer is Module 04: the FDA enforcement standing of a device
// term. Coverage note, stated in every result: the wired feed is the openFDA
// device-enforcement API, which carries RECALLS. Warning letters and
// approval/clearance dates are not in any wired source yet, so the status and
// timeline are recall-based — absence of warnings here is absence of DATA,
// not a clean bill.
type ComplianceAnalyzer struct {
	data Data
}

// Compliance statuses, ordered by seriousness.
const (
	StatusOK       = "OK"       // no recalls on record
	StatusWarning  = "WARNING"  // Class III recalls only (harm unlikely)
	StatusRecall   = "RECALL"   // Class II or a Class I recall on record
	StatusCritical = "CRITICAL" // repeated Class I recalls, or Class I amid many
)

// ComplianceStatus is the summarized enforcement standing.
type ComplianceStatus struct {
	Status          string   `json:"status"` // OK | WARNING | RECALL | CRITICAL
	Actions         []string `json:"actions"`
	LastAction      string   `json:"last_action"` // YYYYMMDD of the newest recall
	Severity        float64  `json:"severity"`    // 0-1, weighted recall-class mix
	Reasoning       string   `json:"reasoning"`
	ConfidenceLevel string   `json:"confidence_level"`
	SourceType      []string `json:"source_type"`
}

// timelineFetch is how many enforcement records back the action lists.
const timelineFetch = 25

// classifyStatus buckets the recall-class counts. The thresholds are stated
// in the reasoning: CRITICAL = 3+ Class I recalls (or any Class I among 20+
// total), RECALL = any Class I/II, WARNING = Class III only, OK = none.
func classifyStatus(c1, c2, c3 int) string {
	total := c1 + c2 + c3
	switch {
	case c1 >= 3 || (c1 >= 1 && total >= 20):
		return StatusCritical
	case c1 >= 1 || c2 >= 1:
		return StatusRecall
	case c3 >= 1:
		return StatusWarning
	default:
		return StatusOK
	}
}

// CheckFDAStatus summarizes the enforcement record: status bucket, weighted
// severity, the newest action date, and formatted recent actions.
func (a *ComplianceAnalyzer) CheckFDAStatus(ctx context.Context, device string) (*ComplianceStatus, error) {
	counts, err := a.data.RecallClassCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("fda-status: classes: %w", err)
	}
	c1, c2, c3 := counts["Class I"], counts["Class II"], counts["Class III"]
	total := c1 + c2 + c3
	src := []string{"openfda_recall"}

	if total == 0 {
		return &ComplianceStatus{
			Status:          StatusOK,
			Actions:         nil,
			Severity:        0,
			Reasoning:       "OK (Devicera rule over FDA recall records — not an FDA status or determination): 0 Class I, 0 Class II, 0 Class III recall records in the openFDA enforcement record for this device term; warning letters and approvals are not in the wired feeds, and absence of records is not evidence of safety",
			ConfidenceLevel: ConfidenceLow,
			SourceType:      src,
		}, nil
	}

	actions, err := a.data.RecallActions(ctx, device, timelineFetch)
	if err != nil {
		return nil, fmt.Errorf("fda-status: actions: %w", err)
	}
	formatted := make([]string, 0, len(actions))
	last := ""
	for _, act := range actions {
		formatted = append(formatted, fmt.Sprintf("%s: %s — %s [%s]", act.Date, act.Type, act.Description, act.Reference))
		if act.Date > last {
			last = act.Date
		}
	}

	severity := (float64(c1)*weightRecallClass1 + float64(c2)*weightRecallClass2 +
		float64(c3)*weightRecallClass3) / float64(total)
	status := classifyStatus(c1, c2, c3)
	return &ComplianceStatus{
		Status:     status,
		Actions:    formatted,
		LastAction: last,
		Severity:   round2(severity),
		Reasoning: fmt.Sprintf(
			"%s (Devicera rule over FDA recall records — not an FDA status or determination): %d recall records (%d Class I, %d Class II, %d Class III); severity is the weighted class mix (I=1.0, II=0.5, III=0.2); rule: CRITICAL=3+ Class I recall records or any Class I among 20+ total, RECALL=any Class I/II recall record, WARNING=Class III recall records only, OK=none; recall feed only — warning letters/approvals not wired",
			status, total, c1, c2, c3),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, nil
}

// GetRegulatoryTimeline returns the dated enforcement actions oldest-first.
// Approvals/clearances would open the timeline once a 510(k)/PMA source is
// wired; today it is the recall history.
func (a *ComplianceAnalyzer) GetRegulatoryTimeline(ctx context.Context, device string) ([]ComplianceAction, error) {
	actions, err := a.data.RecallActions(ctx, device, timelineFetch)
	if err != nil {
		return nil, fmt.Errorf("timeline: %w", err)
	}
	// Chronological ascending; undated actions sort last, stably.
	sort.SliceStable(actions, func(i, j int) bool {
		if actions[i].Date == "" {
			return false
		}
		if actions[j].Date == "" {
			return true
		}
		return actions[i].Date < actions[j].Date
	})
	return actions, nil
}
