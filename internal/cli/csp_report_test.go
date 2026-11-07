package cli

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// connect-src PR A (Report-Only): the collector tests are ported from
// Praxvera's csp_report_test.go. Every request goes through NewServeHandler,
// which also proves the route is registered.

// captureCSPLog sends the standard logger to a buffer without timestamps and
// gives handleCSPReport a fresh flood gate on the given clock.
func captureCSPLog(t *testing.T, now func() time.Time) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags, prevGate := log.Writer(), log.Flags(), cspGate
	log.SetOutput(&buf)
	log.SetFlags(0)
	cspGate = newCSPLogGate(now)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		cspGate = prevGate
	})
	return &buf
}

func postCSPReport(contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/csp-report", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	NewServeHandler().ServeHTTP(rec, req)
	return rec
}

func fixedClock() func() time.Time {
	t0 := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t0 }
}

func logLines(buf *bytes.Buffer) []string {
	s := strings.TrimSuffix(buf.String(), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// A report-to (application/reports+json) array: one log line per csp-violation
// report, other report types ignored, origin of blockedURL, path of documentURL.
const reportsJSONBody = `[
 {"type":"deprecation","url":"https://devicera.pubvera.com/","body":{"id":"x"}},
 {"type":"csp-violation","age":10,"url":"https://devicera.pubvera.com/search?q=1","user_agent":"Mozilla/5.0 Full UA",
  "body":{"effectiveDirective":"connect-src","blockedURL":"https://evil.example/collect?code=SECRET&x=1#frag",
   "documentURL":"https://devicera.pubvera.com/search?q=pacemaker#top","disposition":"report","statusCode":200,"lineNumber":3}}
]`

// A report-uri (application/csp-report) object.
const cspReportBody = `{"csp-report":{"document-uri":"https://devicera.pubvera.com/?token=SECRET","referrer":"",
 "violated-directive":"connect-src","effective-directive":"connect-src","original-policy":"connect-src 'self'",
 "disposition":"report","blocked-uri":"https://abc.supabase.co:8443/auth/v1/logout?scope=global","status-code":200}}`

func TestCSPReportValidBodies(t *testing.T) {
	for _, tc := range []struct {
		name, ct, body, want string
	}{
		{"reports+json", "application/reports+json", reportsJSONBody,
			"csp-violation disposition=report directive=connect-src blocked=https://evil.example page=/search"},
		{"reports+json, mixed case with parameter", "Application/Reports+JSON; charset=utf-8", reportsJSONBody,
			"csp-violation disposition=report directive=connect-src blocked=https://evil.example page=/search"},
		{"csp-report", "application/csp-report", cspReportBody,
			"csp-violation disposition=report directive=connect-src blocked=https://abc.supabase.co:8443 page=/"},
		{"keyword blocked value", "application/csp-report",
			`{"csp-report":{"document-uri":"https://devicera.pubvera.com/a/b","effective-directive":"script-src-elem","disposition":"enforce","blocked-uri":"inline"}}`,
			"csp-violation disposition=enforce directive=script-src-elem blocked=inline page=/a/b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureCSPLog(t, fixedClock())
			rec := postCSPReport(tc.ct, tc.body)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want empty", rec.Body.String())
			}
			if got := logLines(buf); len(got) != 1 || got[0] != tc.want {
				t.Errorf("log lines = %q, want [%q]", got, tc.want)
			}
			for _, bad := range []string{"SECRET", "code=", "Mozilla", "pacemaker", "#", "?"} {
				if strings.Contains(buf.String(), bad) {
					t.Errorf("log %q contains %q", buf.String(), bad)
				}
			}
		})
	}
}

func TestCSPReportRejects(t *testing.T) {
	buf := captureCSPLog(t, fixedClock())

	rec := get(NewServeHandler(), "/csp-report")
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Errorf("GET status = %d Allow = %q, want 405 and %q", rec.Code, rec.Header().Get("Allow"), "POST")
	}

	for _, ct := range []string{"text/plain", "", "application/json", "application/reports+json;;"} {
		if rec := postCSPReport(ct, reportsJSONBody); rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q status = %d, want 415", ct, rec.Code)
		}
	}

	for _, ct := range []string{"application/reports+json", "application/csp-report"} {
		if rec := postCSPReport(ct, `{"csp-report":`); rec.Code != http.StatusBadRequest {
			t.Errorf("%s invalid JSON status = %d, want 400", ct, rec.Code)
		}
	}

	// Exactly the limit is still read; one byte more is refused.
	limit := 16 << 10
	if rec := postCSPReport("application/reports+json", "[]"+strings.Repeat(" ", limit-2)); rec.Code != http.StatusNoContent {
		t.Errorf("body of exactly %d bytes status = %d, want 204", limit, rec.Code)
	}
	if rec := postCSPReport("application/reports+json", "[]"+strings.Repeat(" ", limit-1)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("body of %d bytes status = %d, want 413", limit+1, rec.Code)
	}

	if buf.Len() != 0 {
		t.Errorf("rejected requests logged %q, want nothing", buf.String())
	}
}

