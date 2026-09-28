package intelligence

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ReportingAnalyzer is Module 11: the quality and provenance of the MAUDE
// record itself — who reports, how complete the reports are, and how
// concentrated the reporting manufacturers are. These are readings about the
// DATA, not the device: a poorly-documented record weakens every other
// module's confidence, and that is exactly what this module surfaces.
type ReportingAnalyzer struct {
	data Data
}

// Signal type names for Module 11.
const (
	SignalIndependentReporting = "INDEPENDENT_REPORTING"
	SignalMissingEventDates    = "MISSING_EVENT_DATES"
	SignalMakerConcentration   = "MAKER_CONCENTRATION"
)

// bucketReporter normalizes MAUDE's messy source_type vocabulary (duplicate
// casings like "Company representation" vs "COMPANY REPRESENTATIVE") into
// company / independent / other.
func bucketReporter(term string) string {
	low := strings.ToLower(term)
	switch {
	case strings.Contains(low, "company") || strings.Contains(low, "manufacturer") || strings.Contains(low, "distributor"):
		return "company"
	case strings.Contains(low, "health professional") || strings.Contains(low, "consumer") ||
		strings.Contains(low, "patient") || strings.Contains(low, "user facility"):
		return "independent"
	default:
		return "other"
	}
}

// AnalyzeIndependentReporting reads the share of reports filed by
// independent reporters (health professionals, patients, user facilities)
// rather than the companies themselves. Independent reports corroborate; a
// company-dominated record is mostly mandatory self-reporting.
func (a *ReportingAnalyzer) AnalyzeIndependentReporting(ctx context.Context, device string) (*Signal, error) {
	counts, err := a.data.ReporterSourceCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("reporter-mix: %w", err)
	}
	src := []string{"openfda_maude"}
	buckets := map[string]int{}
	total := 0
	for term, n := range counts {
		buckets[bucketReporter(term)] += n
		total += n
	}
	if total == 0 {
		return noData(SignalIndependentReporting, "no reporter source types recorded in MAUDE for this device term", src), nil
	}
	value := float64(buckets["independent"]) / float64(total)
	return &Signal{
		SignalType: SignalIndependentReporting,
		Value:      round2(value),
		Label:      labelForDirection(directionQuality, value),
		Reasoning: fmt.Sprintf(
			"%d of %d source-typed reports come from independent reporters (health professionals, patients, user facilities) vs %d company-filed and %d other; MAUDE casing variants merged; a reading about data provenance, not the device",
			buckets["independent"], total, buckets["company"], buckets["other"]),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, nil
}

// AnalyzeMissingEventDates reads the share of the device's reports with no
// date_of_event — a documentation-completeness gap that weakens every
// time-based reading (trend, surge, lifecycle).
func (a *ReportingAnalyzer) AnalyzeMissingEventDates(ctx context.Context, device string) (*Signal, error) {
	counts, err := a.data.EventTypeCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("missing-dates: total: %w", err)
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	src := []string{"openfda_maude"}
	if total == 0 {
		return noData(SignalMissingEventDates, "no MAUDE reports found for this device term", src), nil
	}
	missing, err := a.data.EventTotalMissing(ctx, device, "date_of_event")
	if err != nil {
		return nil, fmt.Errorf("missing-dates: missing: %w", err)
	}
	value := float64(missing) / float64(total)
	if value > 1 {
		value = 1
	}
	return &Signal{
		SignalType: SignalMissingEventDates,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%d of %d reports have no date_of_event (%.0f%%); a completeness gap that weakens time-based readings (trend, surge, lifecycle) — about the data, not the device",
			missing, total, value*100),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, nil
}

// AnalyzeMakerConcentration reads how concentrated the reporting
// manufacturers are behind a device term: the Herfindahl index over
// device.manufacturer_d_name. A term dominated by one maker means the
// term-level readings are really that maker's product line.
func (a *ReportingAnalyzer) AnalyzeMakerConcentration(ctx context.Context, device string) (*Signal, error) {
	counts, err := a.data.ManufacturerNameCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("maker-concentration: %w", err)
	}
	src := []string{"openfda_maude"}
	// Merge casing variants of the same maker name.
	merged := map[string]int{}
	total := 0
	for name, n := range counts {
		key := strings.ToUpper(strings.TrimSpace(name))
		merged[key] += n
		total += n
	}
	if total == 0 {
		return noData(SignalMakerConcentration, "no manufacturer names recorded in MAUDE for this device term", src), nil
	}
	hhi := 0.0
	type maker struct {
		name string
		n    int
	}
	makers := make([]maker, 0, len(merged))
	for name, n := range merged {
		share := float64(n) / float64(total)
		hhi += share * share
		makers = append(makers, maker{name, n})
	}
	sort.Slice(makers, func(i, j int) bool {
		if makers[i].n != makers[j].n {
			return makers[i].n > makers[j].n
		}
		return makers[i].name < makers[j].name
	})
	top := makers
	if len(top) > 3 {
		top = top[:3]
	}
	names := make([]string, len(top))
	for i, m := range top {
		names[i] = fmt.Sprintf("%s (%.0f%%)", m.name, float64(m.n)/float64(total)*100)
	}
	return &Signal{
		SignalType: SignalMakerConcentration,
		Value:      round2(hhi),
		Label:      labelFor(hhi),
		Reasoning: fmt.Sprintf(
			"concentration %.2f across the top %d reporting manufacturers (sum of squared shares; %d name-attributed reports in the counted head, not the full distribution; casing merged); top: %s; high concentration means term-level readings describe one maker's product line",
			hhi, len(merged), total, strings.Join(names, ", ")),
		ConfidenceLevel: confidenceForSample(total),
		SourceType:      src,
	}, nil
}
