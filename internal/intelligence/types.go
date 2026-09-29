// Package intelligence holds the hand-built analysis modules (Phase 2c).
// Every module emits Signals: explainable, normalized, source-cited readings
// of the public record — NEVER a "risk score". A Signal always says what it
// measured, why it got its value, and how much data stands behind it.
package intelligence

import "context"

// Signal is one explainable reading. Guardrails: Value is normalized 0.0-1.0,
// Reasoning is plain English, the Sample fields say how many records were
// counted (not whether the reading is right), and SourceType names the APIs
// the reading came from.
type Signal struct {
	SignalType string  `json:"signal_type"` // SEVERITY | VOLUME | TREND
	Value      float64 `json:"value"`       // 0.0-1.0
	Label      string  `json:"label"`       // one word from the signal's direction (see labelForDirection) or Unknown
	Reasoning  string  `json:"reasoning"`
	// Deprecated: ConfidenceLevel is only a sample-size band (HIGH | MEDIUM |
	// LOW), kept for existing clients. Read SampleBand/SampleSize instead.
	ConfidenceLevel string   `json:"confidence_level"`
	SampleSize      int      `json:"sample_size"` // how many SampleUnit were counted
	SampleUnit      string   `json:"sample_unit"` // what was counted, e.g. "MAUDE reports"
	SampleBand      string   `json:"sample_band"` // large | medium | small | none
	SourceType      []string `json:"source_type"`
}

// Signal type names.
const (
	SignalSeverity = "SEVERITY"
	SignalVolume   = "VOLUME"
	SignalTrend    = "TREND"
	// Module 02 (anomaly — trend-breaking readings):
	SignalSurge       = "SURGE"        // short-window spike vs baseline rate
	SignalNewPattern  = "NEW_PATTERN"  // event types absent from the history
	SignalVolumeShift = "VOLUME_SHIFT" // sustained level change vs prior periods
)

// Labels, bucketed from Value by labelFor (concern words) or
// labelForDirection (activity and quality words).
const (
	LabelCritical = "Critical"
	LabelHigh     = "High"
	LabelMedium   = "Medium"
	LabelLow      = "Low"
	LabelUnknown  = "Unknown" // no data — distinct from a true Low reading

	// Activity words: how much public-record activity, not how much concern.
	// Distinct from the concern words so the page can colour by word alone.
	LabelQuiet    = "Quiet"
	LabelModerate = "Moderate"
	LabelBusy     = "Busy"
	LabelTop      = "Top"

	// Quality words: higher is better.
	LabelPoor   = "Poor"
	LabelFair   = "Fair"
	LabelGood   = "Good"
	LabelStrong = "Strong"
)

// direction is which way a signal's Value points, and so which words name
// its buckets. The concern words read as a hazard scale, which misleads for
// a signal whose high value only means more activity, or means better data.
type direction int

const (
	directionConcern  direction = iota // higher = more concern: Low/Medium/High/Critical
	directionActivity                  // higher = more activity: Quiet/Moderate/Busy/Top
	directionQuality                   // higher = better: Poor/Fair/Good/Strong
)

// directionWords names each direction's buckets, lowest first.
var directionWords = map[direction][4]string{
	directionConcern:  {LabelLow, LabelMedium, LabelHigh, LabelCritical},
	directionActivity: {LabelQuiet, LabelModerate, LabelBusy, LabelTop},
	directionQuality:  {LabelPoor, LabelFair, LabelGood, LabelStrong},
}

// labelForDirection buckets a normalized value on labelFor's thresholds
// (>0.7, >0.5, >0.3) and names the bucket in d's words.
func labelForDirection(d direction, v float64) string {
	w := directionWords[d]
	switch {
	case v > 0.7:
		return w[3]
	case v > 0.5:
		return w[2]
	case v > 0.3:
		return w[1]
	default:
		return w[0]
	}
}

// Confidence levels, from sample size.
const (
	ConfidenceHigh   = "HIGH"
	ConfidenceMedium = "MEDIUM"
	ConfidenceLow    = "LOW"
)

// Sample bands: how many records a reading counted, on confidenceForSample's
// cut-offs. A band is not a reliability grade.
const (
	SampleLarge  = "large"
	SampleMedium = "medium"
	SampleSmall  = "small"
	SampleNone   = "none" // nothing counted, or no baseline to scale against
)

// unitMAUDE is the sample unit of every reading that counts MAUDE reports.
const unitMAUDE = "MAUDE reports"

// sampleBandFor names a ConfidenceLevel in sample words, so the deprecated
// field and SampleBand never disagree.
func sampleBandFor(conf string) string {
	switch conf {
	case ConfidenceHigh:
		return SampleLarge
	case ConfidenceMedium:
		return SampleMedium
	default:
		return SampleSmall
	}
}

