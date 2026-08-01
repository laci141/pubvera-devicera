package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
	"github.com/laci141/medical-device-intelligence/internal/sources"
	"github.com/laci141/medical-device-intelligence/web"
)

func init() { register("serve", cmdServe) }

// moduleCount is the number of intelligence modules exposed by the platform
// (01 Telemetry .. 12 Synthesis). There is no module registry to derive it from.
const moduleCount = 12

// getenv is an indirection so tests can fake the PORT environment variable
// without mutating the real process environment.
var getenv = os.Getenv

// cmdServe starts an HTTP server that exposes the command surface as JSON
// endpoints (same-origin API for a future HTML frontend). Each endpoint is a
// thin adapter over the existing command handlers run in --json mode, so the
// envelope shape, the disclaimer, and the never-a-risk-score conventions are
// inherited rather than re-implemented. On Render-style platforms the PORT
// environment variable, when set, overrides --port.
func cmdServe(ctx context.Context, stdout, stderr io.Writer, args []string) int {
	fs, _ := newFlagSet("serve")
	port := fs.Int("port", 8080, "listen port (PORT env, when set, wins)")
	fs.String("db", "", "path to the SQLite cache (reserved for cached endpoints)")
	if err := parse(fs, stderr, args, map[string]bool{"port": true, "db": true}); err != nil {
		return 2
	}
	p := *port
	if env := getenv("PORT"); env != "" {
		n, err := strconv.Atoi(env)
		if err != nil {
			fmt.Fprintf(stderr, "serve: PORT env is not a number: %q\n", env)
			return 2
		}
		p = n
	}
	if p < 1 || p > 65535 {
		fmt.Fprintf(stderr, "serve: port must be 1-65535, got %d\n", p)
		return 2
	}

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", p),
		Handler:           NewServeHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	fmt.Fprintf(stderr, "serve: listening on :%d (Ctrl+C to stop)\n", p)

	select {
	case <-ctx.Done():
		// Graceful shutdown: stop accepting, let in-flight requests finish.
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			fmt.Fprintf(stderr, "serve: shutdown: %v\n", err)
			return 1
		}
		fmt.Fprintln(stderr, "serve: stopped")
		return 0
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "serve: %v\n", err)
			return 1
		}
		return 0
	}
}

// apiRoute maps one GET endpoint onto a registered command run in --json mode.
type apiRoute struct {
	// params are the required query parameters, checked before dispatch so a
	// missing one is a clean 400 rather than a command usage message.
	params []string
	// argv builds the command argv (including the command name) from the query.
	argv func(q url.Values) []string
	// addMeta folds a freshness meta block (queried_at, response_ms,
	// openfda_last_updated, sources) into the response envelope.
	addMeta bool
}

var apiRoutes = map[string]apiRoute{
	"/api/search": {
		params: []string{"device"},
		argv:   func(q url.Values) []string { return []string{"search", q.Get("device"), "--json"} },
	},
	"/api/signals": {
		params:  []string{"device"},
		argv:    func(q url.Values) []string { return []string{"signals", "--device", q.Get("device"), "--json"} },
		addMeta: true,
	},
	"/api/dossier": {
		params:  []string{"device"},
		argv:    func(q url.Values) []string { return []string{"dossier", "--device", q.Get("device"), "--json"} },
		addMeta: true,
	},
	"/api/compare": {
		params: []string{"a", "b"},
		argv:   func(q url.Values) []string { return []string{"compare", q.Get("a"), q.Get("b"), "--json"} },
	},
}

// NewServeHandler builds the API handler. Exported for the frontend embed step
// later; tests drive it through httptest.
func NewServeHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", handleHealth)
	// /healthz alias: the other seven Pubvera apps expose /healthz, and external
	// monitors probe that path by convention. Same handler, no behaviour change.
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/api/trend", handleTrend)
	mux.HandleFunc("/api/failure-modes", handleFailureModes)
	mux.HandleFunc("/api/devices", handleDevices)
	for path, route := range apiRoutes {
		mux.HandleFunc(path, routeHandler(route))
	}
	// GET / serves the embedded frontend; every other unmatched path is a JSON 404.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			if !allowGET(w, r) {
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(web.Index)
			return
		}
		writeJSONError(w, http.StatusNotFound, "not found")
	})
	return withRecovery(mux)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if !allowGET(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"commands":   len(commands),
		"modules":    moduleCount,
		"disclaimer": cliutil.Disclaimer,
	})
}

