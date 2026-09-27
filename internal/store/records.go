package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Record is one provider record in the generic sync cache. Source + ID form
// the primary key; a blank in either is rejected (ErrEmptyID), never dropped.
type Record struct {
	Source  string
	ID      string
	Term    string
	Date    string // provider's record date, YYYYMMDD or "" when unknown
	Summary string
	Raw     map[string]any
}

// UpsertRecords upserts records in chunks of batch rows per transaction
// (guardrail: batched inserts, never one auto-commit per row). The upsert is
// idempotent: re-syncing the same records updates in place. It returns how
// many rows were actually NEW (table growth), which is what a delta report
// needs — an update is not "new".
func (s *Store) UpsertRecords(recs []Record, batch int) (int, error) {
	if batch < 1 {
		batch = 100
	}
	before, err := s.CountRecords()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for start := 0; start < len(recs); start += batch {
		end := start + batch
		if end > len(recs) {
			end = len(recs)
		}
		tx, err := s.db.Begin()
		if err != nil {
			return 0, err
		}
		for _, r := range recs[start:end] {
			if r.Source == "" || r.ID == "" {
				tx.Rollback()
				return 0, ErrEmptyID
			}
			b, err := json.Marshal(r.Raw)
			if err != nil {
				tx.Rollback()
				return 0, fmt.Errorf("marshal raw for %s/%s: %w", r.Source, r.ID, err)
			}
			if _, err := tx.Exec(`
				INSERT INTO records (source, record_id, term, date, summary, raw, fetched_at)
				VALUES (?,?,?,?,?,?,?)
				ON CONFLICT(source, record_id) DO UPDATE SET
				    term=excluded.term, date=excluded.date, summary=excluded.summary,
				    raw=excluded.raw, fetched_at=excluded.fetched_at`,
				r.Source, r.ID, r.Term, r.Date, r.Summary, string(b), now); err != nil {
				tx.Rollback()
				return 0, err
			}
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
	}
	after, err := s.CountRecords()
	if err != nil {
		return 0, err
	}
	return after - before, nil
}

// CountRecords returns the total number of cached records.
func (s *Store) CountRecords() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&n)
	return n, err
}

// ExportRow is one flattened cache row for export (raw JSON omitted; export
// is the human/machine summary view, the raw payload stays in the db).
type ExportRow struct {
	Source    string
	RecordID  string
	Term      string
	Date      string
	Summary   string
	FetchedAt string
}

// AllRecords returns every cached record ordered by (source, record_id) so
// exports are deterministic.
func (s *Store) AllRecords() ([]ExportRow, error) {
	rows, err := s.db.Query(`
		SELECT source, record_id, term, date, summary, fetched_at
		FROM records ORDER BY source, record_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportRow
	for rows.Next() {
		var r ExportRow
		if err := rows.Scan(&r.Source, &r.RecordID, &r.Term, &r.Date, &r.Summary, &r.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordSyncRun appends one bookkeeping row for a completed sync pass.
func (s *Store) RecordSyncRun(term string, newRecords, totalAfter int) error {
	_, err := s.db.Exec(`
		INSERT INTO sync_runs (term, started_at, new_records, total_after)
		VALUES (?,?,?,?)`,
		term, time.Now().UTC().Format(time.RFC3339), newRecords, totalAfter)
	return err
}

// LastSyncTime returns the newest sync start time for a term (RFC3339) and
// whether one exists.
func (s *Store) LastSyncTime(term string) (string, bool, error) {
	var t string
	err := s.db.QueryRow(`
		SELECT started_at FROM sync_runs WHERE term=? ORDER BY id DESC LIMIT 1`,
		term).Scan(&t)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return t, true, nil
}
