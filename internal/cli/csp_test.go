package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/laci141/medical-device-intelligence/web"
)

// CSP phase 2: every <script> tag in the embedded index carries
// nonce="__CSP_NONCE__", and each index response replaces that marker with a
// fresh nonce named in the enforced Content-Security-Policy header. All requests go through
// NewServeHandler in-process (httptest), like root_cache_test.go.

// wantPolicy is written out here rather than taken from contentSecurityPolicy,
// so a change to any directive in serve.go fails a test. Style phase S2: the
// style directives are enforced in the same header, with the same nonce.
func wantPolicy(nonce string) string {
	return "script-src 'nonce-" + nonce + "' 'strict-dynamic' https: 'unsafe-inline'; script-src-attr 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; style-src 'nonce-" + nonce + "'; style-src-attr 'none'"
}

var styleOpenRE = regexp.MustCompile(`<style\b[^>]*>`)

// nonceMarkerText is spelled out so a renamed constant cannot agree with itself.
const nonceMarkerText = "__CSP_NONCE__"

// 16 random bytes in raw standard base64 are exactly 22 characters.
var cspNonceRE = regexp.MustCompile(`^script-src 'nonce-([A-Za-z0-9+/]{22})' `)

var scriptOpenRE = regexp.MustCompile(`<script\b[^>]*>`)

var bodyNonceRE = regexp.MustCompile(`nonce="([^"]*)"`)

// indexNonce fetches path and returns the header nonce, body and headers.
func indexNonce(t *testing.T, path string) (string, string, http.Header) {
	t.Helper()
	rec := get(NewServeHandler(), path)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", path, rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	m := cspNonceRE.FindStringSubmatch(csp)
	if m == nil {
		t.Fatalf("GET %s: Content-Security-Policy = %q, want a 22-character nonce", path, csp)
	}
	return m[1], rec.Body.String(), rec.Header()
}

func TestCSPMarkerReplacedInResponse(t *testing.T) {
	for _, path := range []string{"/", "/index.html"} {
		nonce, body, _ := indexNonce(t, path)
		if strings.Contains(body, nonceMarkerText) {
			t.Errorf("GET %s: body still contains %s", path, nonceMarkerText)
		}
		if n := strings.Count(body, `nonce="`+nonce+`"`); n != 6 {
			t.Errorf("GET %s: %d tags carry the header nonce, want 6 (5 script, 1 style)", path, n)
		}
	}
}

func TestCSPNonceIsFreshPerResponse(t *testing.T) {
	a, _, _ := indexNonce(t, "/")
	b, _, _ := indexNonce(t, "/")
	if a == b {
		t.Errorf("two responses carried the same nonce %q", a)
	}
}

func TestCSPHeaderNonceMatchesEveryBodyNonce(t *testing.T) {
	nonce, body, _ := indexNonce(t, "/")
	tags := scriptOpenRE.FindAllString(body, -1)
	if len(tags) != 5 {
		t.Errorf("GET /: %d <script> tags, want 5", len(tags))
	}
	for _, tag := range tags {
		if !strings.Contains(tag, ` nonce="`+nonce+`"`) {
			t.Errorf("GET /: tag %s does not carry the header nonce %q", tag, nonce)
		}
	}
	for _, m := range bodyNonceRE.FindAllStringSubmatch(body, -1) {
		if m[1] != nonce {
			t.Errorf("GET /: body nonce %q differs from header nonce %q", m[1], nonce)
		}
	}
}

func TestCSPHeaderIsEnforcedWithExactPolicy(t *testing.T) {
	nonce, _, h := indexNonce(t, "/")
	if got := h.Get("Content-Security-Policy"); got != wantPolicy(nonce) {
		t.Errorf("Content-Security-Policy = %q, want %q", got, wantPolicy(nonce))
	}
	if cspHeaderName != "Content-Security-Policy" {
		t.Errorf("cspHeaderName = %q", cspHeaderName)
	}
}

// TestCSPIndexSendsEnforcingAndConnectSrcReportOnly checks the header counts
// directly: one enforced policy, unchanged since style S2, and one Report-Only
// policy that carries connect-src alone (connect-src PR A).
func TestCSPIndexSendsEnforcingAndConnectSrcReportOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		wantRO string
	}{
		{"with supabase", map[string]string{"SUPABASE_URL": "https://dgoksxpnzsvpnhlpcalx.supabase.co", "SUPABASE_PUBLISHABLE_KEY": "sb_publishable_test"},
			"connect-src 'self' https://dgoksxpnzsvpnhlpcalx.supabase.co; report-to csp; report-uri /csp-report"},
		{"without supabase", map[string]string{},
			"connect-src 'self'; report-to csp; report-uri /csp-report"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withEnv(t, tc.env)
			nonce, _, h := indexNonce(t, "/")
			if got := h.Values("Content-Security-Policy"); len(got) != 1 || got[0] != wantPolicy(nonce) {
				t.Errorf("GET /: Content-Security-Policy values %q, want exactly [%q]", got, wantPolicy(nonce))
			}
			if got := h.Values("Content-Security-Policy-Report-Only"); len(got) != 1 || got[0] != tc.wantRO {
				t.Errorf("GET /: Content-Security-Policy-Report-Only values %q, want exactly [%q]", got, tc.wantRO)
			}
			if got := h.Values("Reporting-Endpoints"); len(got) != 1 || got[0] != `csp="/csp-report"` {
				t.Errorf("GET /: Reporting-Endpoints values %q, want exactly [%q]", got, `csp="/csp-report"`)
			}
		})
	}
}

