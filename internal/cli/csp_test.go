package cli

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/laci141/medical-device-intelligence/web"
)

// CSP phase 1: every <script> tag in the embedded index carries
// nonce="__CSP_NONCE__", and each index response replaces that marker with a
// fresh nonce named in a Report-Only policy header. All requests go through
// NewServeHandler in-process (httptest), like root_cache_test.go.

// wantPolicy is written out here rather than taken from contentSecurityPolicy,
// so a change to any directive in serve.go fails a test.
func wantPolicy(nonce string) string {
	return "script-src 'nonce-" + nonce + "' 'strict-dynamic' https: 'unsafe-inline'; script-src-attr 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
}

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
	csp := rec.Header().Get("Content-Security-Policy-Report-Only")
	m := cspNonceRE.FindStringSubmatch(csp)
	if m == nil {
		t.Fatalf("GET %s: Content-Security-Policy-Report-Only = %q, want a 22-character nonce", path, csp)
	}
	return m[1], rec.Body.String(), rec.Header()
}

func TestCSPMarkerReplacedInResponse(t *testing.T) {
	for _, path := range []string{"/", "/index.html"} {
		nonce, body, _ := indexNonce(t, path)
		if strings.Contains(body, nonceMarkerText) {
			t.Errorf("GET %s: body still contains %s", path, nonceMarkerText)
		}
		if n := strings.Count(body, `nonce="`+nonce+`"`); n != 5 {
			t.Errorf("GET %s: %d tags carry the header nonce, want 5", path, n)
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

func TestCSPHeaderIsReportOnlyWithExactPolicy(t *testing.T) {
	nonce, _, h := indexNonce(t, "/")
	if got := h.Get("Content-Security-Policy-Report-Only"); got != wantPolicy(nonce) {
		t.Errorf("policy = %q, want %q", got, wantPolicy(nonce))
	}
	if got := h.Get("Content-Security-Policy"); got != "" {
		t.Errorf("enforcing Content-Security-Policy = %q, want none in phase 1", got)
	}
	if cspHeaderName != "Content-Security-Policy-Report-Only" {
		t.Errorf("cspHeaderName = %q", cspHeaderName)
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
	if rec.Header().Get("Content-Security-Policy-Report-Only") != "" || strings.Contains(rec.Body.String(), "<script") {
		t.Error("GET /: page or policy served despite nonce failure")
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
	if rec.Header().Get("Content-Security-Policy-Report-Only") != "" {
		t.Error("GET /nope: carries the index policy")
	}
}
