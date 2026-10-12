package sources

import (
	"context"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
)

// OpenFDADeviceRecall reads the openFDA device recall feed
// (https://api.fda.gov/device/recall.json), which carries the root_cause_description
// the enforcement feed lacks. It only counts: it is deliberately NOT registered,
// so sync, search and doctor keep their current source list. Keyless unless
// OPENFDA_API_KEY is set. Implements FieldCounter.
type OpenFDADeviceRecall struct {
	client *cliutil.Client
}

// NewOpenFDADeviceRecall constructs the adapter against the openFDA base.
func NewOpenFDADeviceRecall() *OpenFDADeviceRecall {
	return newOpenFDADeviceRecallAt("https://api.fda.gov")
}

func newOpenFDADeviceRecallAt(base string) *OpenFDADeviceRecall {
	return &OpenFDADeviceRecall{client: cliutil.NewOpenFDAClient(base)}
}

// buildSearch matches the device term as a phrase in product_description, the
// same field the enforcement adapter searches.
func (s *OpenFDADeviceRecall) buildSearch(q Query) string {
	if q.Term == "" {
		return ""
	}
	return cliutil.Phrase("product_description", q.Term)
}

// CountField returns the server-side value distribution of a field (e.g.
// root_cause_description.exact) for the query. A 404 is openFDA's "no matches":
// an empty distribution, not a failure.
func (s *OpenFDADeviceRecall) CountField(ctx context.Context, q Query, field string) (map[string]int, error) {
	params := paramsForSearch(Query{Limit: 1})
	params.Del("limit")
	if search := s.buildSearch(q); search != "" {
		params.Set("search", search)
	}
	params.Set("count", field)
	body, _, err := s.client.GetJSON(ctx, "/device/recall.json", params)
	if err != nil {
		if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
			return map[string]int{}, nil
		}
		return nil, err
	}
	return parseCounts(body)
}
