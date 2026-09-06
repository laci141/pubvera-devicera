// Package cli holds the command surface. Each command lives in its own file,
// parses flags with cliutil.ReorderArgs (so flags after positionals still bind),
// validates explicitly, fetches from a source or module, and renders through the
// shared cliutil.Output disclaimer gate. English-only throughout.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
	"github.com/laci141/medical-device-intelligence/internal/intelligence"
	"github.com/laci141/medical-device-intelligence/internal/sources"
)

// Handler runs one command. It returns a process exit code: 0 success/empty,
// 1 API/runtime error, 2 usage error.
type Handler func(ctx context.Context, stdout, stderr io.Writer, args []string) int

// Indirections so tests can inject fake sources without touching the network or
// mutating the global registry.
var (
	getSource  = sources.Get
	allSources = sources.All
	// runSynthesis is the raw Module 12 run over the live sources.
	runSynthesis = func(ctx context.Context, device string) (*intelligence.IntelligenceDossier, error) {
		return intelligence.NewSynthesisAnalyzer(intelligence.NewLiveData()).Synthesize(ctx, device)
	}
	// synthesize is what the signals/dossier commands call. It routes through
	// sharedSynth so the two commands, which serve.go dispatches simultaneously
	// for one page load, run the eleven-probe suite once between them instead of
	// twice. Tests replace this whole function with a canned dossier, which also
	// keeps the group out of their way.
	synthesize = func(ctx context.Context, device string) (*intelligence.IntelligenceDossier, error) {
		return sharedSynth.Do(ctx, device, runSynthesis)
	}
)

// commands is the command registry; each file registers itself in init().
var commands = map[string]Handler{}

func register(name string, h Handler) { commands[name] = h }

// Names returns the registered command names.
func Names() []string {
	out := make([]string, 0, len(commands))
	for n := range commands {
		out = append(out, n)
	}
	return out
}

// Dispatch routes argv (without the program name) to a command handler.
func Dispatch(ctx context.Context, stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	name := args[0]
	if name == "help" || name == "-h" || name == "--help" {
		usage(stdout)
		return 0
	}
	h, ok := commands[name]
	if !ok {
		fmt.Fprintf(stderr, "unknown command %q\n", name)
		usage(stderr)
		return 2
	}
	return h(ctx, stdout, stderr, args[1:])
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: medical-device-intelligence-pp-cli <command> [flags]")
	fmt.Fprintln(w, "commands: search | udi | manufacturers | summary | recalls |")
	fmt.Fprintln(w, "          adverse | safety | timeline | trials | publications |")
	fmt.Fprintln(w, "          evidence | device-report | compare | emerging | score |")
	fmt.Fprintln(w, "          analytics | signals | dossier | sync | watch | workflow |")
	fmt.Fprintln(w, "          export | doctor | serve")
	fmt.Fprintln(w, "output flags (any command): --json | --csv | --agent | --plain")
}

// newFlagSet builds a flag set pre-wired with the four shared output flags.
func newFlagSet(name string) (*flag.FlagSet, *cliutil.Flags) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	f := &cliutil.Flags{}
	fs.BoolVar(&f.JSON, "json", false, "machine JSON envelope")
	fs.BoolVar(&f.CSV, "csv", false, "CSV rows on stdout, disclaimer on stderr")
	fs.BoolVar(&f.Agent, "agent", false, "agent mode (machine JSON)")
	fs.BoolVar(&f.Plain, "plain", false, "force plain text")
	return fs, f
}

// parse reorders args (flags-before-positionals) and parses them. valueFlags
// names the command's own value-taking flags (e.g. "limit", "class").
func parse(fs *flag.FlagSet, stderr io.Writer, args []string, valueFlags map[string]bool) error {
	fs.SetOutput(stderr)
	return fs.Parse(cliutil.ReorderArgs(args, valueFlags))
}

// enforcementRows projects openFDA enforcement records into render rows, always
// leading with the recall_number (the cited source id).
func enforcementRows(recs []sources.RawRecord) []map[string]any {
	rows := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, map[string]any{
			"recall_number":  r.ID,
			"classification": str(r.Raw["classification"]),
			"recalling_firm": str(r.Raw["recalling_firm"]),
			"product":        str(r.Raw["product_description"]),
			"reason":         str(r.Raw["reason_for_recall"]),
			"initiated":      str(r.Raw["recall_initiation_date"]),
		})
	}
	return rows
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
