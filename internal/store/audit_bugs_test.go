package store

import (
	"math"
	"testing"
)

// TestAuditBUG05UnmarshalableRawErrors: a raw payload json.Marshal rejects must
// fail the upsert, and no row may be written with empty/corrupt raw data.
func TestAuditBUG05UnmarshalableRawErrors(t *testing.T) {
	s := openTemp(t)
	bad := Record{Source: "pubmed", ID: "P-1", Term: "t", Raw: map[string]any{"x": math.NaN()}}
	if _, err := s.UpsertRecords([]Record{rec("pubmed", "P-0", "t"), bad}, 100); err == nil {
		t.Fatal("want marshal error, got nil")
	}
	if n, _ := s.CountRecords(); n != 0 {
		t.Fatalf("rows written=%d want 0", n)
	}
}

// TestAuditBUG05RegulatoryActionUnmarshalableRawErrors: same contract for
// UpsertRegulatoryAction — marshal failure is an error and no row is written.
func TestAuditBUG05RegulatoryActionUnmarshalableRawErrors(t *testing.T) {
	s := openTemp(t)
	err := s.UpsertRegulatoryAction("FDA", "A-1", "US", "", "recall", "open", "20240101", "",
		map[string]any{"x": math.NaN()})
	if err == nil {
		t.Fatal("want marshal error, got nil")
	}
	if n, _ := s.CountRegulatoryActions(""); n != 0 {
		t.Fatalf("rows written=%d want 0", n)
	}
}