// routeHandler adapts one command to HTTP: required params → 400, command usage
// error (exit 2) → 400, command runtime/upstream error (exit 1) → 502, success →
// the command's own --json envelope (disclaimer included) passed through.
func routeHandler(route apiRoute) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allowGET(w, r) {
			return
		}
		q := r.URL.Query()
		for _, p := range route.params {
			if strings.TrimSpace(q.Get(p)) == "" {
				writeJSONError(w, http.StatusBadRequest, p+" required")
				return
			}
		}
		var out, errBuf bytes.Buffer
		start := time.Now()
		code := Dispatch(r.Context(), &out, &errBuf, route.argv(q))
		switch code {
		case 0:
			body := out.Bytes()
			if route.addMeta {
				body = withMeta(r.Context(), body, start)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		case 2:
			writeJSONError(w, http.StatusBadRequest, firstLine(errBuf.String()))
		default:
			writeJSONError(w, http.StatusBadGateway, firstLine(errBuf.String()))
		}
	}
}

// allowGET rejects non-GET methods with a JSON 405. Only safe GETs are served,
// which is also why the wide-open CORS header is acceptable.
func allowGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	return false
}

// withRecovery converts a handler panic into a JSON 500 instead of killing the
// connection: module errors must never take the server down.
func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("internal error: %v", rec))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error":      msg,
		"disclaimer": cliutil.Disclaimer,
	})
}

// ---- Freshness meta ----

// apiSources is the fixed source list surfaced in the meta block.
var apiSources = []string{"openFDA MAUDE", "openFDA Enforcement", "ClinicalTrials.gov v2", "PubMed"}

// fetchOpenFDALastUpdated probes openFDA for its dataset timestamp. An
// indirection so tests can stub it without a network call.
var fetchOpenFDALastUpdated = func(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.fda.gov/device/event.json?limit=1", nil)
	if err != nil {
		return ""
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var env struct {
		Meta struct {
			LastUpdated string `json:"last_updated"`
		} `json:"meta"`
	}
	if json.NewDecoder(resp.Body).Decode(&env) != nil {
		return ""
	}
	return env.Meta.LastUpdated
}

var luCache struct {
	mu  sync.Mutex
	val string
	at  time.Time
}

// lastUpdatedCached caches the openFDA dataset timestamp for an hour so the
// freshness header doesn't cost an extra upstream round-trip per request.
func lastUpdatedCached(ctx context.Context) string {
	luCache.mu.Lock()
	defer luCache.mu.Unlock()
	if luCache.val != "" && time.Since(luCache.at) < time.Hour {
		return luCache.val
	}
	if v := fetchOpenFDALastUpdated(ctx); v != "" {
		luCache.val, luCache.at = v, time.Now()
	}
	return luCache.val
}

// withMeta folds the freshness block into a command's JSON envelope. On any
// parse hiccup the original body passes through untouched.
func withMeta(ctx context.Context, body []byte, start time.Time) []byte {
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return body
	}
	obj["meta"] = map[string]any{
		"queried_at":           time.Now().UTC().Format(time.RFC3339),
		"response_ms":          time.Since(start).Milliseconds(),
		"openfda_last_updated": lastUpdatedCached(ctx),
		"sources":              apiSources,
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return b
}

// ---- Direct data endpoints (sources layer, no CLI command behind them) ----

// handleTrend answers /api/trend?device=X with the last 10 years of MAUDE
// report totals, one server-side date-bounded count per year.
func handleTrend(w http.ResponseWriter, r *http.Request) {
	if !allowGET(w, r) {
		return
	}
	device := strings.TrimSpace(r.URL.Query().Get("device"))
	if device == "" {
		writeJSONError(w, http.StatusBadRequest, "device required")
		return
	}
	src, ok := getSource("openfda_device_event")
	if !ok {
		writeJSONError(w, http.StatusBadGateway, "event source unavailable")
		return
	}
	year := time.Now().Year()
	type yearCount struct {
		Year  int `json:"year"`
		Count int `json:"count"`
	}
	rows := make([]yearCount, 0, 10)
	var notes []string
	for y := year - 9; y <= year; y++ {
		q := sources.Query{
			Term:      device,
			Limit:     1,
			DateField: "date_received",
			DateFrom:  fmt.Sprintf("%d0101", y),
			DateTo:    fmt.Sprintf("%d1231", y),
		}
		_, page, err := src.Fetch(r.Context(), q)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%d unavailable: %v", y, err))
			continue
		}
		rows = append(rows, yearCount{Year: y, Count: page.Total})
	}
	resp := map[string]any{
		"records":    rows,
		"count":      len(rows),
		"note":       "MAUDE reports received per year (server-side totals); reporting practices change over time — not a failure rate",
		"disclaimer": cliutil.Disclaimer,
	}
	if len(notes) > 0 {
		resp["partial"] = notes
	}
	writeJSON(w, http.StatusOK, resp)
}

