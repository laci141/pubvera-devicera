// Package sources holds the raw data-provider adapters. Each provider
// implements Source. Adding a provider means adding one file + one registry
// line; no existing adapter changes. Providers never import the regulatory or
// intelligence layers.
package sources

import (
	"context"
	"errors"
)

// ErrNotWired marks a compile-ready adapter whose live Fetch is not implemented
// yet. It lets the plugin registry and the build stay green while a source is
// staged for later. Callers surface it as "source not yet integrated", never as
// a crash or a silent empty result.
var ErrNotWired = errors.New("source not yet integrated")

// Query is the source-agnostic request. Each adapter's own query builder
// translates it into that provider's syntax (openFDA Lucene, ClinicalTrials
// params, PubMed E-utilities, ...).
type Query struct {
	Term      string // free-text subject, e.g. "pacemaker"
	Firm      string // optional manufacturer / recalling-firm filter
	Severity  string // MAUDE event severity: "" (all) | "serious" | "death"
	Limit     int
	Skip      int
	DateField string // optional, for date-bounded queries
	DateFrom  string // YYYYMMDD
	DateTo    string // YYYYMMDD
	Class     int    // 0 = unset; 1..3 = FDA class filter (openFDA only)
}

// EventCounter is an optional capability: a source that can return a server-side
// distribution (e.g. openFDA count=event_type.exact) so severity breakdowns are
// accurate for the whole result set, not skewed by a page limit. Commands
// type-assert for it; sources that lack it are simply not counted.
type EventCounter interface {
	CountEventTypes(ctx context.Context, q Query) (map[string]int, error)
}

// FieldCounter is an optional capability: a source that can return a
// server-side value distribution for an arbitrary field (openFDA count=...),
// so breakdowns reflect the whole result set, not one page. Commands
// type-assert for it.
type FieldCounter interface {
	CountField(ctx context.Context, q Query, field string) (map[string]int, error)
}

// RawRecord is one provider record: the raw JSON plus the extracted primary key.
type RawRecord struct {
	ID  string         // the provider's own record id
	Raw map[string]any // decoded JSON object
}

// Page carries pagination state returned alongside a fetch.
type Page struct {
	Total    int
	Returned int
}

// Source is the common interface every raw provider implements.
type Source interface {
	// Name is the stable registry key, e.g. "openfda_device_enforcement".
	Name() string
	// IDField is the provider's primary-key field. It is mandatory: a source
	// that cannot name its id would drop every row on sync (a bug we already
	// paid for). Compile-time required via this interface.
	IDField() string
	// Fetch performs one request. A provider "no matches" (e.g. openFDA 404)
	// must be returned as an empty slice with nil error, never as a failure.
	Fetch(ctx context.Context, q Query) ([]RawRecord, Page, error)
	// Health is a cheap keyless liveness probe.
	Health(ctx context.Context) error
}

// registry maps a source name to its constructed adapter.
var registry = map[string]Source{}

// Register adds a source to the plugin registry. Called from adapter init().
func Register(s Source) { registry[s.Name()] = s }

// Get returns a registered source by name.
func Get(name string) (Source, bool) { s, ok := registry[name]; return s, ok }

// All returns every registered source name.
func All() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}
