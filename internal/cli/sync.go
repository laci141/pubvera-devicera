package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
)

func init() { register("sync", cmdSync) }

// cmdSync pulls a device's records from every live source into the local
// SQLite cache with idempotent, batched upserts. Re-running is safe: existing
// rows are updated in place and only table growth counts as "new".
func cmdSync(ctx context.Context, stdout, stderr io.Writer, args []string) int {
	fs, f := newFlagSet("sync")
	db := fs.String("db", defaultDBPath(), "path to the SQLite cache")
	since := fs.String("since", "", "only records dated YYYYMMDD or later (date-capable sources)")
	batch := fs.Int("batch", 100, "upsert batch size per transaction (>=1)")
	limit := fs.Int("limit", 50, "max records per source (>=1)")
	if err := parse(fs, stderr, args, map[string]bool{"db": true, "since": true, "batch": true, "limit": true}); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "sync: a device name is required, e.g. sync pacemaker")
		return 2
	}
	if *batch < 1 {
		fmt.Fprintln(stderr, "sync: --batch must be >= 1")
		return 2
	}
	if *limit < 1 {
		fmt.Fprintln(stderr, "sync: --limit must be >= 1")
		return 2
	}
	if *since != "" && !validSince(*since) {
		fmt.Fprintln(stderr, "sync: --since must be a valid calendar date in YYYYMMDD form, e.g. 20250101")
		return 2
	}
	device := fs.Arg(0)

	st, err := openStoreAt(*db)
	if err != nil {
		fmt.Fprintf(stderr, "sync: open cache: %v\n", err)
		return 1
	}
	defer st.Close()

	until := time.Now().UTC().Format("20060102")
	res, err := syncPass(ctx, stderr, st, device, *since, until, *batch, *limit)
	if err != nil {
		fmt.Fprintf(stderr, "sync: %v\n", err)
		return 1
	}

	meta := cliutil.Meta{
		Summary: []cliutil.KV{
			{Key: "device", Value: device},
			{Key: "db", Value: *db},
			{Key: "records_fetched", Value: res.Fetched},
			{Key: "records_new", Value: res.New},
			{Key: "records_in_cache", Value: res.Total},
			{Key: "synced_at", Value: time.Now().UTC().Format(time.RFC3339)},
		},
		EmptyMsg: "no sources returned records",
	}
	if err := cliutil.Output(stdout, stderr, res.PerSource, meta, *f); err != nil {
		fmt.Fprintf(stderr, "sync: %v\n", err)
		return 1
	}
	return 0
}
