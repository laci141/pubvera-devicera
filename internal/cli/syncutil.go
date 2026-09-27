package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/sources"
	"github.com/laci141/medical-device-intelligence/internal/store"
)

// defaultDBPath is the local cache location honored by every Group 5 command
// unless --db overrides it (guardrail 6).
func defaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "mdi.db" // fall back to the working directory
	}
	return filepath.Join(home, ".mdi", "mdi.db")
}

// openStoreAt opens (creating parent directories for) the cache database.
func openStoreAt(path string) (*store.Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	return store.Open(path)
}

// syncDateFields names the date-capable sources and the field a --since
// window applies to. The other live sources (clinicaltrials, pubmed, udi)
// have no server-side date filter in our adapters, so a window is honestly
// NOT applied to them — sync reports that instead of silently pretending.
var syncDateFields = map[string]string{
	"openfda_device_enforcement": "recall_initiation_date",
	"openfda_device_event":       "date_received",
}

// recordDateFields maps each source to the raw field carrying the record date.
var recordDateFields = map[string]string{
	"openfda_device_enforcement": "recall_initiation_date",
	"openfda_device_event":       "date_received",
	"pubmed":                     "year",
	"openfda_device_udi":         "publish_date",
}

// syncResult is one sync pass's outcome.
type syncResult struct {
	PerSource []map[string]any // one row per source: fetched counts or status
	Fetched   int              // records fetched this pass
	New       int              // rows actually added to the cache
	Total     int              // cache size after the pass
}

// syncPass fetches a device's records from every registered source and
// upserts them into the cache in batched transactions. Staged sources are
// noted, a failing source is reported and skipped (partial data is labeled,
// never invented), and the pass is recorded in sync_runs.
func syncPass(ctx context.Context, stderr io.Writer, st *store.Store, device, since, until string, batch, limit int) (syncResult, error) {
	var res syncResult
	var recs []store.Record

	names := allSources()
	sort.Strings(names)
	for _, name := range names {
		src, ok := getSource(name)
		if !ok {
			continue
		}
		q := sources.Query{Term: device, Limit: limit}
		windowed := false
		if since != "" {
			if field, ok := syncDateFields[name]; ok {
				q.DateField, q.DateFrom, q.DateTo = field, since, until
				windowed = true
			}
		}
		got, page, err := src.Fetch(ctx, q)
		if err != nil {
			if errors.Is(err, sources.ErrNotWired) {
				res.PerSource = append(res.PerSource, map[string]any{
					"source": name, "fetched": 0, "status": "staged (not yet integrated)"})
				continue
			}
			fmt.Fprintf(stderr, "sync: %s: %v\n", name, err)
			res.PerSource = append(res.PerSource, map[string]any{
				"source": name, "fetched": 0, "status": "ERROR: " + err.Error()})
			continue
		}
		status := fmt.Sprintf("ok (%d available)", page.Total)
		if since != "" && !windowed {
			status += "; no date filter (source lacks one)"
		}
		res.PerSource = append(res.PerSource, map[string]any{
			"source": name, "fetched": len(got), "status": status})
		for _, r := range got {
			if r.ID == "" {
				fmt.Fprintf(stderr, "sync: %s: skipping record with empty id\n", name)
				continue
			}
			recs = append(recs, store.Record{
				Source:  name,
				ID:      r.ID,
				Term:    device,
				Date:    str(r.Raw[recordDateFields[name]]),
				Summary: summarize(r.Raw),
				Raw:     r.Raw,
			})
		}
	}

	res.Fetched = len(recs)
	n, err := st.UpsertRecords(recs, batch)
	if err != nil {
		return res, fmt.Errorf("upsert: %w", err)
	}
	res.New = n
	total, err := st.CountRecords()
	if err != nil {
		return res, err
	}
	res.Total = total
	if err := st.RecordSyncRun(device, n, total); err != nil {
		return res, fmt.Errorf("record sync run: %w", err)
	}
	return res, nil
}

// validSince reports whether s is a real calendar date in compact YYYYMMDD
// form; impossible dates such as 20261399 or 20260230 are rejected.
func validSince(s string) bool {
	if len(s) != 8 {
		return false
	}
	_, err := time.Parse("20060102", s)
	return err == nil
}

// rfc3339ToCompact converts "2026-07-09T..." to "20260709".
func rfc3339ToCompact(ts string) string {
	if len(ts) < 10 {
		return ""
	}
	return strings.ReplaceAll(ts[:10], "-", "")
}