// withSample records what a reading counted: n records of unit, banded by
// the reading's ConfidenceLevel.
func withSample(s *Signal, n int, unit string) *Signal {
	s.SampleSize = n
	s.SampleUnit = unit
	s.SampleBand = sampleBandFor(s.ConfidenceLevel)
	return s
}

// labelFor buckets a normalized value: >0.7 Critical, >0.5 High, >0.3 Medium,
// else Low (guardrail 6). These are the concern words; activity and quality
// signals use labelForDirection instead.
func labelFor(v float64) string { return labelForDirection(directionConcern, v) }

// confidenceForSample maps a sample size to a confidence level: 200+ records
// HIGH, 50+ MEDIUM, otherwise LOW.
func confidenceForSample(n int) string {
	switch {
	case n >= 200:
		return ConfidenceHigh
	case n >= 50:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

// Data is the read-only view the analyzers consume (guardrail 8: no
// mutation). The live implementation reads the registered sources; tests
// supply mocks.
type Data interface {
	// EventTypeCounts is the MAUDE event_type distribution for a device term.
	EventTypeCounts(ctx context.Context, device string) (map[string]int, error)
	// RecallTotal is the server-side openFDA enforcement total for a device term.
	RecallTotal(ctx context.Context, device string) (int, error)
	// EventTotalWindow is the MAUDE total for a device term within
	// [from, to] compact YYYYMMDD dates.
	EventTotalWindow(ctx context.Context, device, from, to string) (int, error)
	// VolumeBaseline returns the p95 event volume across the most-reported
	// device types and the number of device types sampled.
	VolumeBaseline(ctx context.Context) (p95 int, sample int, err error)
	// EventTypeCountsWindow is EventTypeCounts restricted to [from, to]
	// compact YYYYMMDD dates (Module 02 needs windowed distributions).
	EventTypeCountsWindow(ctx context.Context, device, from, to string) (map[string]int, error)
	// RecallClassCounts is the openFDA enforcement classification distribution
	// for a device term (Class I/II/III → counts).
	RecallClassCounts(ctx context.Context, device string) (map[string]int, error)
	// TrialTotal is the ClinicalTrials.gov device-intervention study total.
	TrialTotal(ctx context.Context, device string) (int, error)
	// PublicationTotal is the PubMed Title/Abstract match total.
	PublicationTotal(ctx context.Context, device string) (int, error)
	// RecallActions returns individual enforcement records as timeline
	// actions (Module 08 recall recency), newest first: the live read requests
	// sort=recall_initiation_date:desc (openFDA's default order is not by date).
	RecallActions(ctx context.Context, device string, limit int) ([]ComplianceAction, error)
	// Firm-level enforcement reads (Module 05), filtered by recalling_firm.
	FirmRecallClassCounts(ctx context.Context, firm string) (map[string]int, error)
	FirmRecallStatusCounts(ctx context.Context, firm string) (map[string]int, error)
	FirmRecallTotalWindow(ctx context.Context, firm, from, to string) (int, error)
	// Population-level reads (Module 06 benchmarks).
	DeviceTypeVolumes(ctx context.Context) (map[string]int, error)
	GlobalEventTypeCounts(ctx context.Context) (map[string]int, error)
	GlobalRecallClassCounts(ctx context.Context) (map[string]int, error)
	// ClinicalTrials.gov reads (Module 07 clustering): the MeSH condition
	// terms attached to a device's trials, and the device interventions
	// studied under a condition.
	DeviceMeshTerms(ctx context.Context, device string, maxTrials int) ([]string, error)
	DevicesForCondition(ctx context.Context, meshTerm string, limit int) ([]string, error)
	// MAUDE product_problems distributions (Module 09 failure modes). The
	// count API returns the top ~100 problem terms — the head, not the tail.
	ProblemCounts(ctx context.Context, device string) (map[string]int, error)
	ProblemCountsWindow(ctx context.Context, device, from, to string) (map[string]int, error)
	// Research-pipeline reads (Module 10): PubMed totals within a
	// publication-year range, and CT.gov totals restricted to trial statuses.
	PublicationCountWindow(ctx context.Context, device string, fromYear, toYear int) (int, error)
	TrialStatusTotal(ctx context.Context, device string, statuses []string) (int, error)
	// Reporting-quality reads (Module 11): who reports and how completely.
	ReporterSourceCounts(ctx context.Context, device string) (map[string]int, error)
	EventTotalMissing(ctx context.Context, device, field string) (int, error)
	ManufacturerNameCounts(ctx context.Context, device string) (map[string]int, error)
}

// ComplianceAction is one dated regulatory action on the timeline.
type ComplianceAction struct {
	Date        string `json:"date"` // YYYYMMDD
	Type        string `json:"type"` // e.g. "recall (Class I)"
	Description string `json:"description"`
	Reference   string `json:"reference"` // the agency record id (recall_number)
}
