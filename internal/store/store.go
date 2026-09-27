// Package store is the local SQLite cache (offline, fast search). It uses the
// pure-Go modernc.org/sqlite driver, so there is no cgo and no external process.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ErrEmptyID is returned when an upsert is attempted with a blank primary key.
// Guardrail 10: a record with no extractable id is a hard error surfaced to the
// caller, never a silent drop.
var ErrEmptyID = errors.New("store: record id is empty; refusing to upsert (would drop the row)")

// Store wraps a SQLite database handle.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the SQLite database at path and applies the
// schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// DB exposes the underlying handle for read queries in other packages.
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// UpsertRegulatoryAction stores one action keyed by (agency, source_id). A
// blank source_id or agency is rejected (ErrEmptyID) rather than dropped.
func (s *Store) UpsertRegulatoryAction(agency, sourceID, jurisdiction, deviceID, actionType, status, date, url string, raw map[string]any) error {
	if sourceID == "" || agency == "" {
		return ErrEmptyID
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal raw for %s/%s: %w", agency, sourceID, err)
	}
	_, err = s.db.Exec(`
		INSERT INTO regulatory_actions
		    (source_id, agency, jurisdiction, device_id, action_type, status, date, url, raw, fetched_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(agency, source_id) DO UPDATE SET
		    jurisdiction=excluded.jurisdiction, device_id=excluded.device_id,
		    action_type=excluded.action_type, status=excluded.status,
		    date=excluded.date, url=excluded.url, raw=excluded.raw,
		    fetched_at=excluded.fetched_at`,
		sourceID, agency, jurisdiction, deviceID, actionType, status, date, url,
		string(b), time.Now().UTC().Format(time.RFC3339))
	return err
}

// CountRegulatoryActions returns the number of rows for an agency ("" = all).
func (s *Store) CountRegulatoryActions(agency string) (int, error) {
	var n int
	var err error
	if agency == "" {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM regulatory_actions`).Scan(&n)
	} else {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM regulatory_actions WHERE agency=?`, agency).Scan(&n)
	}
	return n, err
}
