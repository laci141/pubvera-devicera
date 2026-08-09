package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// getJSON fetches a URL and decodes the JSON body, failing the test on any
// transport or decode error.
func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET %s: Content-Type=%q want application/json", url, ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", url, err)
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("GET %s: body is not JSON: %v\n%s", url, err, body)
	}
	return resp.StatusCode, v
}

func TestServeHealth(t *testing.T) {
	ts := httptest.NewServer(NewServeHandler())
	defer ts.Close()

	status, v := getJSON(t, ts.URL+"/api/health")
	if status != http.StatusOK {
		t.Fatalf("health status=%d want 200", status)
	}
	if v["status"] != "ok" {
		t.Errorf("health status field=%v want ok", v["status"])
	}
	if v["commands"] != float64(len(commands)) {
		t.Errorf("health commands=%v want %d (the live registry size)", v["commands"], len(commands))
	}
	if v["modules"] != float64(moduleCount) {
		t.Errorf("health modules=%v want %d", v["modules"], moduleCount)
	}
	if _, ok := v["disclaimer"]; !ok {
		t.Error("health must carry the disclaimer")
	}
}

func TestServeSignalsWithDevice(t *testing.T) {
	withDossier(t, sampleDossier(), nil)
	ts := httptest.NewServer(NewServeHandler())
	defer ts.Close()

	status, v := getJSON(t, ts.URL+"/api/signals?device=pacemaker")
	if status != http.StatusOK {
		t.Fatalf("signals status=%d want 200: %v", status, v)
	}
	if _, ok := v["records"]; !ok {
		t.Error("signals must return the --json records envelope")
	}
	if _, ok := v["disclaimer"]; !ok {
		t.Error("signals must carry the disclaimer")
	}
	sum, _ := v["summary"].(map[string]any)
	if sum["device"] != "pacemaker" {
		t.Errorf("signals summary device=%v want pacemaker", sum["device"])
	}
}

func TestServeSignalsMissingDevice400(t *testing.T) {
	withDossier(t, sampleDossier(), nil)
	ts := httptest.NewServer(NewServeHandler())
	defer ts.Close()

	status, v := getJSON(t, ts.URL+"/api/signals")
	if status != http.StatusBadRequest {
		t.Fatalf("signals without device status=%d want 400", status)
	}
	if v["error"] != "device required" {
		t.Errorf("error=%v want %q", v["error"], "device required")
	}
}

func TestServeDossierWithDevice(t *testing.T) {
	withDossier(t, sampleDossier(), nil)
	ts := httptest.NewServer(NewServeHandler())
	defer ts.Close()

	status, v := getJSON(t, ts.URL+"/api/dossier?device=pacemaker")
	if status != http.StatusOK {
		t.Fatalf("dossier status=%d want 200: %v", status, v)
	}
	if v["attention_index"] != 0.47 {
		t.Errorf("attention_index=%v want 0.47", v["attention_index"])
	}
}

func TestServeCompareMissingParam400(t *testing.T) {
	ts := httptest.NewServer(NewServeHandler())
	defer ts.Close()

	status, v := getJSON(t, ts.URL+"/api/compare?a=pacemaker")
	if status != http.StatusBadRequest {
		t.Fatalf("compare missing b status=%d want 400", status)
	}
	if v["error"] != "b required" {
		t.Errorf("error=%v want %q", v["error"], "b required")
	}
}

func TestServeUnknownRoute404(t *testing.T) {
	ts := httptest.NewServer(NewServeHandler())
	defer ts.Close()

	status, v := getJSON(t, ts.URL+"/api/nonexistent")
	if status != http.StatusNotFound {
		t.Fatalf("unknown route status=%d want 404", status)
	}
	if v["error"] != "not found" {
		t.Errorf("error=%v want %q", v["error"], "not found")
	}
}

