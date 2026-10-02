package sources

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
)

// OpenFDADeviceEvent is the LIVE openFDA MAUDE adapter. Endpoint:
// https://api.fda.gov/device/event.json. Keyless unless OPENFDA_API_KEY is set.
//
// Severity is derived from the real field event_type (values Death, Injury,
// Malfunction, Other) — NOT from a "serious_adverse_event_flag", which exists in
// the DRUG event schema but not here. This was confirmed against the live API
// before coding: "serious" == Death OR Injury (verified count 326105 for
// pacemaker == Death 16694 + Injury 309411).
type OpenFDADeviceEvent struct {
	client *cliutil.Client
}

func NewOpenFDADeviceEvent() *OpenFDADeviceEvent {
	return &OpenFDADeviceEvent{client: cliutil.NewOpenFDAClient("https://api.fda.gov")}
}

func (s *OpenFDADeviceEvent) Name() string { return "openfda_device_event" }

// IDField: MAUDE's stable per-report key.
func (s *OpenFDADeviceEvent) IDField() string { return "mdr_report_key" }

// nameClause matches the device by generic or brand name (parenthesized so it
// composes safely under AND).
func nameClause(term string) string {
	return "(" + cliutil.Or(
		cliutil.Phrase("device.generic_name", term),
		cliutil.Phrase("device.brand_name", term),
	) + ")"
}

// severityClause returns the event_type filter for a severity, or "" for all.
func severityClause(sev string) (string, error) {
	switch sev {
	case "", "all":
		return "", nil
	case "death":
		return cliutil.Phrase("event_type", "Death"), nil
	case "serious":
		// Serious == death or (serious) injury, per MDR reporting.
		return "(" + cliutil.Or(
			cliutil.Phrase("event_type", "Death"),
			cliutil.Phrase("event_type", "Injury"),
		) + ")", nil
	default:
		return "", fmt.Errorf("severity must be serious, death, or empty (got %q)", sev)
	}
}

func (s *OpenFDADeviceEvent) buildSearch(q Query) (string, error) {
	if q.Term == "" {
		return "", fmt.Errorf("event search requires a device term")
	}
	clauses := []string{nameClause(q.Term)}
	sev, err := severityClause(q.Severity)
	if err != nil {
		return "", err
	}
	if sev != "" {
		clauses = append(clauses, sev)
	}
	if q.DateFrom != "" && q.DateTo != "" {
		field := q.DateField
		if field == "" {
			field = "date_received"
		}
		clauses = append(clauses, cliutil.DateRange(field, q.DateFrom, q.DateTo))
	}
	return cliutil.And(clauses...), nil
}

func (s *OpenFDADeviceEvent) Fetch(ctx context.Context, q Query) ([]RawRecord, Page, error) {
	search, err := s.buildSearch(q)
	if err != nil {
		return nil, Page{}, err
	}
	params := paramsForSearch(q)
	params.Set("search", search)

	body, _, err := s.client.GetJSON(ctx, "/device/event.json", params)
	if err != nil {
		if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
			return nil, Page{}, nil // no matches → empty, not an error
		}
		return nil, Page{}, err
	}

	var env openFDAEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, Page{}, err
	}
	recs := make([]RawRecord, 0, len(env.Results))
	for _, r := range env.Results {
		id, _ := r[s.IDField()].(string)
		recs = append(recs, RawRecord{ID: id, Raw: r})
	}
	return recs, Page{Total: env.Meta.Results.Total, Returned: len(recs)}, nil
}

// CountEventTypes returns the server-side event_type distribution for the device
// term (no severity filter), so a breakdown reflects the whole result set rather
// than one page. Implements EventCounter.
func (s *OpenFDADeviceEvent) CountEventTypes(ctx context.Context, q Query) (map[string]int, error) {
	if q.Term == "" {
		return nil, fmt.Errorf("count requires a device term")
	}
	params := paramsForSearch(Query{Limit: 1})
	params.Del("limit")
	params.Set("search", nameClause(q.Term))
	params.Set("count", "event_type.exact")

	body, _, err := s.client.GetJSON(ctx, "/device/event.json", params)
	if err != nil {
		if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
			return map[string]int{}, nil
		}
		return nil, err
	}
	return parseCounts(body)
}

// CountField returns the server-side value distribution of any field for the
// query. Unlike CountEventTypes, the device term is OPTIONAL: with an empty
// Term the distribution spans the whole endpoint (e.g. the per-device-type
// event volumes a baseline needs — verified live: count=device.generic_name.exact).
// Implements FieldCounter.
func (s *OpenFDADeviceEvent) CountField(ctx context.Context, q Query, field string) (map[string]int, error) {
	params := paramsForSearch(Query{Limit: 1})
	params.Del("limit")
	var clauses []string
	if q.Term != "" {
		clauses = append(clauses, nameClause(q.Term))
	}
	if q.DateFrom != "" && q.DateTo != "" {
		df := q.DateField
		if df == "" {
			df = "date_received"
		}
		clauses = append(clauses, cliutil.DateRange(df, q.DateFrom, q.DateTo))
	}
	if len(clauses) > 0 {
		params.Set("search", cliutil.And(clauses...))
	}
	params.Set("count", field)
	if q.Limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", q.Limit))
	}
	body, _, err := s.client.GetJSON(ctx, "/device/event.json", params)
	if err != nil {
		if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
			return map[string]int{}, nil
		}
		return nil, err
	}
	return parseCounts(body)
}

// TotalMissingField returns the server-side total of a device's reports where
// a field is absent (openFDA _missing_ filter, verified live 2026-07-09:
// _missing_:date_of_event → 110259 for pacemaker).
func (s *OpenFDADeviceEvent) TotalMissingField(ctx context.Context, term, field string) (int, error) {
	if term == "" || field == "" {
		return 0, fmt.Errorf("missing-field total requires a term and a field")
	}
	params := paramsForSearch(Query{Limit: 1})
	params.Set("search", cliutil.And(nameClause(term), "_missing_:"+field))
	body, _, err := s.client.GetJSON(ctx, "/device/event.json", params)
	if err != nil {
		if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
			return 0, nil
		}
		return 0, err
	}
	var env openFDAEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return 0, err
	}
	return env.Meta.Results.Total, nil
}

func (s *OpenFDADeviceEvent) Health(ctx context.Context) error {
	_, _, err := s.client.GetJSON(ctx, "/device/event.json", paramsForSearch(Query{Limit: 1}))
	if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
		return nil
	}
	return err
}

func init() { Register(NewOpenFDADeviceEvent()) }
