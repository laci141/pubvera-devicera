package intelligence

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ClusterAnalyzer is Module 07: device neighborhoods. Devices are "similar"
// when their ClinicalTrials.gov studies share MeSH condition terms — a
// clinical-indication clustering, not a technological one. Matching similar
// device names back to MAUDE is fuzzy (trial intervention names vs MAUDE
// generic/brand names), and that caveat travels with every risk reading.
type ClusterAnalyzer struct {
	data Data
}

// SignalClusterTrend is Module 07's risk reading.
const SignalClusterTrend = "CLUSTER_TREND"

// clusterMeshTerms is how many of the device's top MeSH terms seed the search.
const clusterMeshTerms = 3

// ClusterAnalysis is the similar-device neighborhood for one device term.
type ClusterAnalysis struct {
	SimilarDevices     []string `json:"similar_devices"`
	SharedTerms        []string `json:"shared_terms"`
	ClusterSize        int      `json:"cluster_size"`
	SharedVolumeMetric float64  `json:"shared_volume_metric"` // 0-1: mean share of seed terms each member shares
	Reasoning          string   `json:"reasoning"`
}

// FindSimilarDevices builds the cluster: the device's most frequent MeSH
// condition terms, then the other device interventions studied under those
// terms. SharedVolumeMetric is the mean fraction of the seed terms each
// similar device appears under (1.0 = every member shares every seed term).
func (a *ClusterAnalyzer) FindSimilarDevices(ctx context.Context, device string, topN int) (*ClusterAnalysis, error) {
	if topN < 1 {
		topN = 5
	}
	terms, err := a.data.DeviceMeshTerms(ctx, device, 20)
	if err != nil {
		return nil, fmt.Errorf("cluster: mesh terms: %w", err)
	}
	if len(terms) == 0 {
		return &ClusterAnalysis{
			Reasoning: "no MeSH condition terms: the device has no indexed ClinicalTrials.gov studies, so no clinical-indication cluster can be built",
		}, nil
	}
	seeds := terms
	if len(seeds) > clusterMeshTerms {
		seeds = seeds[:clusterMeshTerms]
	}

	deviceLower := strings.ToLower(device)
	hits := map[string]int{} // candidate -> number of seed terms it appears under
	var order []string
	for _, term := range seeds {
		names, err := a.data.DevicesForCondition(ctx, term, 20)
		if err != nil {
			return nil, fmt.Errorf("cluster: condition %q: %w", term, err)
		}
		seenThisTerm := map[string]bool{}
		for _, name := range names {
			low := strings.ToLower(name)
			// Skip the device itself and non-informative arms.
			if strings.Contains(low, deviceLower) || low == "no intervention" {
				continue
			}
			if seenThisTerm[name] {
				continue
			}
			seenThisTerm[name] = true
			if hits[name] == 0 {
				order = append(order, name)
			}
			hits[name]++
		}
	}
	if len(order) == 0 {
		return &ClusterAnalysis{
			SharedTerms: seeds,
			Reasoning: fmt.Sprintf(
				"no other device interventions found under the seed MeSH terms (%s) — the device stands alone in its indexed indications",
				strings.Join(seeds, ", ")),
		}, nil
	}
	// Rank: most shared seed terms first, then discovery order (stable).
	sort.SliceStable(order, func(i, j int) bool { return hits[order[i]] > hits[order[j]] })
	if len(order) > topN {
		order = order[:topN]
	}
	sum := 0.0
	for _, name := range order {
		sum += float64(hits[name]) / float64(len(seeds))
	}
	metric := sum / float64(len(order))
	return &ClusterAnalysis{
		SimilarDevices:     order,
		SharedTerms:        seeds,
		ClusterSize:        len(order),
		SharedVolumeMetric: round2(metric),
		Reasoning: fmt.Sprintf(
			"%d similar device interventions share the seed MeSH terms (%s); shared-volume %.2f = mean share of seed terms per member; clustering is by clinical indication, not technology",
			len(order), strings.Join(seeds, ", "), metric),
	}, nil
}

// AnalyzeClusterRisk reads the cluster's shared MAUDE trajectory: for the
// device and each similar device, reports in the last 365 days vs the prior
// 365, then the mean growth slope across members with a baseline. Rising
// together is the flag; a lone riser is the device's own trend (Module 01).
func (a *ClusterAnalyzer) AnalyzeClusterRisk(ctx context.Context, device string) (*Signal, error) {
	cluster, err := a.FindSimilarDevices(ctx, device, 5)
	if err != nil {
		return nil, err
	}
	src := []string{"clinicaltrials", "openfda_maude"}
	if cluster.ClusterSize == 0 {
		return noData(SignalClusterTrend, cluster.Reasoning, src), nil
	}

	now := timeNow().UTC()
	mid := now.AddDate(0, 0, -365)
	old := now.AddDate(0, 0, -730)
	members := append([]string{device}, cluster.SimilarDevices...)
	rising, measured := 0, 0
	sumSlope := 0.0
	total := 0
	for _, m := range members {
		recent, err := a.data.EventTotalWindow(ctx, m, mid.Format(day), now.Format(day))
		if err != nil {
			return nil, fmt.Errorf("cluster-risk: %q recent: %w", m, err)
		}
		prior, err := a.data.EventTotalWindow(ctx, m, old.Format(day), mid.AddDate(0, 0, -1).Format(day))
		if err != nil {
			return nil, fmt.Errorf("cluster-risk: %q prior: %w", m, err)
		}
		total += recent + prior
		if prior == 0 {
			continue // no baseline for this member
		}
		slope := (float64(recent) - float64(prior)) / float64(prior)
		sumSlope += slope
		measured++
		if slope > 0.1 {
			rising++
		}
	}
	if measured == 0 {
		return noData(SignalClusterTrend,
			fmt.Sprintf("none of the %d cluster members have a MAUDE baseline (trial intervention names often don't match MAUDE device names)", len(members)), src), nil
	}
	mean := sumSlope / float64(measured)
	value := math.Min(1.0, math.Max(0, mean))
	// A cluster reading needs a cluster: with fewer than 2 measurable members
	// this is just one device's own trend, so confidence is LOW regardless of
	// how many reports back that single member.
	conf := confidenceForSample(total)
	if measured < 2 {
		conf = ConfidenceLow
	}
	return &Signal{
		SignalType: SignalClusterTrend,
		Value:      round2(value),
		Label:      labelFor(value),
		Reasoning: fmt.Sprintf(
			"cluster trend %+.0f%% mean year-over-year across %d measurable members (%d of %d rising >10%%); shared terms: %s; name matching from trials to MAUDE is fuzzy — treat as a lead, not a measurement",
			mean*100, measured, rising, measured, strings.Join(cluster.SharedTerms, ", ")),
		ConfidenceLevel: conf,
		SourceType:      src,
	}, nil
}
