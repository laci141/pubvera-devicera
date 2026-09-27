package cliutil

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type failingBody struct{ r io.Reader }

func (f *failingBody) Read(p []byte) (int, error) {
	if n, _ := f.r.Read(p); n > 0 {
		return n, nil
	}
	return 0, errors.New("connection reset mid-body")
}
func (f *failingBody) Close() error { return nil }

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestAuditBUG07BodyReadErrorReturned: a body read error must be an error,
// not a partial "successful" body.
func TestAuditBUG07BodyReadErrorReturned(t *testing.T) {
	c := testClient("http://example.invalid")
	c.HTTP = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{},
			Body: &failingBody{r: strings.NewReader(`{"partial":`)}, Request: r}, nil
	})}
	body, _, err := c.GetJSON(context.Background(), "/x", nil)
	if err == nil {
		t.Fatalf("want read error, got nil (body=%q)", body)
	}
	if !strings.Contains(err.Error(), "connection reset mid-body") {
		t.Errorf("error lacks cause: %v", err)
	}
}
