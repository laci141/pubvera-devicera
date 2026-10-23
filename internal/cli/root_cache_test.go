package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The index page must be revalidated on every load, so a deploy is visible
// immediately instead of whenever a browser's heuristic cache expires. Without
// a Cache-Control header neither Caddy nor Cloudflare adds one.
//
// Every request goes through NewServeHandler, so the real middleware chain
// (logging, recovery, gzip) is exercised, not just the "/" closure.

func TestIndexIsRevalidatedOnEveryLoad(t *testing.T) {
	for _, path := range []string{"/", "/index.html"} {
		rec := get(NewServeHandler(), path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", path, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s: Cache-Control = %q, want %q", path, cc, "no-cache")
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s: Content-Type = %q, want text/html", path, ct)
		}
	}
}

// A client that asks for gzip still gets the index page's no-cache.
func TestIndexGzipRequestKeepsNoCache(t *testing.T) {
	rec := gzipGet(NewServeHandler(), "/", "gzip")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / (gzip): status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("GET / (gzip): Cache-Control = %q, want %q", cc, "no-cache")
	}
}

// handleConfig sets no-store on /config.json; the index fix must not change it.
func TestConfigKeepsItsCacheControl(t *testing.T) {
	withEnv(t, map[string]string{})
	rec := get(NewServeHandler(), "/config.json")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /config.json: status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("GET /config.json: Cache-Control = %q, want %q", cc, "no-store")
	}
}

func TestUnknownPathIs404(t *testing.T) {
	rec := get(NewServeHandler(), "/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nope: status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("GET /nope: Content-Type = %q, want application/json", ct)
	}
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("GET /nope: body is not JSON: %v\n%s", err, rec.Body.String())
	}
	if v["error"] != "not found" {
		t.Errorf("GET /nope: error = %v, want %q", v["error"], "not found")
	}
	if cc := rec.Header().Get("Cache-Control"); cc == "no-cache" {
		t.Errorf("GET /nope: Cache-Control = %q, want anything but %q", cc, "no-cache")
	}
}

// allowGET answers a non-GET with a JSON 405 before the page is served; that
// answer is not the page and must not carry its no-cache.
func TestIndexNonGETIsNotRevalidated(t *testing.T) {
	rec := httptest.NewRecorder()
	NewServeHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /: status = %d, want 405", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc == "no-cache" {
		t.Errorf("POST /: Cache-Control = %q, want anything but %q", cc, "no-cache")
	}
}
