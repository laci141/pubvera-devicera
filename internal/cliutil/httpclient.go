package cliutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// APIError carries the HTTP status and a short body snippet for diagnosis.
// Callers inspect StatusCode: a 404 is "no records" (data, exit 0), not a
// failure. See guardrail 5.
type APIError struct {
	StatusCode int
	Snippet    string // ~200 bytes of the response body
	URL        string
}

func (e *APIError) Error() string {
	return redactAPIKey(fmt.Sprintf("http %d for %s: %s", e.StatusCode, e.URL, e.Snippet))
}

// The only two classes of upstream failure that may be sent to a browser. They
// carry no URL, no status line and no upstream body: the full APIError text
// goes to the server log instead.
const (
	MsgRateLimited  = "upstream rate limit reached"
	MsgUnavailable  = "upstream unavailable"
	CodeRateLimited = "rate_limited"
	CodeUnavail     = "upstream_unavailable"
)

// IsRateLimited reports whether err is, or wraps, an upstream 429.
func IsRateLimited(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests
}

// UpstreamClass maps an upstream error to the fixed phrase and machine code
// that are safe to send to a browser.
func UpstreamClass(err error) (msg, code string) {
	if IsRateLimited(err) {
		return MsgRateLimited, CodeRateLimited
	}
	return MsgUnavailable, CodeUnavail
}

// apiKeyValue matches the value of an api_key query parameter.
var apiKeyValue = regexp.MustCompile(`(?i)(api_key=)[^&\s"']*`)

// redactAPIKey replaces every api_key value with [REDACTED]. A backstop: the
// openFDA transport adds the key to a clone of the request, so URL never
// carries it — this keeps a future change that breaks that out of logs and
// out of the browser.
func redactAPIKey(s string) string {
	return apiKeyValue.ReplaceAllString(s, "${1}[REDACTED]")
}

// Client is a small JSON HTTP client with a single retry policy. It is keyless
// unless built by NewOpenFDAClient with OPENFDA_API_KEY set.
type Client struct {
	BaseURL   string
	UserAgent string
	HTTP      *http.Client
	// Backoff is the pause before the first retry; it doubles for the second.
	Backoff time.Duration
	// MaxBodyBytes caps the response body. openFDA records are large (a single
	// device enforcement record is ~66 KB, so a page of 50 is ~3.3 MB and a full
	// page of 1000 approaches ~66 MB), so this must be generous. If a response
	// exceeds it, GetJSON returns a CLEAR error rather than silently truncating
	// into an "unexpected end of JSON input" — the exact bug a 1 MB cap caused.
	MaxBodyBytes int64
}

const defaultMaxBodyBytes = 128 << 20 // 128 MiB — comfortably fits a full openFDA page

// maxAttempts is the total number of tries, i.e. two retries after the first
// attempt. Three, not two, because both upstreams were measured failing
// transiently in one session: NCBI answered a keyless burst with 429 and, on a
// different run, served its own eutils102 error page as a 500. A single retry
// after 300ms was not enough for the 500 — it failed twice in a row.
const maxAttempts = 3

// maxRetryAfter caps how long a Retry-After header can hold a request. A
// dossier probe is one of eleven running under a 90s ceiling; obeying a
// multi-minute value would cost more than the signal is worth, so past this we
// give up and report the error rather than stall the run.
const maxRetryAfter = 5 * time.Second

// NewClient returns a Client with sane keyless defaults.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		UserAgent:    "medical-device-intelligence-pp-cli/0.1 (keyless)",
		HTTP:         &http.Client{Timeout: 30 * time.Second},
		Backoff:      300 * time.Millisecond,
		MaxBodyBytes: defaultMaxBodyBytes,
	}
}

// retryable reports whether a status is worth trying again. 5xx is the upstream
// having a bad moment. 429 is a rate limit — the one 4xx where waiting is
// exactly the right answer, and the reason this is not simply "4xx never
// retries" any more.
func retryable(status int) bool {
	return status >= 500 || status == http.StatusTooManyRequests
}