// Report values are attacker-controlled: a newline must not start a second,
// forged log line, and every value is capped.
func TestCSPReportSanitizesValues(t *testing.T) {
	buf := captureCSPLog(t, fixedClock())
	long := strings.Repeat("a", 500)
	body := `[{"type":"csp-violation","body":{"effectiveDirective":"connect-src\ncsp-violation disposition=forged\r\u0000\u001b[31m","blockedURL":"eval",` +
		`"documentURL":"https://devicera.pubvera.com/` + long + `","disposition":"` + long + `"}}]`
	if rec := postCSPReport("application/reports+json", body); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	got := logLines(buf)
	if len(got) != 1 {
		t.Fatalf("log lines = %q, want exactly one", got)
	}
	want := "csp-violation disposition=" + long[:200] + " directive=connect-srccsp-violation disposition=forged[31m blocked=eval page=/" + long[:199]
	if got[0] != want {
		t.Errorf("log line = %q, want %q", got[0], want)
	}
}

func TestCSPReportProcessesAtMost20(t *testing.T) {
	buf := captureCSPLog(t, fixedClock())
	one := `{"type":"csp-violation","body":{"effectiveDirective":"connect-src","blockedURL":"https://x.example/","documentURL":"https://g.example/","disposition":"report"}}`
	body := "[" + strings.TrimSuffix(strings.Repeat(one+",", 25), ",") + "]"
	if rec := postCSPReport("application/reports+json", body); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(logLines(buf)); n != 20 {
		t.Errorf("25 reports in one body logged %d lines, want 20", n)
	}
}

// 61 reports in one window: 60 lines, then the 61st is only counted. The
// window that had a drop says so once, when the next window begins.
func TestCSPReportFloodCap(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	buf := captureCSPLog(t, func() time.Time { return now })
	one := `{"csp-report":{"document-uri":"https://g.example/","effective-directive":"connect-src","disposition":"report","blocked-uri":"https://x.example/"}}`
	line := "csp-violation disposition=report directive=connect-src blocked=https://x.example page=/"
	for i := 0; i < 61; i++ {
		if rec := postCSPReport("application/csp-report", one); rec.Code != http.StatusNoContent {
			t.Fatalf("report %d status = %d, want 204", i+1, rec.Code)
		}
		now = now.Add(500 * time.Millisecond) // all 61 inside one minute
	}
	got := logLines(buf)
	if len(got) != 60 {
		t.Fatalf("61 reports in one window logged %d lines, want 60", len(got))
	}
	for _, l := range got {
		if l != line {
			t.Fatalf("log line = %q, want %q", l, line)
		}
	}

	now = now.Add(time.Minute)
	if rec := postCSPReport("application/csp-report", one); rec.Code != http.StatusNoContent {
		t.Fatalf("status after the window = %d, want 204", rec.Code)
	}
	got = logLines(buf)
	if len(got) != 62 || got[60] != "csp-violation dropped=1" || got[61] != line {
		t.Errorf("after the window, lines 61-62 = %q, want [%q %q] (total %d, want 62)", got[60:], "csp-violation dropped=1", line, len(got))
	}
}

// The collector's own response carries no policy header of any kind.
func TestCSPReportResponseHasNoCSP(t *testing.T) {
	captureCSPLog(t, fixedClock())
	rec := postCSPReport("application/csp-report", cspReportBody)
	for _, name := range []string{"Content-Security-Policy", "Content-Security-Policy-Report-Only", "Reporting-Endpoints"} {
		if got := rec.Header().Values(name); len(got) != 0 {
			t.Errorf("POST /csp-report: %s = %q, want none", name, got)
		}
	}
}

func TestSupabaseConfig(t *testing.T) {
	for _, tc := range []struct {
		name, envURL, envKey      string
		wantURL, wantKey, wantOri string
	}{
		{"empty", "", "", "", "", ""},
		{"only url", "https://abc.supabase.co", "", "", "", ""},
		{"only key", "", "pk_123", "", "", ""},
		{"blank url is empty", "   ", "pk_123", "", "", ""},
		{"http url", "http://abc.supabase.co", "pk_123", "http://abc.supabase.co", "pk_123", ""},
		{"https url", " https://abc.supabase.co ", " pk_123 ", "https://abc.supabase.co", "pk_123", "https://abc.supabase.co"},
		{"https url with path, query, userinfo and port", "https://user:pw@abc.supabase.co:8443/rest/v1?x=1#f", "pk_123",
			"https://user:pw@abc.supabase.co:8443/rest/v1?x=1#f", "pk_123", "https://abc.supabase.co:8443"},
		{"host with ;", "https://abc.supabase.co;script-src", "pk_123", "https://abc.supabase.co;script-src", "pk_123", ""},
		{"bad host with space", "https://abc supabase.co", "pk_123", "https://abc supabase.co", "pk_123", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withEnv(t, map[string]string{"SUPABASE_URL": tc.envURL, "SUPABASE_PUBLISHABLE_KEY": tc.envKey})
			u, k, o := supabaseConfig()
			if u != tc.wantURL || k != tc.wantKey || o != tc.wantOri {
				t.Errorf("supabaseConfig() = (%q, %q, %q), want (%q, %q, %q)", u, k, o, tc.wantURL, tc.wantKey, tc.wantOri)
			}
		})
	}
}

func TestCSPConnectSrcReportOnlyLiteral(t *testing.T) {
	if got, want := cspConnectSrcReportOnly(""), "connect-src 'self'; report-to csp; report-uri /csp-report"; got != want {
		t.Errorf("cspConnectSrcReportOnly(\"\") = %q, want %q", got, want)
	}
	if got, want := cspConnectSrcReportOnly("https://abc.supabase.co"), "connect-src 'self' https://abc.supabase.co; report-to csp; report-uri /csp-report"; got != want {
		t.Errorf("cspConnectSrcReportOnly(origin) = %q, want %q", got, want)
	}
}
