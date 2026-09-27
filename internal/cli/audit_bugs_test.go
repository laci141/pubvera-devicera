package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laci141/medical-device-intelligence/internal/sources"
	"github.com/laci141/medical-device-intelligence/internal/store"
)

// TestAuditBUG04EmptyIDRecordSurfacedInSync: an empty-ID record must not be
// lost silently — it is reported — and the valid records are still stored.
func TestAuditBUG04EmptyIDRecordSurfacedInSync(t *testing.T) {
	withSources(t, map[string]sources.Source{
		"pubmed": fakeSource{name: "pubmed", id: "pmid", recs: []sources.RawRecord{
			{ID: "P-1", Raw: map[string]any{"title": "a"}},
			{ID: "", Raw: map[string]any{"title": "no id"}},
			{ID: "P-2", Raw: map[string]any{"title": "b"}},
		}},
	})
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var stderr bytes.Buffer
	res, err := syncPass(context.Background(), &stderr, st, "pacemaker", "", "", 100, 10)
	if err != nil {
		t.Fatalf("syncPass: %v", err)
	}
	if !strings.Contains(stderr.String(), "empty id") {
		t.Errorf("empty-ID record not surfaced; stderr=%q", stderr.String())
	}
	if res.Total != 2 || res.New != 2 {
		t.Errorf("valid records: new=%d total=%d want 2/2", res.New, res.Total)
	}
}