// retryDelay is how long to wait before the next attempt: the server's own
// Retry-After when it sends one (it knows its window better than we do),
// otherwise our doubling backoff. Only the integer-seconds form is read; the
// HTTP-date form is rare here and not worth the parsing surface.
func (c *Client) retryDelay(resp *http.Response, attempt int) (time.Duration, bool) {
	if resp != nil {
		if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
			if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
				d := time.Duration(secs) * time.Second
				if d > maxRetryAfter {
					return 0, false // too long to be worth waiting for
				}
				return d, true
			}
		}
	}
	// attempt is 0-based: 300ms before the first retry, 600ms before the second.
	return c.Backoff << attempt, true
}

// rateLimitDelay is the wait before retrying a 429, and whether to retry at
// all. A 429 is retried only when the server itself names a wait of 0..5 whole
// seconds in Retry-After. With no header, an unparsable one, an HTTP-date, a
// negative or a longer wait there is nothing to go on, and retrying on our own
// schedule would only hammer a service that just said it is overloaded.
func rateLimitDelay(resp *http.Response) (time.Duration, bool) {
	secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
	if err != nil || secs < 0 || time.Duration(secs)*time.Second > maxRetryAfter {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

// GetJSON issues GET BaseURL+path?params and returns the raw body and status.
//
// Retry policy (guardrail 6, revised 2026-09-06 and 2026-10-03): up to three
// attempts. A 5xx or a transport error is retried with a doubling backoff
// (Retry-After honoured on a 5xx when the server sends one). A 429 is retried
// only when Retry-After is a whole number of seconds from 0 to 5; otherwise it
// is returned at once (see rateLimitDelay). Every other 4xx is returned
// immediately — never retried, because waiting does not turn a malformed query
// into a good one. Any non-2xx yields an *APIError carrying a ~200-byte body
// snippet. A 404 is returned as an *APIError with StatusCode 404 so the caller
// can treat it as "no records".
//
// The original rule was one retry, 5xx only. It was written when the dossier
// ran its probes one at a time; concurrent probes made 429 a real outcome
// rather than a theoretical one, and a measured NCBI 500 survived the single
// retry. Both changes come from observed failures, not from caution.
func (c *Client) GetJSON(ctx context.Context, path string, params url.Values) ([]byte, int, error) {
	u := c.BaseURL + path
	if len(params) > 0 {
		// url.Values.Encode turns spaces into "+" — exactly what the Lucene
		// builders rely on. Never hand-encode the search expression.
		u += "?" + params.Encode()
	}

	var lastErr error
	// delay is set by the previous attempt; zero means go straight on.
	var delay time.Duration
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("User-Agent", c.UserAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			delay, _ = c.retryDelay(nil, attempt)
			continue // transport error — allow a retry
		}
		max := c.MaxBodyBytes
		if max <= 0 {
			max = defaultMaxBodyBytes
		}
		// Read one byte past the cap so we can detect (rather than silently
		// swallow) a body that exceeds it.
		body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
		resp.Body.Close()
		if err != nil {
			return nil, resp.StatusCode, fmt.Errorf("read response body from %s: %w", u, err)
		}
		if int64(len(body)) > max {
			return nil, resp.StatusCode, &APIError{
				StatusCode: resp.StatusCode,
				Snippet:    fmt.Sprintf("response body exceeded %d-byte cap; lower --limit", max),
				URL:        u,
			}
		}

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return body, resp.StatusCode, nil
		case retryable(resp.StatusCode):
			apiErr := &APIError{StatusCode: resp.StatusCode, Snippet: snippet(body), URL: u}
			lastErr = apiErr
			var d time.Duration
			var ok bool
			if resp.StatusCode == http.StatusTooManyRequests {
				d, ok = rateLimitDelay(resp)
			} else {
				d, ok = c.retryDelay(resp, attempt)
			}
			if !ok {
				// A 5xx whose Retry-After is too long, or a 429 that names no
				// short wait: give up and report it rather than stall the run.
				return body, resp.StatusCode, apiErr
			}
			delay = d
			continue
		default: // other 4xx (incl. 404) — return immediately, never retried
			return body, resp.StatusCode, &APIError{StatusCode: resp.StatusCode, Snippet: snippet(body), URL: u}
		}
	}
	return nil, 0, lastErr
}

func snippet(b []byte) string {
	const max = 200
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
