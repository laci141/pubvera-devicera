package intelligence

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestClusterNoMeshTerms(t *testing.T) {
	a := NewClusterAnalyzer(mockData{}) // no trials → no MeSH terms
	ca, err := a.FindSimilarDevices(context.Background(), "obscuredevice", 5)
	if err != nil {
		t.Fatal(err)
	}
	if ca.ClusterSize != 0 || len(ca.SimilarDevices) != 0 {
		t.Errorf("want empty cluster, got %+v", ca)
	}
	if !strings.Contains(ca.Reasoning, "no MeSH condition terms") {
		t.Errorf("reasoning: %s", ca.Reasoning)
	}
}

func TestClusterSingleDeviceStandsAlone(t *testing.T) {
	// MeSH terms exist, but every intervention found is the device itself.
	a := NewClusterAnalyzer(mockData{
		meshTerms: []string{"Bradycardia"},
		condDevices: map[string][]string{
			"Bradycardia": {"Aveir DR Pacemaker", "No intervention"},
		},
	})
	ca, err := a.FindSimilarDevices(context.Background(), "pacemaker", 5)
	if err != nil {
		t.Fatal(err)
	}
	if ca.ClusterSize != 0 {
		t.Errorf("device should stand alone, got %+v", ca)
	}
	if !strings.Contains(ca.Reasoning, "stands alone") {
		t.Errorf("reasoning: %s", ca.Reasoning)
	}
}

func TestClusterSmall(t *testing.T) {
	a := NewClusterAnalyzer(mockData{
		meshTerms: []string{"Bradycardia", "Arrhythmias, Cardiac"},
		condDevices: map[string][]string{
			"Bradycardia":          {"ICD System", "Loop Recorder"},
			"Arrhythmias, Cardiac": {"ICD System"},
		},
	})
	ca, err := a.FindSimilarDevices(context.Background(), "pacemaker", 5)
	if err != nil {
		t.Fatal(err)
	}
	if ca.ClusterSize != 2 {
		t.Fatalf("want cluster of 2, got %+v", ca)
	}
	// ICD System shares 2 of 2 seed terms → ranked first.
	if ca.SimilarDevices[0] != "ICD System" {
		t.Errorf("most-shared member must rank first: %v", ca.SimilarDevices)
	}
	// Metric: mean(2/2, 1/2) = 0.75.
	if ca.SharedVolumeMetric != 0.75 {
		t.Errorf("metric=%v want 0.75", ca.SharedVolumeMetric)
	}
	if len(ca.SharedTerms) != 2 {
		t.Errorf("shared terms: %v", ca.SharedTerms)
	}
}

func TestClusterLargeCapsAtTopN(t *testing.T) {
	a := NewClusterAnalyzer(mockData{
		meshTerms: []string{"Heart Failure"},
		condDevices: map[string][]string{
			"Heart Failure": {"D1", "D2", "D3", "D4", "D5", "D6", "D7"},
		},
	})
	ca, err := a.FindSimilarDevices(context.Background(), "pacemaker", 5)
	if err != nil {
		t.Fatal(err)
	}
	if ca.ClusterSize != 5 || len(ca.SimilarDevices) != 5 {
		t.Errorf("want topN=5 cap, got %+v", ca)
	}
}

func TestClusterRiskSharedRise(t *testing.T) {
	pinClock(t)
	now := timeNow().UTC()
	mid := now.AddDate(0, 0, -364)
	old := mid.AddDate(0, 0, -365)
	recentKey := mid.Format(day) + "-" + now.Format(day)
	priorKey := old.Format(day) + "-" + mid.AddDate(0, 0, -1).Format(day)

	rise := map[string]int{recentKey: 30, priorKey: 20}   // +50%
	flat := map[string]int{recentKey: 100, priorKey: 100} // 0%
	a := NewClusterAnalyzer(mockData{
		meshTerms:   []string{"Bradycardia"},
		condDevices: map[string][]string{"Bradycardia": {"ICD System", "Loop Recorder"}},
		deviceWindows: map[string]map[string]int{
			"pacemaker":     rise,
			"ICD System":    rise,
			"Loop Recorder": flat,
		},
	})
	sig, err := a.AnalyzeClusterRisk(context.Background(), "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	// Mean slope (0.5+0.5+0)/3 ≈ 0.33 → Medium.
	if sig.Value != 0.33 || sig.Label != LabelMedium {
		t.Errorf("got %v/%q want 0.33/Medium", sig.Value, sig.Label)
	}
	for _, want := range []string{"+33%", "2 of 3 rising", "lead, not a measurement"} {
		if !strings.Contains(sig.Reasoning, want) {
			t.Errorf("reasoning missing %q: %s", want, sig.Reasoning)
		}
	}
}

func TestClusterRiskNoClusterIsUnknown(t *testing.T) {
	a := NewClusterAnalyzer(mockData{})
	sig, err := a.AnalyzeClusterRisk(context.Background(), "obscuredevice")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Label != LabelUnknown || sig.Value != 0 {
		t.Errorf("no cluster: %+v want Unknown/0", sig)
	}
}

// TestLiveClustering grounds Module 07 against the real APIs (MDI_LIVE=1).
func TestLiveClustering(t *testing.T) {
	if os.Getenv("MDI_LIVE") == "" {
		t.Skip("set MDI_LIVE=1 to run live grounding")
	}
	a := NewClusterAnalyzer(NewLiveData())
	ctx := context.Background()

	ca, err := a.FindSimilarDevices(ctx, "pacemaker", 5)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(ca, "", "  ")
	t.Logf("cluster:\n%s", b)
	if ca.ClusterSize == 0 {
		t.Fatal("pacemaker should have a non-empty MeSH cluster")
	}

	sig, err := a.AnalyzeClusterRisk(ctx, "pacemaker")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Value < 0 || sig.Value > 1 {
		t.Errorf("value %v out of [0,1]", sig.Value)
	}
	b, _ = json.MarshalIndent(sig, "", "  ")
	t.Logf("cluster-risk:\n%s", b)
}
