package cli

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

// gzipMinBytes is the smallest body worth compressing.
const gzipMinBytes = 1024

// withGzip compresses /api/* responses larger than gzipMinBytes when the client
// sends Accept-Encoding: gzip. The body is buffered so the size is known; a
// response that already has a Content-Encoding is passed through untouched.
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		bw := &gzipBuffer{header: w.Header(), status: http.StatusOK}
		next.ServeHTTP(bw, r)
		body := bw.buf.Bytes()
		h := w.Header()
		if len(body) > gzipMinBytes && h.Get("Content-Encoding") == "" {
			var zb bytes.Buffer
			zw := gzip.NewWriter(&zb)
			_, _ = zw.Write(body)
			if zw.Close() == nil {
				body = zb.Bytes()
				h.Set("Content-Encoding", "gzip")
			}
		}
		if h.Get("Content-Length") != "" {
			h.Set("Content-Length", strconv.Itoa(len(body)))
		}
		w.WriteHeader(bw.status)
		_, _ = w.Write(body)
	})
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			return !strings.Contains(strings.ReplaceAll(params, " ", ""), "q=0")
		}
	}
	return false
}

// gzipBuffer holds a handler's status and body until withGzip decides.
type gzipBuffer struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (g *gzipBuffer) Header() http.Header         { return g.header }
func (g *gzipBuffer) WriteHeader(code int)        { g.status = code }
func (g *gzipBuffer) Write(b []byte) (int, error) { return g.buf.Write(b) }