// The page connects to the URL /config.json hands it; the Report-Only header
// must name exactly that URL's origin. Whitespace around the values is the
// case where two separate readers of the environment would disagree.
func TestCSPConfigJSONAgreesWithReportOnlyHeader(t *testing.T) {
	for _, tc := range []struct{ envURL, envKey, wantURL, wantRO string }{
		{" https://dgoksxpnzsvpnhlpcalx.supabase.co ", " pk_test ", "https://dgoksxpnzsvpnhlpcalx.supabase.co",
			"connect-src 'self' https://dgoksxpnzsvpnhlpcalx.supabase.co; report-to csp; report-uri /csp-report"},
		{"https://dgoksxpnzsvpnhlpcalx.supabase.co", "", "", "connect-src 'self'; report-to csp; report-uri /csp-report"},
		{"", "", "", "connect-src 'self'; report-to csp; report-uri /csp-report"},
	} {
		withEnv(t, map[string]string{"SUPABASE_URL": tc.envURL, "SUPABASE_PUBLISHABLE_KEY": tc.envKey})
		h := NewServeHandler()
		cfgRec := get(h, "/config.json")
		var cfg browserConfig
		if err := json.Unmarshal(cfgRec.Body.Bytes(), &cfg); err != nil {
			t.Fatalf("env %q: /config.json body %q: %v", tc.envURL, cfgRec.Body.String(), err)
		}
		if cfg.SupabaseURL != tc.wantURL {
			t.Errorf("env %q: /config.json supabase_url = %q, want %q", tc.envURL, cfg.SupabaseURL, tc.wantURL)
		}
		ro := get(h, "/").Header().Get("Content-Security-Policy-Report-Only")
		if ro != tc.wantRO {
			t.Errorf("env %q: Report-Only = %q, want %q", tc.envURL, ro, tc.wantRO)
		}
		if cfg.SupabaseURL != "" && !strings.Contains(ro, " "+cfg.SupabaseURL+";") {
			t.Errorf("env %q: Report-Only %q does not allow the /config.json url %q", tc.envURL, ro, cfg.SupabaseURL)
		}
	}
}

func TestCSPStyleTagCarriesHeaderNonce(t *testing.T) {
	nonce, body, _ := indexNonce(t, "/")
	tags := styleOpenRE.FindAllString(body, -1)
	if len(tags) != 1 {
		t.Fatalf("GET /: %d <style> tags, want 1", len(tags))
	}
	if !strings.Contains(tags[0], ` nonce="`+nonce+`"`) {
		t.Errorf("GET /: tag %s does not carry the header nonce %q", tags[0], nonce)
	}
}

func TestCSPEmbeddedIndexMarksTheStyle(t *testing.T) {
	tags := styleOpenRE.FindAll(web.Index, -1)
	if len(tags) != 1 {
		t.Fatalf("embedded index: %d <style> tags, want 1", len(tags))
	}
	if !bytes.Contains(tags[0], []byte(` nonce="`+nonceMarkerText+`"`)) {
		t.Errorf("embedded index: tag %s has no nonce marker", tags[0])
	}
	if bytes.Contains(web.Index, []byte(`style=`)) {
		t.Errorf("embedded index: contains style=, which style-src-attr 'none' would block")
	}
}

func TestCSPIndexIsNoStore(t *testing.T) {
	for _, path := range []string{"/", "/index.html"} {
		_, _, h := indexNonce(t, path)
		if cc := h.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", path, cc)
		}
	}
}

func TestCSPEmbeddedIndexUnchangedByRequests(t *testing.T) {
	before := sha256.Sum256(web.Index)
	for i := 0; i < 3; i++ {
		indexNonce(t, "/")
	}
	if sha256.Sum256(web.Index) != before {
		t.Fatal("web.Index changed after serving requests")
	}
	if !bytes.Contains(web.Index, []byte(nonceMarkerText)) {
		t.Fatal("web.Index lost its nonce marker")
	}
}

func TestCSPEmbeddedIndexMarksEveryScript(t *testing.T) {
	tags := scriptOpenRE.FindAll(web.Index, -1)
	if len(tags) != 5 {
		t.Errorf("embedded index: %d <script> tags, want 5", len(tags))
	}
	for _, tag := range tags {
		if !bytes.Contains(tag, []byte(` nonce="`+nonceMarkerText+`"`)) {
			t.Errorf("embedded index: tag %s has no nonce marker", tag)
		}
	}
}

func TestCSPRandFailureIs500(t *testing.T) {
	orig := nonceRandRead
	nonceRandRead = func([]byte) (int, error) { return 0, errors.New("no entropy") }
	defer func() { nonceRandRead = orig }()
	rec := get(NewServeHandler(), "/")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /: status = %d, want 500", rec.Code)
	}
	for _, name := range []string{"Content-Security-Policy", "Content-Security-Policy-Report-Only", "Reporting-Endpoints"} {
		if got := rec.Header().Get(name); got != "" {
			t.Errorf("GET /: %s = %q, want none", name, got)
		}
	}
	if strings.Contains(rec.Body.String(), "<script") {
		t.Errorf("GET /: body contains <script despite nonce failure")
	}
}

func TestCSPUnknownPathStillJSON404(t *testing.T) {
	rec := get(NewServeHandler(), "/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nope: status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("GET /nope: Content-Type = %q", ct)
	}
	for _, name := range []string{"Content-Security-Policy", "Content-Security-Policy-Report-Only", "Reporting-Endpoints"} {
		if got := rec.Header().Get(name); got != "" {
			t.Errorf("GET /nope: %s = %q, want none", name, got)
		}
	}
}
