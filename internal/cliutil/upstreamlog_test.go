package cliutil

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestUpstreamLogNeverContainsQueryOrKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	orig := UpstreamLogger
	UpstreamLogger = func() *slog.Logger { return slog.New(slog.NewJSONHandler(&buf, nil)) }
	t.Cleanup(func() { UpstreamLogger = orig })

	c := NewClient(srv.URL)
	c.Backoff = time.Millisecond
	_, _, err := c.GetJSON(context.Background(), "/device/udi.json",
		url.Values{"api_key": {"SECRET"}, "search": {"brand:pacemaker"}})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`"msg":"upstream"`, `"path":"/device/udi.json"`, `"attempt":1`, `"status":200`, `"bytes":11`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	for _, bad := range []string{"SECRET", "api_key", "pacemaker", "search=", "?", "http://"} {
		if strings.Contains(out, bad) {
			t.Errorf("log contains %q:\n%s", bad, out)
		}
	}
}
