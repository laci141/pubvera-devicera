package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
)

// recallServer answers every request with a root-cause count and records the
// query it received.
func recallServer(t *testing.T, status int) (*httptest.Server, func() (string, url.Values)) {
	t.Helper()
	var mu sync.Mutex
	var path string
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path, query = r.URL.Path, r.URL.Query()
		mu.Unlock()
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"results":[{"term":"Device Design","count":69},{"term":"Other","count":71}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"No matches found!"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() (string, url.Values) {
		mu.Lock()
		defer mu.Unlock()
		return path, query
	}
}

func TestRecallCountFieldRequest(t *testing.T) {
	restore := cliutil.SetOpenFDAKey("")
	defer restore()
	srv, got := recallServer(t, http.StatusOK)
	s := newOpenFDADeviceRecallAt(srv.URL)
	counts, err := s.CountField(context.Background(), Query{Term: "pacemaker"}, "root_cause_description.exact")
	if err != nil {
		t.Fatal(err)
	}
	if counts["Device Design"] != 69 || counts["Other"] != 71 {
		t.Errorf("counts=%v", counts)
	}
	path, q := got()
	if path != "/device/recall.json" {
		t.Errorf("path=%q want /device/recall.json", path)
	}
	if q.Get("count") != "root_cause_description.exact" {
		t.Errorf("count=%q want root_cause_description.exact", q.Get("count"))
	}
	if q.Get("search") != `product_description:"pacemaker"` {
		t.Errorf("search=%q", q.Get("search"))
	}
	if q.Has("limit") {
		t.Errorf("a count request must not send limit: %v", q)
	}
	if q.Has("api_key") {
		t.Errorf("keyless client must not send api_key: %v", q)
	}
}

func TestRecallCountFieldSendsKeyWhenSet(t *testing.T) {
	restore := cliutil.SetOpenFDAKey("test-key")
	defer restore()
	srv, got := recallServer(t, http.StatusOK)
	s := newOpenFDADeviceRecallAt(srv.URL)
	if _, err := s.CountField(context.Background(), Query{Term: "pacemaker"}, "root_cause_description.exact"); err != nil {
		t.Fatal(err)
	}
	if _, q := got(); q.Get("api_key") != "test-key" {
		t.Errorf("api_key=%q want test-key", q.Get("api_key"))
	}
}

func TestRecallCountField404IsEmpty(t *testing.T) {
	srv, _ := recallServer(t, http.StatusNotFound)
	s := newOpenFDADeviceRecallAt(srv.URL)
	counts, err := s.CountField(context.Background(), Query{Term: "zzznope"}, "root_cause_description.exact")
	if err != nil || len(counts) != 0 {
		t.Errorf("404 must be empty data, got %v / %v", counts, err)
	}
}
