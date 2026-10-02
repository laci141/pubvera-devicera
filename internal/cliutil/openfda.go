package cliutil

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
)

// OpenFDAKeyEnv names the environment variable that holds the openFDA API key.
// Same name as in pubvera-trialvera, so one variable serves every Pubvera app
// that calls openFDA. Unset or empty means keyless, exactly as before.
const OpenFDAKeyEnv = "OPENFDA_API_KEY"

// openFDAKey is read once, when the process starts. Atomic because tests swap
// it while request goroutines from earlier tests may still be finishing.
var openFDAKey atomic.Value // string

func init() { openFDAKey.Store(strings.TrimSpace(os.Getenv(OpenFDAKeyEnv))) }

// SetOpenFDAKey replaces the key read at startup and returns a func that puts
// the previous one back. For tests: production reads the key only from env.
func SetOpenFDAKey(key string) (restore func()) {
	prev := openFDAKey.Load().(string)
	openFDAKey.Store(key)
	return func() { openFDAKey.Store(prev) }
}

// NewOpenFDAClient is NewClient for api.fda.gov: keyless unless
// OPENFDA_API_KEY is set, in which case every request carries api_key.
func NewOpenFDAClient(baseURL string) *Client {
	c := NewClient(baseURL)
	c.UserAgent = "medical-device-intelligence-pp-cli/0.1 (keyless unless OPENFDA_API_KEY is set)"
	c.HTTP.Transport = apiKeyTransport{}
	return c
}

// apiKeyTransport adds api_key to a CLONE of each request, never to the
// request it was handed. That is what keeps the key out of logs and error
// text: every URL the app builds or prints — APIError.URL, the read-body
// error, the *url.Error a failed Do returns — comes from the original request,
// which never carries the key. The key exists only on the wire.
//
// The base is looked up per request rather than captured, so a test that
// swaps http.DefaultTransport still sees these requests.
type apiKeyTransport struct{}

func (apiKeyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	key, _ := openFDAKey.Load().(string)
	if key == "" {
		return http.DefaultTransport.RoundTrip(req)
	}
	keyed := req.Clone(req.Context())
	// Appended, not re-encoded: the rest of the query goes out byte for byte
	// as url.Values.Encode built it (the Lucene builders depend on that).
	if keyed.URL.RawQuery != "" {
		keyed.URL.RawQuery += "&"
	}
	keyed.URL.RawQuery += "api_key=" + url.QueryEscape(key)
	resp, err := http.DefaultTransport.RoundTrip(keyed)
	if resp != nil {
		// http.Client builds redirect-error text from resp.Request.URL; point
		// it back at the keyless original.
		resp.Request = req
	}
	return resp, err
}
