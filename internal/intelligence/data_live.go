package intelligence

import (
	"context"
	"fmt"
	"sort"

	"github.com/laci141/medical-device-intelligence/internal/sources"
)

// liveData implements Data over the registered live sources. Read-only by
// construction: it only calls Fetch/count capabilities, never the store.
type liveData struct{}

// NewLiveData returns the production Data backed by the source registry.
func NewLiveData() Data { return liveData{} }

func (liveData) EventTypeCounts(ctx context.Context, device string) (map[string]int, error) {
	src, ok := sources.Get("openfda_device_event")
	if !ok {
		return nil, fmt.Errorf("MAUDE source unavailable")
	}
	counter, ok := src.(sources.EventCounter)
	if !ok {
		return nil, fmt.Errorf("MAUDE source lacks event counting")
	}
	return counter.CountEventTypes(ctx, sources.Query{Term: device})
}

func (liveData) RecallTotal(ctx context.Context, device string) (int, error) {
	src, ok := sources.Get("openfda_device_enforcement")
	if !ok {
		return 0, fmt.Errorf("enforcement source unavailable")
	}
	_, page, err := src.Fetch(ctx, sources.Query{Term: device, Limit: 1})
	return page.Total, err
}

func (liveData) EventTotalWindow(ctx context.Context, device, from, to string) (int, error) {
	src, ok := sources.Get("openfda_device_event")
	if !ok {
		return 0, fmt.Errorf("MAUDE source unavailable")
	}
	_, page, err := src.Fetch(ctx, sources.Query{
		Term: device, Limit: 1,
		DateField: "date_received", DateFrom: from, DateTo: to,
	})
	return page.Total, err
}

func (liveData) EventTypeCountsWindow(ctx context.Context, device, from, to string) (map[string]int, error) {
	src, ok := sources.Get("openfda_device_event")
	if !ok {
		return nil, fmt.Errorf("MAUDE source unavailable")
	}
	counter, ok := src.(sources.FieldCounter)
	if !ok {
		return nil, fmt.Errorf("MAUDE source lacks field counting")
	}
	return counter.CountField(ctx, sources.Query{
		Term: device, DateField: "date_received", DateFrom: from, DateTo: to,
	}, "event_type.exact")
}

func (liveData) RecallClassCounts(ctx context.Context, device string) (map[string]int, error) {
	src, ok := sources.Get("openfda_device_enforcement")
	if !ok {
		return nil, fmt.Errorf("enforcement source unavailable")
	}
	counter, ok := src.(sources.FieldCounter)
	if !ok {
		return nil, fmt.Errorf("enforcement source lacks field counting")
	}
	return counter.CountField(ctx, sources.Query{Term: device}, "classification.exact")
}

func (liveData) TrialTotal(ctx context.Context, device string) (int, error) {
	src, ok := sources.Get("clinicaltrials")
	if !ok {
		return 0, fmt.Errorf("clinicaltrials source unavailable")
	}
	_, page, err := src.Fetch(ctx, sources.Query{Term: device, Limit: 1})
	return page.Total, err
}

func (liveData) PublicationTotal(ctx context.Context, device string) (int, error) {
	src, ok := sources.Get("pubmed")
	if !ok {
		return 0, fmt.Errorf("pubmed source unavailable")
	}
	_, page, err := src.Fetch(ctx, sources.Query{Term: device, Limit: 1})
	return page.Total, err
}

func (liveData) RecallActions(ctx context.Context, device string, limit int) ([]ComplianceAction, error) {
	src, ok := sources.Get("openfda_device_enforcement")
	if !ok {
		return nil, fmt.Errorf("enforcement source unavailable")
	}
	// Newest first, so the page holds the most recent recalls, not index order.
	recs, _, err := src.Fetch(ctx, sources.Query{
		Term: device, Limit: limit, Sort: "recall_initiation_date:desc",
	})
	if err != nil {
		return nil, err
	}
	out := make([]ComplianceAction, 0, len(recs))
	for _, r := range recs {
		class, _ := r.Raw["classification"].(string)
		desc, _ := r.Raw["product_description"].(string)
		date, _ := r.Raw["recall_initiation_date"].(string)
		if len(desc) > 100 {
			desc = desc[:100] + "..."
		}
		out = append(out, ComplianceAction{
			Date:        date,
			Type:        "recall (" + class + ")",
			Description: desc,
			Reference:   r.ID,
		})
	}
	return out, nil
}