// fieldCounter is the source capability the failure-modes endpoint needs.
type fieldCounter interface {
	CountField(ctx context.Context, q sources.Query, field string) (map[string]int, error)
}

// handleFailureModes answers /api/failure-modes?device=X with the top-10
// MAUDE product_problems.exact terms, verbatim as filed.
func handleFailureModes(w http.ResponseWriter, r *http.Request) {
	if !allowGET(w, r) {
		return
	}
	device := strings.TrimSpace(r.URL.Query().Get("device"))
	if device == "" {
		writeJSONError(w, http.StatusBadRequest, "device required")
		return
	}
	src, ok := getSource("openfda_device_event")
	if !ok {
		writeJSONError(w, http.StatusBadGateway, "event source unavailable")
		return
	}
	counter, ok := src.(fieldCounter)
	if !ok {
		writeJSONError(w, http.StatusBadGateway, "event source lacks field counting")
		return
	}
	counts, err := counter.CountField(r.Context(), sources.Query{Term: device}, "product_problems.exact")
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	type problem struct {
		Problem string `json:"problem"`
		Count   int    `json:"count"`
	}
	rows := make([]problem, 0, len(counts))
	for p, c := range counts {
		rows = append(rows, problem{Problem: p, Count: c})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Problem < rows[j].Problem
	})
	if len(rows) > 10 {
		rows = rows[:10]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"records":    rows,
		"count":      len(rows),
		"note":       "device problem terms as filed in MAUDE, verbatim (product_problems.exact)",
		"disclaimer": cliutil.Disclaimer,
	})
}

// asMaps coerces a raw []any JSON field into its object elements.
func asMaps(v any) []map[string]any {
	list, _ := v.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// looseBool reads a boolean that openFDA may serialize either as a JSON bool
// or as the strings "true"/"false" (the UDI endpoint does the latter).
func looseBool(v any) (val, ok bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		if b == "true" {
			return true, true
		}
		if b == "false" {
			return false, true
		}
	}
	return false, false
}

// deviceRow flattens one GUDID record into the registration-style table row
// the frontend renders and exports. Missing fields stay empty strings so the
// table and the exports never show "null".
func deviceRow(raw map[string]any) map[string]any {
	udi := ""
	ids := asMaps(raw["identifiers"])
	for _, id := range ids {
		if str(id["type"]) == "Primary" {
			udi = str(id["id"])
			break
		}
	}
	if udi == "" && len(ids) > 0 {
		udi = str(ids[0]["id"])
	}

	class, category := "", ""
	if pcs := asMaps(raw["product_codes"]); len(pcs) > 0 {
		if of, ok := pcs[0]["openfda"].(map[string]any); ok {
			class = str(of["device_class"])
			category = str(of["device_name"])
		}
		if category == "" {
			category = str(pcs[0]["name"])
		}
	}
	switch class {
	case "1":
		class = "Class I"
	case "2":
		class = "Class II"
	case "3":
		class = "Class III"
	case "":
		class = "Not specified"
	default:
		// GUDID uses "U" (and other letter codes) for unclassified and
		// pre-amendment devices; never leak a raw code into the table.
		class = "Unclassified"
	}

	sterile := ""
	if st, ok := raw["sterilization"].(map[string]any); ok {
		if b, ok := looseBool(st["is_sterile"]); ok {
			if b {
				sterile = "Sterile"
			} else {
				sterile = "Non-sterile"
			}
		}
		if m := str(st["sterilization_methods"]); m != "" {
			if sterile == "" {
				sterile = m
			} else {
				sterile += " (" + m + ")"
			}
		}
	}

	latex := ""
	if b, ok := looseBool(raw["is_labeled_as_no_nrl"]); ok {
		if b {
			latex = "Labeled latex-free"
		} else {
			latex = "Not labeled latex-free"
		}
	}

	name := str(raw["brand_name"])
	if name == "" {
		name = clip(str(raw["device_description"]), 80)
	}
	last := str(raw["public_version_date"])
	if last == "" {
		last = str(raw["publish_date"])
	}

	return map[string]any{
		"udi":                 udi,
		"device_name":         name,
		"company":             str(raw["company_name"]),
		"device_class":        class,
		"product_category":    category,
		"registration_status": str(raw["record_status"]),
		"listing_status":      str(raw["commercial_distribution_status"]),
		"sterilization":       sterile,
		"latex":               latex,
		"last_update":         last,
	}
}

// udiClient reaches openFDA directly for the one UDI search the sources
// adapter's fixed brand/DI expression cannot ask for. Handler-level, like
// handleTrend: the source definition stays untouched.
var udiClient = cliutil.NewClient("https://api.fda.gov")