func TestServeNonGET405(t *testing.T) {
	ts := httptest.NewServer(NewServeHandler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/health", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST health status=%d want 405", resp.StatusCode)
	}
}

func TestServeCORSHeader(t *testing.T) {
	ts := httptest.NewServer(NewServeHandler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin=%q want *", got)
	}
}

func TestServeSynthesizeErrorIsJSONNotPanic(t *testing.T) {
	withDossier(t, nil, io.ErrUnexpectedEOF)
	ts := httptest.NewServer(NewServeHandler())
	defer ts.Close()

	status, v := getJSON(t, ts.URL+"/api/signals?device=pacemaker")
	if status != http.StatusBadGateway {
		t.Fatalf("signals with failing module status=%d want 502", status)
	}
	errMsg, _ := v["error"].(string)
	if errMsg == "" {
		t.Error("module failure must surface as a JSON error field")
	}
}

func TestServeRootServesFrontend(t *testing.T) {
	ts := httptest.NewServer(NewServeHandler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status=%d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type=%q want text/html", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET /: read body: %v", err)
	}
	for _, want := range []string{"<!DOCTYPE html>", "Devicera", "/api/signals"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("frontend HTML missing %q", want)
		}
	}
}

func TestServeSignalsJSONFullReasoning(t *testing.T) {
	d := sampleDossier()
	long := strings.Repeat("x", 200) // well past the 90-char plain-mode clip
	d.Signals[0].Reasoning = long
	withDossier(t, d, nil)
	ts := httptest.NewServer(NewServeHandler())
	defer ts.Close()

	status, v := getJSON(t, ts.URL+"/api/signals?device=pacemaker")
	if status != http.StatusOK {
		t.Fatalf("signals status=%d want 200", status)
	}
	recs, _ := v["records"].([]any)
	if len(recs) == 0 {
		t.Fatal("signals returned no records")
	}
	first, _ := recs[0].(map[string]any)
	got, _ := first["reasoning"].(string)
	if got != long {
		t.Errorf("JSON reasoning clipped: len=%d want %d (must be the full text)", len(got), len(long))
	}
}

func TestServeCommandRegistered(t *testing.T) {
	if _, ok := commands["serve"]; !ok {
		t.Error("command \"serve\" not registered")
	}
}

// deviceRow must translate every device_class code — a raw one-character code
// ("U", "N", "1", …) reaching the table or the exports is a bug.
func TestDeviceRowClassNeverRaw(t *testing.T) {
	rawFor := func(class any) map[string]any {
		pc := map[string]any{"openfda": map[string]any{"device_class": class}}
		return map[string]any{"product_codes": []any{pc}}
	}
	cases := map[string]string{
		"1": "Class I",
		"2": "Class II",
		"3": "Class III",
		"U": "Unclassified",
		"N": "Unclassified",
		"f": "Unclassified",
		"":  "Not specified",
	}
	for in, want := range cases {
		got, _ := deviceRow(rawFor(in))["device_class"].(string)
		if got != want {
			t.Errorf("device_class %q => %q, want %q", in, got, want)
		}
		if len(got) <= 1 {
			t.Errorf("device_class %q leaked as raw code %q", in, got)
		}
	}
	// No product_codes at all must still yield a translated value.
	if got, _ := deviceRow(map[string]any{})["device_class"].(string); got != "Not specified" {
		t.Errorf("missing product_codes => %q, want \"Not specified\"", got)
	}
}