// enforcementCounter type-asserts the enforcement source's count capability.
func enforcementCounter() (sources.FieldCounter, error) {
	src, ok := sources.Get("openfda_device_enforcement")
	if !ok {
		return nil, fmt.Errorf("enforcement source unavailable")
	}
	counter, ok := src.(sources.FieldCounter)
	if !ok {
		return nil, fmt.Errorf("enforcement source lacks field counting")
	}
	return counter, nil
}

func (liveData) FirmRecallClassCounts(ctx context.Context, firm string) (map[string]int, error) {
	counter, err := enforcementCounter()
	if err != nil {
		return nil, err
	}
	return counter.CountField(ctx, sources.Query{Firm: firm}, "classification.exact")
}

func (liveData) FirmRecallStatusCounts(ctx context.Context, firm string) (map[string]int, error) {
	counter, err := enforcementCounter()
	if err != nil {
		return nil, err
	}
	return counter.CountField(ctx, sources.Query{Firm: firm}, "status.exact")
}

func (liveData) FirmRecallTotalWindow(ctx context.Context, firm, from, to string) (int, error) {
	src, ok := sources.Get("openfda_device_enforcement")
	if !ok {
		return 0, fmt.Errorf("enforcement source unavailable")
	}
	_, page, err := src.Fetch(ctx, sources.Query{
		Firm: firm, Limit: 1,
		DateField: "recall_initiation_date", DateFrom: from, DateTo: to,
	})
	return page.Total, err
}

// eventCounter type-asserts the MAUDE source's count capability.
func eventCounter() (sources.FieldCounter, error) {
	src, ok := sources.Get("openfda_device_event")
	if !ok {
		return nil, fmt.Errorf("MAUDE source unavailable")
	}
	counter, ok := src.(sources.FieldCounter)
	if !ok {
		return nil, fmt.Errorf("MAUDE source lacks field counting")
	}
	return counter, nil
}

// DeviceTypeVolumes is the per-device-type MAUDE event volume distribution
// (top 100 most-reported types — openFDA count endpoints return the top of
// the distribution, so percentile ranks are within that top slice).
func (liveData) DeviceTypeVolumes(ctx context.Context) (map[string]int, error) {
	counter, err := eventCounter()
	if err != nil {
		return nil, err
	}
	return counter.CountField(ctx, sources.Query{Limit: 100}, "device.generic_name.exact")
}

func (liveData) GlobalEventTypeCounts(ctx context.Context) (map[string]int, error) {
	counter, err := eventCounter()
	if err != nil {
		return nil, err
	}
	return counter.CountField(ctx, sources.Query{}, "event_type.exact")
}

func (liveData) GlobalRecallClassCounts(ctx context.Context) (map[string]int, error) {
	counter, err := enforcementCounter()
	if err != nil {
		return nil, err
	}
	return counter.CountField(ctx, sources.Query{}, "classification.exact")
}

// meshLookup type-asserts the CT.gov source's MeSH capability.
func meshLookup() (sources.MeshLookup, error) {
	src, ok := sources.Get("clinicaltrials")
	if !ok {
		return nil, fmt.Errorf("clinicaltrials source unavailable")
	}
	m, ok := src.(sources.MeshLookup)
	if !ok {
		return nil, fmt.Errorf("clinicaltrials source lacks MeSH lookup")
	}
	return m, nil
}

func (liveData) DeviceMeshTerms(ctx context.Context, device string, maxTrials int) ([]string, error) {
	m, err := meshLookup()
	if err != nil {
		return nil, err
	}
	return m.MeshTerms(ctx, device, maxTrials)
}

