package sources

import (
	"context"
	"encoding/json"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
)

// OpenFDADeviceEnforcement is the LIVE openFDA device recall/enforcement
// adapter. Endpoint: https://api.fda.gov/device/enforcement.json. Keyless
// unless OPENFDA_API_KEY is set.
type OpenFDADeviceEnforcement struct {
	client *cliutil.Client
}

// NewOpenFDADeviceEnforcement constructs the adapter against the openFDA base.
func NewOpenFDADeviceEnforcement() *OpenFDADeviceEnforcement {
	return &OpenFDADeviceEnforcement{client: cliutil.NewOpenFDAClient("https://api.fda.gov")}
}

func (s *OpenFDADeviceEnforcement) Name() string    { return "openfda_device_enforcement" }
func (s *OpenFDADeviceEnforcement) IDField() string { return "recall_number" }

// buildSearch turns a Query into an openFDA Lucene expression using the shared,
// space-safe builders. Exposed (unexported) for the hermetic test.
func (s *OpenFDADeviceEnforcement) buildSearch(q Query) (string, error) {
	clauses := []string{}
	if q.Term != "" {
		clauses = append(clauses, cliutil.Phrase("product_description", q.Term))
	}
	if q.Firm != "" {
		clauses = append(clauses, cliutil.Phrase("recalling_firm", q.Firm))
	}
	if q.Class != 0 {
		cf, err := cliutil.ClassFilter(q.Class)
		if err != nil {
			return "", err
		}
		clauses = append(clauses, cf)
	}
	if q.DateFrom != "" && q.DateTo != "" {
		field := q.DateField
		if field == "" {
			field = "recall_initiation_date"
		}
		clauses = append(clauses, cliutil.DateRange(field, q.DateFrom, q.DateTo))
	}
	return cliutil.And(clauses...), nil
}

// openFDAEnvelope is the shape of an openFDA response.
type openFDAEnvelope struct {
	Meta struct {
		Results struct {
			Total int `json:"total"`
		} `json:"results"`
	} `json:"meta"`
	Results []map[string]any `json:"results"`
}

func (s *OpenFDADeviceEnforcement) Fetch(ctx context.Context, q Query) ([]RawRecord, Page, error) {
	params := paramsForSearch(q)
	search, err := s.buildSearch(q)
	if err != nil {
		return nil, Page{}, err
	}
	if search != "" {
		params.Set("search", search)
	}

	body, status, err := s.client.GetJSON(ctx, "/device/enforcement.json", params)
	if err != nil {
		// openFDA reports zero matches as HTTP 404. That is DATA, not a failure:
		// return an empty result with exit-0 semantics (guardrail 5).
		if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
			return nil, Page{}, nil
		}
		return nil, Page{}, err
	}
	_ = status

	return parseEnforcement(body, s.IDField())
}

func parseEnforcement(body []byte, idField string) ([]RawRecord, Page, error) {
	var env openFDAEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, Page{}, err
	}
	recs := make([]RawRecord, 0, len(env.Results))
	for _, r := range env.Results {
		id, _ := r[idField].(string)
		recs = append(recs, RawRecord{ID: id, Raw: r})
	}
	return recs, Page{Total: env.Meta.Results.Total, Returned: len(recs)}, nil
}

// CountField returns the server-side value distribution of a field (e.g.
// classification.exact) for the query — verified live: count=classification.exact
// on /device/enforcement.json. Implements FieldCounter.
func (s *OpenFDADeviceEnforcement) CountField(ctx context.Context, q Query, field string) (map[string]int, error) {
	search, err := s.buildSearch(q)
	if err != nil {
		return nil, err
	}
	params := paramsForSearch(Query{Limit: 1})
	params.Del("limit")
	if search != "" {
		params.Set("search", search)
	}
	params.Set("count", field)
	body, _, err := s.client.GetJSON(ctx, "/device/enforcement.json", params)
	if err != nil {
		if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
			return map[string]int{}, nil
		}
		return nil, err
	}
	return parseCounts(body)
}

func (s *OpenFDADeviceEnforcement) Health(ctx context.Context) error {
	params := paramsForSearch(Query{Limit: 1})
	_, _, err := s.client.GetJSON(ctx, "/device/enforcement.json", params)
	if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
		return nil // reachable, just no data for the probe
	}
	return err
}

func init() { Register(NewOpenFDADeviceEnforcement()) }
