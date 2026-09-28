package intelligence

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// BenchmarkAnalyzer is Module 06: peer readings — the device against the
// MAUDE/enforcement population rather than against itself. Three signals:
//
//   - EventRank: the device's event volume as a percentile among the 100
//     most-reported device types.
//   - SeverityDelta: the device's weighted severity vs the global mix.
//   - RecallRate: recalls per 1000 MAUDE events vs the global rate.
//
// A benchmark says where a device sits in the reporting population; ubiquity
// and reporting practices drive position at least as much as device behavior.
type BenchmarkAnalyzer struct {
	data Data
}

// Signal type names for Module 06.
const (
	SignalPeerEventRank     = "PEER_EVENT_RANK"
	SignalPeerSeverityDelta = "PEER_SEVERITY_DELTA"
	SignalPeerRecallRate    = "PEER_RECALL_RATE"
)

// weightedSeverity is the shared severity formula (Module 01 weights).
func weightedSeverity(counts map[string]int) (float64, int) {
	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return 0, 0
	}
	v := (float64(counts["Death"])*weightDeath + float64(counts["Injury"])*weightInjury +
		float64(counts["Malfunction"])*weightMalfunction) / float64(total)
	return v, total
}

// AnalyzeEventRank places the device's MAUDE volume as a percentile within
// the top-100 most-reported device types. Devices below the whole top slice
// rank 0 — "below the 100 most-reported types", stated in the reasoning.
func (a *BenchmarkAnalyzer) AnalyzeEventRank(ctx context.Context, device string) (*Signal, error) {
	counts, err := a.data.EventTypeCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("event-rank: device: %w", err)
	}
	deviceVol := 0
	for _, n := range counts {
		deviceVol += n
	}
	src := []string{"openfda_maude"}
	if deviceVol == 0 {
		return noData(SignalPeerEventRank, "no MAUDE reports found for this device term", src), nil
	}
	peers, err := a.data.DeviceTypeVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("event-rank: peers: %w", err)
	}
	if len(peers) == 0 {
		return noData(SignalPeerEventRank, "peer volume distribution unavailable", src), nil
	}
	vols := make([]int, 0, len(peers))
	for _, n := range peers {
		vols = append(vols, n)
	}
	sort.Ints(vols)
	below := sort.SearchInts(vols, deviceVol) // peers strictly below deviceVol
	value := float64(below) / float64(len(vols))
	return &Signal{
		SignalType: SignalPeerEventRank,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%d MAUDE reports puts this term above %d of the %d most-reported device types (percentile within that top slice); volume tracks ubiquity and reporting practices, not risk",
			deviceVol, below, len(vols)),
		ConfidenceLevel: confidenceForSample(deviceVol),
		SourceType:      src,
	}, nil
}

// severityDeltaScale: sitting 0.3 above the global weighted severity
// saturates the signal at 1.0.
const severityDeltaScale = 0.3

// AnalyzeSeverityDelta compares the device's weighted severity mix against
// the global MAUDE mix. Only sitting ABOVE the global mix raises the value.
func (a *BenchmarkAnalyzer) AnalyzeSeverityDelta(ctx context.Context, device string) (*Signal, error) {
	counts, err := a.data.EventTypeCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("severity-delta: device: %w", err)
	}
	deviceSev, deviceTotal := weightedSeverity(counts)
	src := []string{"openfda_maude"}
	if deviceTotal == 0 {
		return noData(SignalPeerSeverityDelta, "no MAUDE reports found for this device term", src), nil
	}
	global, err := a.data.GlobalEventTypeCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("severity-delta: global: %w", err)
	}
	globalSev, globalTotal := weightedSeverity(global)
	if globalTotal == 0 {
		return noData(SignalPeerSeverityDelta, "global severity mix unavailable", src), nil
	}
	delta := deviceSev - globalSev
	value := math.Min(1.0, math.Max(0, delta/severityDeltaScale))
	direction := "at"
	if delta > 0.02 {
		direction = "above"
	} else if delta < -0.02 {
		direction = "below"
	}
	return withSample(&Signal{
		SignalType: SignalPeerSeverityDelta,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"device severity %.2f is %s the global MAUDE mix %.2f (delta %+.2f; +0.30 saturates at 1.0); severity weights: death=1.0, injury=0.6, malfunction=0.3",
			deviceSev, direction, globalSev, delta),
		ConfidenceLevel: confidenceForSample(deviceTotal),
		SourceType:      src,
	}, deviceTotal, unitMAUDE), nil
}

// recallRateScale: a recall rate 4x the global rate saturates at 1.0.
const recallRateScale = 4.0

// AnalyzeRecallRate compares the device's recalls per 1000 MAUDE events
// against the global recalls-per-1000-events rate.
func (a *BenchmarkAnalyzer) AnalyzeRecallRate(ctx context.Context, device string) (*Signal, error) {
	counts, err := a.data.EventTypeCounts(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("recall-rate: events: %w", err)
	}
	events := 0
	for _, n := range counts {
		events += n
	}
	recalls, err := a.data.RecallTotal(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("recall-rate: recalls: %w", err)
	}
	src := []string{"openfda_maude", "openfda_recall"}
	if events == 0 {
		return noData(SignalPeerRecallRate, "no MAUDE reports, so a recall rate cannot be computed", src), nil
	}
	globalEvents, err := a.data.GlobalEventTypeCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("recall-rate: global events: %w", err)
	}
	gEvents := 0
	for _, n := range globalEvents {
		gEvents += n
	}
	globalRecalls, err := a.data.GlobalRecallClassCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("recall-rate: global recalls: %w", err)
	}
	gRecalls := 0
	for _, n := range globalRecalls {
		gRecalls += n
	}
	if gEvents == 0 || gRecalls == 0 {
		return noData(SignalPeerRecallRate, "global recall/event totals unavailable", src), nil
	}
	deviceRate := float64(recalls) / (float64(events) / 1000.0)
	globalRate := float64(gRecalls) / (float64(gEvents) / 1000.0)
	ratio := deviceRate / globalRate
	value := math.Min(1.0, ratio/recallRateScale)
	return &Signal{
		SignalType: SignalPeerRecallRate,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"%.2f recalls per 1000 events (%d recalls / %d events) vs global %.2f per 1000 (%.1fx; 4x saturates at 1.0); a high rate can reflect vigilant recall practice as much as device trouble",
			deviceRate, recalls, events, globalRate, ratio),
		ConfidenceLevel: confidenceForSample(events),
		SourceType:      src,
	}, nil
}
