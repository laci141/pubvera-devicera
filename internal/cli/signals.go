package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
)

func init() { register("signals", cmdSignals) }

// cmdSignals runs the full intelligence suite for a device and lists every
// signal reading (value, label, reasoning, sample size, sources). These are
// explainable public-record readings, never a risk score.
func cmdSignals(ctx context.Context, stdout, stderr io.Writer, args []string) int {
	fs, f := newFlagSet("signals")
	device := fs.String("device", "", "device name to analyze (required)")
	if err := parse(fs, stderr, args, map[string]bool{"device": true}); err != nil {
		return 2
	}
	term := *device
	if term == "" && fs.NArg() > 0 {
		term = fs.Arg(0)
	}
	if term == "" {
		fmt.Fprintln(stderr, "signals: a device is required, e.g. signals --device pacemaker")
		return 2
	}

	d, err := synthesize(ctx, term)
	if err != nil {
		fmt.Fprintf(stderr, "signals: %v\n", err)
		return 1
	}

	rows := make([]map[string]any, 0, len(d.Signals))
	for _, s := range d.Signals {
		// Machine modes get the full reasoning (clients parse it); the plain
		// terminal table stays clipped for readability.
		reasoning := s.Reasoning
		if !f.JSON && !f.Agent {
			reasoning = clip(reasoning, 90)
		}
		rows = append(rows, map[string]any{
			"signal":      s.SignalType,
			"value":       s.Value,
			"label":       s.Label,
			"confidence":  s.ConfidenceLevel, // deprecated: same band as sample_band
			"sample_size": s.SampleSize,
			"sample_unit": s.SampleUnit,
			"sample_band": s.SampleBand,
			"reasoning":   reasoning,
		})
	}

	meta := cliutil.Meta{
		Summary: []cliutil.KV{
			{Key: "device", Value: d.Device},
			{Key: "signals", Value: len(d.Signals)},
			{Key: "readable", Value: d.SignalsMeasured},
			{Key: "note", Value: "explainable public-record signals, not a risk score"},
		},
		EmptyMsg: "no signals produced for this device term",
	}
	if err := cliutil.Output(stdout, stderr, rows, meta, *f); err != nil {
		fmt.Fprintf(stderr, "signals: %v\n", err)
		return 1
	}
	return 0
}
