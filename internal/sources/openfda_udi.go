package sources

import (
	"context"
	"encoding/json"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
)

// OpenFDADeviceUDI is the LIVE openFDA GUDID adapter. Endpoint:
// https://api.fda.gov/device/udi.json. Keyless unless OPENFDA_API_KEY is set.
// It looks a device up by its UDI-DI (identifiers.id) or, failing that, by
// brand name.
type OpenFDADeviceUDI struct {
	client *cliutil.Client
}

func NewOpenFDADeviceUDI() *OpenFDADeviceUDI {
	return &OpenFDADeviceUDI{client: cliutil.NewOpenFDAClient("https://api.fda.gov")}
}

func (s *OpenFDADeviceUDI) Name() string { return "openfda_device_udi" }

// IDField: GUDID's stable per-record key. Mandatory so sync never drops rows.
func (s *OpenFDADeviceUDI) IDField() string { return "public_device_record_key" }

// buildSearch prefers an exact UDI-DI match (identifiers.id) and falls back to
// a brand-name phrase. Term carries whichever the caller passed.
func (s *OpenFDADeviceUDI) buildSearch(q Query) string {
	if q.Term == "" {
		return ""
	}
	// identifiers.id is the DI; brand_name is the human name. OR them so a
	// caller can pass either a DI string or a device name.
	return cliutil.Or(
		cliutil.Phrase("identifiers.id", q.Term),
		cliutil.Phrase("brand_name", q.Term),
	)
}

func (s *OpenFDADeviceUDI) Fetch(ctx context.Context, q Query) ([]RawRecord, Page, error) {
	params := paramsForSearch(q)
	if search := s.buildSearch(q); search != "" {
		params.Set("search", search)
	}
	body, _, err := s.client.GetJSON(ctx, "/device/udi.json", params)
	if err != nil {
		if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
			return nil, Page{}, nil // no matches → empty, not an error
		}
		return nil, Page{}, err
	}
	return parseUDI(body, s.IDField())
}

func parseUDI(body []byte, idField string) ([]RawRecord, Page, error) {
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

func (s *OpenFDADeviceUDI) Health(ctx context.Context) error {
	_, _, err := s.client.GetJSON(ctx, "/device/udi.json", paramsForSearch(Query{Limit: 1}))
	if apiErr, ok := err.(*cliutil.APIError); ok && apiErr.StatusCode == 404 {
		return nil
	}
	return err
}

func init() { Register(NewOpenFDADeviceUDI()) }
