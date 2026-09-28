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
			{SignalType: "SEVERITY", Value: 0.45, Label: "Medium", ConfidenceLevel: "HIGH", Reasoning: "weighted MAUDE mix"},
			{SignalType: "VOLUME", Value: 1.0, Label: "Top", ConfidenceLevel: "HIGH", Reasoning: "719k vs p95 444k"},
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
