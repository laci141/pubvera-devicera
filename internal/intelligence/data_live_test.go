package intelligence

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// redirectTransport sends every request to target, so the live adapters'
// hard-coded https://api.fda.gov lands on a local httptest server.
type redirectTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (rt redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host, r.Host = rt.target.Scheme, rt.target.Host, rt.target.Host
	return rt.base.RoundTrip(r)
}

// swapDefaultTransport replaces the process-wide http.DefaultTransport for one
// test and restores it on cleanup. DefaultTransport is not env-driven; the
// t.Setenv call is purely a parallel guard: the testing package panics if a
// test that used t.Setenv calls t.Parallel, or runs under a parallel parent
// (measured on go1.26.8 and go1.27.0). A sequential test cannot overlap
// top-level parallel tests, so the guard is what keeps the swap race-free.
func swapDefaultTransport(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	t.Setenv("MDI_TEST_PARALLEL_GUARD_DEFAULT_TRANSPORT", "1")
	old := http.DefaultTransport
	http.DefaultTransport = rt
	t.Cleanup(func() { http.DefaultTransport = old })
}

// TestRecallActionsRequestsNewestFirst: RECALL_RECENCY takes the newest date
// from one page, so the page must be sorted by recall_initiation_date desc.
// Unsorted, openFDA returns index order (measured 2026-09-29: pacemaker's page
// topped out at 20260227 while the true newest was 20260427).
func TestRecallActionsRequestsNewestFirst(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/device/enforcement.json" {
			got = r.URL.Query()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"meta":{"results":{"total":1}},"results":[
			{"recall_number":"Z-1-2026","classification":"Class II","recall_initiation_date":"20260427"}]}`))
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	swapDefaultTransport(t, redirectTransport{target: target, base: http.DefaultTransport})

	acts, err := NewLiveData().RecallActions(context.Background(), "pacemaker", timelineFetch)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("no request reached /device/enforcement.json")
	}
	if s := got.Get("sort"); s != "recall_initiation_date:desc" {
		t.Errorf("sort=%q want recall_initiation_date:desc", s)
	}
	if l := got.Get("limit"); l != "25" {
		t.Errorf("limit=%q want 25", l)
	}
	if q := got.Get("search"); q != `product_description:"pacemaker"` {
		t.Errorf("search=%q", q)
	}
	if len(acts) != 1 || acts[0].Date != "20260427" || acts[0].Reference != "Z-1-2026" {
		t.Errorf("actions=%+v", acts)
	}
}