// udiCategorySearch queries device/udi by FDA product-code name
// (product_codes.openfda.device_name) — the field where "Pacemaker, Permanent,
// Implantable" lives even when no manufacturer puts the word in a brand name.
func udiCategorySearch(ctx context.Context, term string, limit int) ([]map[string]any, int, error) {
	params := url.Values{}
	params.Set("search", cliutil.Phrase("product_codes.openfda.device_name", term))
	params.Set("limit", strconv.Itoa(limit))
	body, _, err := udiClient.GetJSON(ctx, "/device/udi.json", params)
	if err != nil {
		var apiErr *cliutil.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return nil, 0, nil // no matches → empty, not an error
		}
		return nil, 0, err
	}
	var env struct {
		Meta struct {
			Results struct {
				Total int `json:"total"`
			} `json:"results"`
		} `json:"meta"`
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, 0, err
	}
	return env.Results, env.Meta.Results.Total, nil
}

// mergeDeviceRows unions the brand-name and product-category result sets,
// deduplicating by UDI: a row present in both comes out once with matched_on
// "Both". Rows without a UDI are kept as-is (never collapsed onto each other).
// Returns the merged rows (capped at max) and the number of duplicates folded,
// so the caller can report an honest union total.
func mergeDeviceRows(brand, category []map[string]any, max int) ([]map[string]any, int) {
	out := make([]map[string]any, 0, len(brand)+len(category))
	seen := make(map[string]int, len(brand)) // udi -> index in out
	dups := 0
	add := func(rows []map[string]any, label string) {
		for _, row := range rows {
			udi, _ := row["udi"].(string)
			if udi != "" {
				if i, ok := seen[udi]; ok {
					dups++
					if out[i]["matched_on"] != label {
						out[i]["matched_on"] = "Both"
					}
					continue
				}
				seen[udi] = len(out)
			}
			row["matched_on"] = label
			out = append(out, row)
		}
	}
	add(brand, "Brand name")
	add(category, "Product category")
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out, dups
}

// handleDevices answers /api/devices?device=X with flattened GUDID device
// records. Two searches run in parallel — brand name / UDI-DI (the existing
// source) and FDA product-category name (direct) — because major manufacturers
// rarely put the generic word in a brand name ("Azure XT DR MRI SureScan" is a
// pacemaker). One failing leg degrades to the other's results, not to a 502.
func handleDevices(w http.ResponseWriter, r *http.Request) {
	if !allowGET(w, r) {
		return
	}
	device := strings.TrimSpace(r.URL.Query().Get("device"))
	if device == "" {
		writeJSONError(w, http.StatusBadRequest, "device required")
		return
	}
	src, ok := getSource("openfda_device_udi")
	if !ok {
		writeJSONError(w, http.StatusBadGateway, "UDI source unavailable")
		return
	}

	var (
		brandRecs  []sources.RawRecord
		brandTotal int
		brandErr   error
		catRecs    []map[string]any
		catTotal   int
		catErr     error
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		var page sources.Page
		brandRecs, page, brandErr = src.Fetch(r.Context(), sources.Query{Term: device, Limit: 100})
		brandTotal = page.Total
	}()
	go func() {
		defer wg.Done()
		catRecs, catTotal, catErr = udiCategorySearch(r.Context(), device, 100)
	}()
	wg.Wait()
	if brandErr != nil && catErr != nil {
		writeJSONError(w, http.StatusBadGateway, brandErr.Error())
		return
	}

	brandRows := make([]map[string]any, 0, len(brandRecs))
	for _, rec := range brandRecs {
		brandRows = append(brandRows, deviceRow(rec.Raw))
	}
	catRows := make([]map[string]any, 0, len(catRecs))
	for _, raw := range catRecs {
		catRows = append(catRows, deviceRow(raw))
	}
	rows, dups := mergeDeviceRows(brandRows, catRows, 100)

	resp := map[string]any{
		"records":        rows,
		"count":          len(rows),
		"total":          brandTotal + catTotal - dups,
		"total_brand":    brandTotal,
		"total_category": catTotal,
		"note":           "GUDID device records via openFDA device/udi; union of a brand-name/UDI-DI search and an FDA product-category search, deduplicated by UDI (total is approximate when the sets overlap beyond the fetched pages); registration data may be incomplete or delayed",
		"disclaimer":     cliutil.Disclaimer,
	}
	var partial []string
	if brandErr != nil {
		partial = append(partial, "brand-name search unavailable: "+brandErr.Error())
	}
	if catErr != nil {
		partial = append(partial, "product-category search unavailable: "+catErr.Error())
	}
	if partial != nil {
		resp["partial"] = partial
	}
	writeJSON(w, http.StatusOK, resp)
}

// firstLine trims a multi-line stderr capture to its first line for the JSON
// error field; the full text stays server-side only.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		s = "command failed"
	}
	return s
}
