package cli

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// The app used to log nothing but its startup line, so a slow or partial
// response could only be investigated by reading the code and guessing. This
// gives every request one structured line — the same shape the auth service
// emits, which is why auth's behaviour is diagnosable from `docker logs` alone.
//
// What is logged is deliberately narrow: method, path, status, duration, and
// the device term. NOT the raw query string. /config.json hands the browser a
// publishable Supabase key and its comment states that the key is never logged;
// a whole-query log would quietly break that promise the first time any
// endpoint grew a sensitive parameter. Logs are long-lived and collected by
// Docker, so what goes in is hard to take back.
var reqLog = slog.New(slog.NewJSONHandler(os.Stdout, nil))

// healthPaths are the monitor probes. They arrive every 30 seconds forever, and
// logging them would bury the request lines that matter under heartbeat noise.
var healthPaths = map[string]bool{"/api/health": true, "/healthz": true}

// statusRecorder captures the status code on its way out. http.ResponseWriter
// does not expose what was written, and a handler that never calls WriteHeader
// has implicitly sent 200 — hence the default.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// withLogging emits one line per request. It wraps OUTSIDE withRecovery so a
// handler panic — turned into a 500 by the recovery layer — is still logged;
// that is precisely the case worth having in the log.
func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"bytes", rec.bytes,
		}
		// The device term is the one query value worth having: without it a slow
		// line cannot be tied to what was searched, and the dossier/signals pair
		// cannot be told apart from two unrelated requests. Clipped, because a
		// log field should not carry an unbounded user-supplied string.
		if d := strings.TrimSpace(r.URL.Query().Get("device")); d != "" {
			attrs = append(attrs, "device", clip(d, 80))
		}
		reqLog.Info("request", attrs...)
	})
}