// The category chips are pure frontend logic, so this test extracts the
// marker-delimited buildCategoryChips function from web/index.html and runs
// its invariants under node: chip counts must sum to the row count exactly
// (no row lost, none double-counted), the empty category must survive the
// chip cap as "Uncategorised", and no category may appear in two chips.
func TestCategoryChipsSumInvariant(t *testing.T) {
	nodeExe, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; chip-logic invariant needs a JS runtime")
	}
	html, err := os.ReadFile(filepath.Join("..", "..", "web", "index.html"))
	if err != nil {
		t.Fatalf("read web/index.html: %v", err)
	}
	s := string(html)
	start := strings.Index(s, "// chips-logic-start")
	end := strings.Index(s, "// chips-logic-end")
	if start < 0 || end < start {
		t.Fatal("chips-logic markers missing from web/index.html")
	}
	script := s[start:end] + `
const mk = (cat, n) => Array.from({ length: n }, () => ({ 'Product Category': cat }));
const cases = [
  [].concat(mk('A', 40), mk('B', 30), mk('', 14), mk('C', 9), mk('D', 7)),
  // 12 distinct categories: exceeds the cap of 8, and the 1-row empty
  // category must still surface instead of folding into Other.
  [].concat(...'ABCDEFGHIJK'.split('').map((c, i) => mk(c, 12 - i)), mk('', 1)),
  mk('', 5),
  [],
];
for (const rows of cases) {
  const chips = buildCategoryChips(rows, 8);
  const sum = chips.reduce((a, c) => a + c.count, 0);
  if (sum !== rows.length) { console.error('SUM MISMATCH', sum, '!=', rows.length); process.exit(1); }
  const covered = new Set();
  for (const ch of chips) for (const c of ch.cats) {
    if (covered.has(c)) { console.error('CATEGORY IN TWO CHIPS:', JSON.stringify(c)); process.exit(1); }
    covered.add(c);
  }
  if (rows.some(r => r['Product Category'] === '') && !chips.some(ch => ch.label === 'Uncategorised')) {
    console.error('EMPTY CATEGORY FOLDED AWAY'); process.exit(1);
  }
  if (chips.length > 9) { console.error('TOO MANY CHIPS:', chips.length); process.exit(1); }
}
console.log('ok');
`
	out, err := exec.Command(nodeExe, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("chip invariant failed: %v\n%s", err, out)
	}
}

// mergeDeviceRows must fold a UDI present in both search legs into ONE row
// labeled "Both", keep distinct and UDI-less rows, and respect the cap.
func TestMergeDeviceRowsDedup(t *testing.T) {
	row := func(udi string) map[string]any { return map[string]any{"udi": udi} }

	brand := []map[string]any{row("A"), row("B"), row("")}
	category := []map[string]any{row("B"), row("C"), row("")}
	rows, dups := mergeDeviceRows(brand, category, 100)

	if len(rows) != 5 {
		t.Fatalf("merged %d rows, want 5 (A, B, two UDI-less, C)", len(rows))
	}
	if dups != 1 {
		t.Errorf("dups=%d want 1 (only B overlaps)", dups)
	}
	byUDI := map[string][]string{}
	empties := []string{}
	for _, r := range rows {
		udi, _ := r["udi"].(string)
		m, _ := r["matched_on"].(string)
		if udi == "" {
			empties = append(empties, m)
			continue
		}
		byUDI[udi] = append(byUDI[udi], m)
	}
	if len(byUDI["B"]) != 1 || byUDI["B"][0] != "Both" {
		t.Errorf("UDI B => %v, want exactly one row matched_on \"Both\"", byUDI["B"])
	}
	if len(byUDI["A"]) != 1 || byUDI["A"][0] != "Brand name" {
		t.Errorf("UDI A => %v, want one \"Brand name\" row", byUDI["A"])
	}
	if len(byUDI["C"]) != 1 || byUDI["C"][0] != "Product category" {
		t.Errorf("UDI C => %v, want one \"Product category\" row", byUDI["C"])
	}
	// Two records without a UDI must not be collapsed onto each other.
	if len(empties) != 2 {
		t.Errorf("UDI-less rows collapsed: got %d, want 2 (%v)", len(empties), empties)
	}

	// Cap: 3 unique rows, max 2 → exactly 2 out.
	capped, _ := mergeDeviceRows([]map[string]any{row("A"), row("B")}, []map[string]any{row("C")}, 2)
	if len(capped) != 2 {
		t.Errorf("cap ignored: got %d rows, want 2", len(capped))
	}
}

