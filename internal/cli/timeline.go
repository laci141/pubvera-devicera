package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
	"github.com/laci141/medical-device-intelligence/internal/sources"
)

func init() { register("timeline", cmdTimeline) }

// releaseLagNote marks the newest month: MAUDE publishes reports in batches, so
// the latest month can still grow.
const releaseLagNote = "may be incomplete (MAUDE release lag)"

// cmdTimeline merges a device's public records into one chronology, newest
// first: recalls (openFDA enforcement) and adverse events (MAUDE). FDA
// regulatory actions are the recall entries themselves (same source), so they
// are not double-counted; non-FDA agencies are skeletons. Every recall row
// cites its source record id. Facts only — no causation language.
//
// MAUDE is shown as monthly report counts from one count=date_received query,
// not as individual reports: measured 2026-09-30, even the newest 999 reports
// for pacemaker cover only 6 days, so no recall ever shared their period.
func cmdTimeline(ctx context.Context, stdout, stderr io.Writer, args []string) int {
	fs, f := newFlagSet("timeline")
	limit := fs.Int("limit", 25, "max recalls, newest first (1-999)")
	months := fs.Int("months", 24, "newest months of MAUDE report counts (0 = full history)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: timeline [flags] <device>")
		fmt.Fprintln(fs.Output(), "Recalls (openFDA enforcement) newest first, one row per recall, merged with")
		fmt.Fprintln(fs.Output(), "MAUDE adverse event reports counted per month (not individual reports).")
		fmt.Fprintln(fs.Output(), "Month rows are dated to the month's last day, so each sits above its recalls.")
		fmt.Fprintln(fs.Output(), "--limit applies to recalls; --months sets the monthly window.")
		fs.PrintDefaults()
	}
	if err := parse(fs, stderr, args, map[string]bool{"limit": true, "months": true}); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "timeline: a device name is required, e.g. timeline pacemaker")
		return 2
	}
	if *limit < 1 || *limit > 999 {
		fmt.Fprintln(stderr, "timeline: --limit must be between 1 and 999")
		return 2
	}
	if *months < 0 {
		fmt.Fprintln(stderr, "timeline: --months must be >= 0")
		return 2
	}
	device := fs.Arg(0)

	var rows []map[string]any

	if src, ok := getSource("openfda_device_enforcement"); ok {
		recs, _, err := src.Fetch(ctx, sources.Query{Term: device, Limit: *limit, Sort: "recall_initiation_date:desc"})
		if err != nil {
			fmt.Fprintf(stderr, "timeline: recalls unavailable: %v\n", err)
		}
		for _, r := range recs {
			rows = append(rows, map[string]any{
				"kind":        "recall",
				"date":        str(r.Raw["recall_initiation_date"]),
				"event_type":  "Recall (" + str(r.Raw["classification"]) + ")",
				"source":      "openFDA enforcement",
				"source_id":   r.ID,
				"description": clip(str(r.Raw["product_description"]), 90),
			})
		}
	}

	if src, ok := getSource("openfda_device_event"); ok {
		counter, ok := src.(sources.FieldCounter)
		if !ok {
			fmt.Fprintln(stderr, "timeline: monthly event counts unavailable: MAUDE source cannot count")
		} else if daily, err := counter.CountField(ctx, sources.Query{Term: device}, "date_received"); err != nil {
			fmt.Fprintf(stderr, "timeline: monthly event counts unavailable: %v\n", err)
		} else {
			rows = append(rows, monthRows(daily, *months)...)
		}
	}

	// Sort by date descending (YYYYMMDD strings, equal length → lexicographic).
	// A month row and a recall on the month's last day tie; the month row goes
	// first so the recall stays under its month. Empty dates sort last.
	sort.SliceStable(rows, func(i, j int) bool {
		di, dj := str(rows[i]["date"]), str(rows[j]["date"])
		if di == "" {
			return false
		}
		if dj == "" {
			return true
		}
		if di == dj {
			return rows[i]["kind"] == "events_month" && rows[j]["kind"] != "events_month"
		}
		return di > dj
	})

	meta := cliutil.Meta{Legend: cliutil.FDAClassLegend, EmptyMsg: cliutil.NoRecordsMsg}
	if err := cliutil.Output(stdout, stderr, rows, meta, *f); err != nil {
		fmt.Fprintf(stderr, "timeline: %v\n", err)
		return 1
	}
	return 0
}

// monthRows sums daily YYYYMMDD buckets into one row per calendar month, newest
// first, covering every month (zero-count months included) from the newest
// month with data back `window` months, or back to the oldest data if window
// is 0. Each row is dated to its month's last day; the newest carries the
// release-lag note.
func monthRows(daily map[string]int, window int) []map[string]any {
	byMonth := map[string]int{}
	for day, n := range daily {
		if len(day) != 8 {
			continue
		}
		byMonth[day[:6]] += n
	}
	if len(byMonth) == 0 {
		return nil
	}
	keys := make([]string, 0, len(byMonth))
	for k := range byMonth {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	oldest, err1 := time.Parse("200601", keys[0])
	newest, err2 := time.Parse("200601", keys[len(keys)-1])
	if err1 != nil || err2 != nil {
		return nil
	}

	var rows []map[string]any
	for m := newest; !m.Before(oldest); m = m.AddDate(0, -1, 0) {
		if window > 0 && len(rows) == window {
			break
		}
		row := map[string]any{
			"kind":       "events_month",
			"date":       m.AddDate(0, 1, -1).Format("20060102"),
			"month":      m.Format("2006-01"),
			"count":      byMonth[m.Format("200601")],
			"event_type": "MAUDE reports received",
			"source":     "openFDA MAUDE (count=date_received)",
		}
		if len(rows) == 0 {
			row["note"] = releaseLagNote
		}
		rows = append(rows, row)
	}
	return rows
}
