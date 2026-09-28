package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/laci141/medical-device-intelligence/internal/intelligence"
)

// withDossier installs a canned Synthesize result for the duration of a test.
func withDossier(t *testing.T, d *intelligence.IntelligenceDossier, err error) {
	t.Helper()
	old := synthesize
	synthesize = func(context.Context, string) (*intelligence.IntelligenceDossier, error) {
		return d, err
	}
	t.Cleanup(func() { synthesize = old })
}

func sampleDossier() *intelligence.IntelligenceDossier {
	return &intelligence.IntelligenceDossier{
		Device: "pacemaker",
		Signals: []intelligence.Signal{
			{SignalType: "SEVERITY", Value: 0.45, Label: "Medium", ConfidenceLevel: "HIGH", Reasoning: "weighted MAUDE mix",
				SampleSize: 751555, SampleUnit: "MAUDE reports", SampleBand: "large"},
			{SignalType: "VOLUME", Value: 1.0, Label: "Top", ConfidenceLevel: "HIGH", Reasoning: "719k vs p95 444k",
				SampleSize: 100, SampleUnit: "peer device types", SampleBand: "large"},
			{SignalType: "INDEPENDENT_REPORTING", Value: 0.34, Label: "Fair", ConfidenceLevel: "HIGH", Reasoning: "provenance"},
		},
		Highlights: []string{
			"telemetry/volume = 1.00 (Top): 719k records",
			"correlation/corroboration = 1.00 (Top): 4 of 4 feeds",
			"lifecycle/recall-recency = 0.93 (Top): 133 days",
		},
		DataQuality:     []string{"INDEPENDENT_REPORTING: 34% independent"},
		AttentionIndex:  0.47,
		IndexFormula:    "attention_index = mean(value) over readable signals; NOT risk",
		SignalsMeasured: 9,
	}
}

func TestSignalsListsAllReadings(t *testing.T) {
	withDossier(t, sampleDossier(), nil)
	out, _, code := run(cmdSignals, "--device", "pacemaker")
	if code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}
	for _, want := range []string{"SEVERITY", "VOLUME", "INDEPENDENT_REPORTING", "Top", "Fair", "not a risk score"} {
		if !strings.Contains(out, want) {
			t.Errorf("signals output missing %q\n%s", want, out)
		}
	}
	if !strings.Contains(out, "not medical advice") {
		t.Error("signals must carry the disclaimer")
	}
}

// The sample columns sit beside the deprecated confidence column in every
// output mode: plain, --csv, --json.
func TestSignalsCarrySampleColumns(t *testing.T) {
	withDossier(t, sampleDossier(), nil)

	out, _, code := run(cmdSignals, "--device", "pacemaker")
	if code != 0 {
		t.Fatalf("plain exit=%d", code)
	}
	for _, want := range []string{"sample_size", "sample_unit", "sample_band", "confidence", "peer device types"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain output missing %q\n%s", want, out)
		}
	}

	out, _, code = run(cmdSignals, "--device", "pacemaker", "--csv")
	if code != 0 {
		t.Fatalf("csv exit=%d", code)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if got := strings.TrimSpace(lines[0]); got != "confidence,label,reasoning,sample_band,sample_size,sample_unit,signal,value" {
		t.Errorf("csv header = %q", got)
	}
	if !strings.Contains(out, "HIGH,Medium,weighted MAUDE mix,large,751555,MAUDE reports,SEVERITY") {
		t.Errorf("csv SEVERITY row missing sample cells:\n%s", out)
	}

	out, _, code = run(cmdSignals, "--device", "pacemaker", "--json")
	if code != 0 {
		t.Fatalf("json exit=%d", code)
	}
	var env struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	r := env.Records[1]
	if r["signal"] != "VOLUME" || r["sample_size"] != float64(100) ||
		r["sample_unit"] != "peer device types" || r["sample_band"] != "large" || r["confidence"] != "HIGH" {
		t.Errorf("json VOLUME record = %v", r)
	}
}

func TestSignalsPositionalDevice(t *testing.T) {
	withDossier(t, sampleDossier(), nil)
	if _, _, code := run(cmdSignals, "pacemaker"); code != 0 {
		t.Error("positional device arg must work")
	}
}

func TestSignalsMissingDeviceExit2(t *testing.T) {
	withDossier(t, sampleDossier(), nil)
	if _, _, code := run(cmdSignals); code != 2 {
		t.Error("missing device must exit 2")
	}
}

func TestDossierPlainShowsIndexAndHighlights(t *testing.T) {
	withDossier(t, sampleDossier(), nil)
	out, _, code := run(cmdDossier, "--device", "pacemaker")
	if code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}
	for _, want := range []string{"attention_index", "0.47", "NOT risk", "data_quality", "recall-recency"} {
		if !strings.Contains(out, want) {
			t.Errorf("dossier output missing %q\n%s", want, out)
		}
	}
	// Only the top-3 highlights, so rank 4 must never appear.
	if strings.Contains(out, "rank:                    4") {
		t.Error("dossier must show at most 3 highlights")
	}
}

func TestDossierJSONFullStruct(t *testing.T) {
	withDossier(t, sampleDossier(), nil)
	out, _, code := run(cmdDossier, "--device", "pacemaker", "--json")
	if code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("--json must emit valid JSON: %v\n%s", err, out)
	}
	if env["attention_index"] != 0.47 {
		t.Errorf("attention_index=%v want 0.47", env["attention_index"])
	}
	if _, ok := env["disclaimer"]; !ok {
		t.Error("json dossier must carry a disclaimer")
	}
	if _, ok := env["signals"]; !ok {
		t.Error("json dossier must include the full signal list")
	}
}

func TestDossierMissingDeviceExit2(t *testing.T) {
	withDossier(t, sampleDossier(), nil)
	if _, _, code := run(cmdDossier); code != 2 {
		t.Error("missing device must exit 2")
	}
}

func TestGroup6CommandsRegistered(t *testing.T) {
	for _, name := range []string{"signals", "dossier"} {
		if _, ok := commands[name]; !ok {
			t.Errorf("command %q not registered", name)
		}
	}
}