// openFDA's UDI endpoint serializes booleans as the strings "true"/"false";
// sterilization and latex labeling must be read from either representation.
func TestDeviceRowStringBooleans(t *testing.T) {
	raw := map[string]any{
		"sterilization":        map[string]any{"is_sterile": "true"},
		"is_labeled_as_no_nrl": "true",
	}
	row := deviceRow(raw)
	if row["sterilization"] != "Sterile" {
		t.Errorf("string is_sterile=true => %q, want \"Sterile\"", row["sterilization"])
	}
	if row["latex"] != "Labeled latex-free" {
		t.Errorf("string is_labeled_as_no_nrl=true => %q, want \"Labeled latex-free\"", row["latex"])
	}

	raw = map[string]any{
		"sterilization":        map[string]any{"is_sterile": false},
		"is_labeled_as_no_nrl": "false",
	}
	row = deviceRow(raw)
	if row["sterilization"] != "Non-sterile" {
		t.Errorf("bool is_sterile=false => %q, want \"Non-sterile\"", row["sterilization"])
	}
	if row["latex"] != "Not labeled latex-free" {
		t.Errorf("string is_labeled_as_no_nrl=false => %q, want \"Not labeled latex-free\"", row["latex"])
	}
}

// withEnv swaps the getenv indirection for a fixed map for one test, so the
// config route can be exercised without mutating the real process environment.
func withEnv(t *testing.T, env map[string]string) {
	t.Helper()
	prev := getenv
	getenv = func(k string) string { return env[k] }
	t.Cleanup(func() { getenv = prev })
}

// /config.json is the browser bootstrap: it must be reachable OUTSIDE /api/,
// must never be cached, and must answer 200 with an empty pair when the
// environment is not set (the page then runs unauthenticated).
func TestServeConfigJSON(t *testing.T) {
	cases := []struct {
		name        string
		env         map[string]string
		wantURL     string
		wantAnonKey string
	}{
		{"configured",
			map[string]string{"SUPABASE_URL": "https://proj.supabase.co", "SUPABASE_PUBLISHABLE_KEY": "pk_test"},
			"https://proj.supabase.co", "pk_test"},
		{"unset", map[string]string{}, "", ""},
		// Half a config is no config: a client built from it could not sign
		// anyone in, so both fields blank out together.
		{"key missing", map[string]string{"SUPABASE_URL": "https://proj.supabase.co"}, "", ""},
		{"url missing", map[string]string{"SUPABASE_PUBLISHABLE_KEY": "pk_test"}, "", ""},
		// Whitespace-only values are an unset variable that survived a shell.
		{"blank strings", map[string]string{"SUPABASE_URL": "  ", "SUPABASE_PUBLISHABLE_KEY": "  "}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withEnv(t, tc.env)
			ts := httptest.NewServer(NewServeHandler())
			defer ts.Close()

			resp, err := http.Get(ts.URL + "/config.json")
			if err != nil {
				t.Fatalf("GET /config.json: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d want 200 (a missing config is not an error)", resp.StatusCode)
			}
			if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control=%q want no-store", cc)
			}
			var v map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
				t.Fatalf("body is not JSON: %v", err)
			}
			if v["supabase_url"] != tc.wantURL {
				t.Errorf("supabase_url=%v want %q", v["supabase_url"], tc.wantURL)
			}
			if v["supabase_anon_key"] != tc.wantAnonKey {
				t.Errorf("supabase_anon_key=%v want %q", v["supabase_anon_key"], tc.wantAnonKey)
			}
		})
	}
}

// The config route is a plain GET like every other endpoint.
func TestServeConfigNonGET405(t *testing.T) {
	ts := httptest.NewServer(NewServeHandler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/config.json", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /config.json: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /config.json status=%d want 405", resp.StatusCode)
	}
}
