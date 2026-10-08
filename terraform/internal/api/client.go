// Package api is the provider's hand-written client for the Santati control
// plane's trails, log streams, organizations and event definitions.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Options configures a Client.
type Options struct {
	// BaseURL is the control plane origin, e.g. https://api.santati.io.
	BaseURL string
	// APIKey is sent as `Authorization: Api-Key <key>`.
	APIKey    string
	UserAgent string
}

// Client talks to the control plane. It is safe for concurrent use.
type Client struct {
	baseURL        string
	apiKey         string
	userAgent      string
	http           *http.Client
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// New returns a Client for opts.
func New(opts Options) *Client {
	return &Client{
		baseURL:   strings.TrimRight(opts.BaseURL, "/"),
		apiKey:    opts.APIKey,
		userAgent: opts.UserAgent,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// A 3xx is a response, never followed: the key must not travel to
			// a host the caller did not name.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		maxRetries:     2,
		initialBackoff: 250 * time.Millisecond,
		maxBackoff:     8 * time.Second,
	}
}

// do sends one logical request, retrying what is safe to retry. in is encoded
// as JSON when non-nil; a 2xx body is decoded into out unless out is nil or
// the status is 204.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body []byte
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = encoded
	}
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	for attempt := 1; ; attempt++ {
		err := c.once(ctx, method, target, body, out)
		if err == nil {
			return nil
		}
		if attempt > c.maxRetries || !retryable(method, err) {
			return err
		}
		delay, stop := c.retryDelay(attempt, err)
		if stop {
			return err
		}
		if sleepErr := sleep(ctx, delay); sleepErr != nil {
			return fmt.Errorf("%w; retry aborted: %w", err, sleepErr)
		}
	}
}

func (c *Client) once(ctx context.Context, method, target string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Api-Key "+c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return &transportError{err}
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return &transportError{err}
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		code, message := parseErrorBody(payload, resp.StatusCode)
		return &APIError{
			Status:     resp.StatusCode,
			Code:       code,
			Message:    message,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode response (HTTP %d): %w", resp.StatusCode, err)
	}
	return nil
}

// retryable reports whether a failed attempt may be repeated. A timed-out
// POST may have executed on the server, so POST is retried only when the
// server said it did not run: a 429.
func retryable(method string, err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusTooManyRequests:
			return apiErr.Code != "quota_exceeded"
		case 500, 502, 503, 504:
			return method != http.MethodPost
		}
		return false
	}
	var transport *transportError
	return errors.As(err, &transport) && method != http.MethodPost
}

// retryDelay returns how long to wait before retry number attempt (1-based),
// and whether the caller must stop and return the error instead.
func (c *Client) retryDelay(attempt int, err error) (time.Duration, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter != nil {
		delay := time.Duration(*apiErr.RetryAfter) * time.Second
		if delay > c.maxBackoff {
			return 0, true
		}
		return delay, false
	}
	ceiling := c.initialBackoff * time.Duration(int64(1)<<uint(attempt-1))
	if ceiling <= 0 || ceiling > c.maxBackoff {
		ceiling = c.maxBackoff
	}
	if ceiling <= 0 {
		return 0, false
	}
	return time.Duration(rand.Int64N(int64(ceiling) + 1)), false
}

// sleep waits for the delay, returning early when the context is done.
func sleep(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// page is the cursor-paginated list envelope.
type page[T any] struct {
	Results []T     `json:"results"`
	Next    *string `json:"next"`
}

// listAll collects every page of a cursor-paginated list, in API order.
func listAll[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var all []T
	seen := map[string]bool{}
	var query url.Values
	for {
		var p page[T]
		if err := c.do(ctx, http.MethodGet, path, query, nil, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Results...)

		cursor := nextCursor(p.Next)
		if cursor == "" {
			return all, nil
		}
		if seen[cursor] {
			return nil, fmt.Errorf("GET %s: the control plane returned cursor %q twice", path, cursor)
		}
		seen[cursor] = true
		query = url.Values{"cursor": {cursor}}
	}
}

// nextCursor extracts the cursor from a page's `next` URL, or "" on the last
// page. The path is re-requested against the configured base URL rather than
// following `next`, which names whatever host the server believes it has.
func nextCursor(next *string) string {
	if next == nil {
		return ""
	}
	parsed, err := url.Parse(*next)
	if err != nil {
		return ""
	}
	return parsed.Query().Get("cursor")
}