func (liveData) DevicesForCondition(ctx context.Context, meshTerm string, limit int) ([]string, error) {
	m, err := meshLookup()
	if err != nil {
		return nil, err
	}
	return m.InterventionsForCondition(ctx, meshTerm, limit)
}

func (liveData) ProblemCounts(ctx context.Context, device string) (map[string]int, error) {
	counter, err := eventCounter()
	if err != nil {
		return nil, err
	}
	return counter.CountField(ctx, sources.Query{Term: device}, "product_problems.exact")
}

func (liveData) ProblemCountsWindow(ctx context.Context, device, from, to string) (map[string]int, error) {
	counter, err := eventCounter()
	if err != nil {
		return nil, err
	}
	return counter.CountField(ctx, sources.Query{
		Term: device, DateField: "date_received", DateFrom: from, DateTo: to,
	}, "product_problems.exact")
}

func (liveData) PublicationCountWindow(ctx context.Context, device string, fromYear, toYear int) (int, error) {
	src, ok := sources.Get("pubmed")
	if !ok {
		return 0, fmt.Errorf("pubmed source unavailable")
	}
	pm, ok := src.(interface {
		PublicationCountWindow(ctx context.Context, term string, fromYear, toYear int) (int, error)
	})
	if !ok {
		return 0, fmt.Errorf("pubmed source lacks windowed counting")
	}
	return pm.PublicationCountWindow(ctx, device, fromYear, toYear)
}

func (liveData) TrialStatusTotal(ctx context.Context, device string, statuses []string) (int, error) {
	src, ok := sources.Get("clinicaltrials")
	if !ok {
		return 0, fmt.Errorf("clinicaltrials source unavailable")
	}
	ct, ok := src.(interface {
		TrialStatusTotal(ctx context.Context, device string, statuses []string) (int, error)
	})
	if !ok {
		return 0, fmt.Errorf("clinicaltrials source lacks status totals")
	}
	return ct.TrialStatusTotal(ctx, device, statuses)
}

func (liveData) ReporterSourceCounts(ctx context.Context, device string) (map[string]int, error) {
	counter, err := eventCounter()
	if err != nil {
		return nil, err
	}
	return counter.CountField(ctx, sources.Query{Term: device}, "source_type.exact")
}

func (liveData) EventTotalMissing(ctx context.Context, device, field string) (int, error) {
	src, ok := sources.Get("openfda_device_event")
	if !ok {
		return 0, fmt.Errorf("MAUDE source unavailable")
	}
	ev, ok := src.(interface {
		TotalMissingField(ctx context.Context, term, field string) (int, error)
	})
	if !ok {
		return 0, fmt.Errorf("MAUDE source lacks missing-field totals")
	}
	return ev.TotalMissingField(ctx, device, field)
}

func (liveData) ManufacturerNameCounts(ctx context.Context, device string) (map[string]int, error) {
	counter, err := eventCounter()
	if err != nil {
		return nil, err
	}
	return counter.CountField(ctx, sources.Query{Term: device}, "device.manufacturer_d_name.exact")
}

// VolumeBaseline computes the p95 of MAUDE event volumes across the 100
// most-reported device types (count=device.generic_name.exact, verified live
// 2026-07-09: p95 ≈ 444k). The sample is the top of the distribution, so the
// baseline is conservative — it understates, never inflates, a volume signal.
func (liveData) VolumeBaseline(ctx context.Context) (int, int, error) {
	src, ok := sources.Get("openfda_device_event")
	if !ok {
		return 0, 0, fmt.Errorf("MAUDE source unavailable")
	}
	counter, ok := src.(sources.FieldCounter)
	if !ok {
		return 0, 0, fmt.Errorf("MAUDE source lacks field counting")
	}
	counts, err := counter.CountField(ctx, sources.Query{Limit: 100}, "device.generic_name.exact")
	if err != nil {
		return 0, 0, err
	}
	if len(counts) == 0 {
		return 0, 0, nil
	}
	vals := make([]int, 0, len(counts))
	for _, n := range counts {
		vals = append(vals, n)
	}
	sort.Ints(vals)
	p95 := vals[int(0.95*float64(len(vals)-1))]
	return p95, len(vals), nil
}
