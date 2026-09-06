package intelligence

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// SynthesisAnalyzer is Module 12: the capstone. It runs the module suite for
// a device, collects every Signal into one dossier, and computes a
// transparent attention index — the mean of the readable signal values. The
// index measures how much public-record activity surrounds a device, and is
// explicitly NOT a risk score: ubiquitous, well-studied devices score high on
// attention. A failing feed degrades the dossier to "partial", never fabricates.
type SynthesisAnalyzer struct {
	data Data
}

// IntelligenceDossier is the assembled read for one device term.
type IntelligenceDossier struct {
	Device          string   `json:"device"`
	Signals         []Signal `json:"signals"`
	Highlights      []string `json:"highlights"` // top readings by value
	DataQuality     []string `json:"data_quality"`
	Notes           []string `json:"notes"` // per-signal failures (partial dossier)
	AttentionIndex  float64  `json:"attention_index"`
	IndexFormula    string   `json:"index_formula"`
	Reasoning       string   `json:"reasoning"`
	GeneratedAt     string   `json:"generated_at"`
	SignalsMeasured int      `json:"signals_measured"` // non-Unknown signals
}

const indexFormula = "attention_index = mean(value) over readable (non-Unknown) signals; measures public-record attention, NOT risk"

// probeConcurrency bounds how many probes hit the upstream feeds at once.
// The probes are independent, so the ceiling is politeness to openFDA rather
// than correctness: the keyless public API is shared by every user of this
// deployment, and eleven simultaneous request bursts per page load is a poor
// neighbour. Six keeps the wall-clock close to the unbounded case while
// leaving headroom. Raise it only with a measurement, not a guess.
const probeConcurrency = 6

// dossierProbe is one named signal producer in the synthesis run.
type dossierProbe struct {
	name string
	run  func(ctx context.Context, device string) (*Signal, error)
}

// probes assembles the module suite the dossier runs, in a stable order.
func (s *SynthesisAnalyzer) probes() []dossierProbe {
	tel := &TelemetryAnalyzer{data: s.data}
	ano := &AnomalyAnalyzer{data: s.data}
	cor := &CorrelationAnalyzer{data: s.data}
	ben := &BenchmarkAnalyzer{data: s.data}
	lif := &LifecycleAnalyzer{data: s.data}
	rep := &ReportingAnalyzer{data: s.data}
	return []dossierProbe{
		{"telemetry/severity", tel.AnalyzeSeverity},
		{"telemetry/volume", tel.AnalyzeVolume},
		{"anomaly/volume-shift", func(ctx context.Context, d string) (*Signal, error) {
			return ano.DetectVolumeShift(ctx, d, 90)
		}},
		{"correlation/recall-severity", cor.AnalyzeRecallSeverity},
		{"correlation/corroboration", cor.AnalyzeCorroboration},
		{"correlation/evidence-gap", cor.AnalyzeEvidenceGap},
		{"benchmark/severity-delta", ben.AnalyzeSeverityDelta},
		{"lifecycle/phase", lif.AnalyzeLifecyclePhase},
		{"lifecycle/recall-recency", lif.AnalyzeRecallRecency},
		{"reporting/independent", rep.AnalyzeIndependentReporting},
		{"reporting/missing-dates", rep.AnalyzeMissingEventDates},
	}
}

// dataQualityTypes are the Module 11 signals that describe the record rather
// than the device; they are reported separately and kept out of the index.
var dataQualityTypes = map[string]bool{
	SignalIndependentReporting: true,
	SignalMissingEventDates:    true,
}

// probeResult is one probe's outcome, held until every probe has finished.
type probeResult struct {
	sig *Signal
	err error
}

// runProbes runs the suite concurrently and returns the results POSITIONALLY,
// index i holding probe i's outcome regardless of which finished first. The
// probes are independent — each takes only (ctx, device) and returns its own
// Signal — so running them together changes timing, not meaning. Writing into
// a pre-sized slice by index means no goroutine touches another's element and
// no mutex is needed; the assembly loop below then reads them back in
// probes() order, which is what keeps the dossier byte-identical between runs.
func (s *SynthesisAnalyzer) runProbes(ctx context.Context, device string, ps []dossierProbe) []probeResult {
	out := make([]probeResult, len(ps))
	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup
	for i, p := range ps {
		wg.Add(1)
		go func(i int, p dossierProbe) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sig, err := p.run(ctx, device)
			out[i] = probeResult{sig: sig, err: err}
		}(i, p)
	}
	wg.Wait()
	return out
}

// Synthesize runs the suite and assembles the dossier. Individual probe
// failures become notes (a partial dossier), never a fabricated reading.
func (s *SynthesisAnalyzer) Synthesize(ctx context.Context, device string) (*IntelligenceDossier, error) {
	d := &IntelligenceDossier{
		Device:       device,
		IndexFormula: indexFormula,
		GeneratedAt:  timeNow().UTC().Format(time.RFC3339),
	}
	type scored struct {
		name string
		sig  *Signal
	}
	var readable []scored
	sum := 0.0
	ps := s.probes()
	results := s.runProbes(ctx, device, ps)
	for i, p := range ps {
		res := results[i]
		if res.err != nil {
			d.Notes = append(d.Notes, fmt.Sprintf("%s unavailable: %v", p.name, res.err))
			continue
		}
		sig := res.sig
		d.Signals = append(d.Signals, *sig)
		if dataQualityTypes[sig.SignalType] {
			d.DataQuality = append(d.DataQuality, fmt.Sprintf("%s: %s", sig.SignalType, sig.Reasoning))
			continue
		}
		if sig.Label == LabelUnknown {
			continue
		}
		readable = append(readable, scored{p.name, sig})
		sum += sig.Value
	}
	d.SignalsMeasured = len(readable)

	if len(readable) == 0 {
		d.AttentionIndex = 0
		d.Reasoning = "insufficient data: no signal produced a readable value; absence of records is not evidence of safety"
		return d, nil
	}
	d.AttentionIndex = round2(sum / float64(len(readable)))

	sort.SliceStable(readable, func(i, j int) bool { return readable[i].sig.Value > readable[j].sig.Value })
	top := readable
	if len(top) > 3 {
		top = top[:3]
	}
	for _, r := range top {
		d.Highlights = append(d.Highlights, fmt.Sprintf(
			"%s = %.2f (%s): %s", r.name, r.sig.Value, r.sig.Label, r.sig.Reasoning))
	}

	partial := ""
	if len(d.Notes) > 0 {
		partial = fmt.Sprintf("; PARTIAL — %d probe(s) unavailable", len(d.Notes))
	}
	d.Reasoning = fmt.Sprintf(
		"attention index %.2f over %d readable signals (%d data-quality readings reported separately)%s; %s",
		d.AttentionIndex, len(readable), len(d.DataQuality), partial,
		strings.TrimPrefix(indexFormula, "attention_index = "))
	return d, nil
}
