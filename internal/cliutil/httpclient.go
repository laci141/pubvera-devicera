package cliutil

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	return fmt.Sprintf("http %d for %s: %s", e.StatusCode, e.URL, e.Snippet)
}

// Client is a small keyless JSON HTTP client with a single retry policy.
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

// GetJSON issues GET BaseURL+path?params and returns the raw body and status.
//
// Retry policy (guardrail 6, revised 2026-09-06): up to three attempts, on a
// 5xx OR a 429, with a doubling backoff and Retry-After honoured when the
// server sends one. Every other 4xx is returned immediately — never retried,
// because waiting does not turn a malformed query into a good one. Any non-2xx
// yields an *APIError carrying a ~200-byte body snippet. A 404 is returned as
// an *APIError with StatusCode 404 so the caller can treat it as "no records".
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, max+1))
		resp.Body.Close()
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
			d, ok := c.retryDelay(resp, attempt)
			if !ok {
				// The server asked for longer than we are willing to wait.
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
