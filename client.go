/*
Copyright 2026 Hyperbolic Labs, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hyperbolic

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
	"strings"
	"time"
)

// DefaultBaseURL is the production Hyperbolic API.
const DefaultBaseURL = "https://api.hyperbolic.ai"

// maxResponseBytes bounds how much of a response body the client reads.
const maxResponseBytes = 8 << 20

var defaultUserAgent = "hyperbolic-go/" + version

// transport sends API requests for a [Client]: it authenticates them, retries
// them by the client's [RetryPolicy], and turns failures into errors. The
// generated services call it.
type transport struct {
	baseURL   *url.URL
	apiKey    string
	http      *http.Client
	userAgent string
	retry     RetryPolicy
}

// Option configures a [Client].
type Option func(*transport) error

// WithBaseURL points the client at a different API endpoint, for example a
// staging environment or a test server. Plain http is accepted only for
// loopback hosts, so an API key is never sent unencrypted over a network.
func WithBaseURL(raw string) Option {
	return func(t *transport) error {
		u, err := parseBaseURL(raw)
		if err != nil {
			return err
		}
		t.baseURL = u
		return nil
	}
}

// WithHTTPClient replaces the underlying HTTP client (for custom transports,
// proxies or timeouts). The default has a 30 second timeout.
func WithHTTPClient(h *http.Client) Option {
	return func(t *transport) error {
		if h == nil {
			return errors.New("hyperbolic: WithHTTPClient: nil *http.Client")
		}
		t.http = h
		return nil
	}
}

// WithUserAgent prepends a product token to the User-Agent header, for
// example "hyperbolic-operator/0.1.0". The SDK's own token is always kept.
func WithUserAgent(product string) Option {
	return func(t *transport) error {
		product = strings.TrimSpace(product)
		if product != "" {
			t.userAgent = product + " " + defaultUserAgent
		}
		return nil
	}
}

// WithRetryPolicy overrides the default [RetryPolicy].
func WithRetryPolicy(p RetryPolicy) Option {
	return func(t *transport) error {
		t.retry = p
		return nil
	}
}

// NewClient returns a client authenticated with apiKey. A few operations,
// such as listing rental options, work without a key. The rest need one.
func NewClient(apiKey string, opts ...Option) (*Client, error) {
	base, err := url.Parse(DefaultBaseURL)
	if err != nil {
		return nil, err
	}
	t := &transport{
		baseURL:   base,
		apiKey:    strings.TrimSpace(apiKey),
		http:      &http.Client{Timeout: 30 * time.Second},
		userAgent: defaultUserAgent,
		retry:     DefaultRetryPolicy(),
	}
	for _, opt := range opts {
		if err := opt(t); err != nil {
			return nil, err
		}
	}
	return newClient(t), nil
}

// String keeps the API key out of logs when a Client is printed.
func (c *Client) String() string {
	return "hyperbolic.Client{baseURL: " + c.t.baseURL.String() + "}"
}

// GoString keeps the API key out of %#v output.
func (c *Client) GoString() string { return c.String() }

func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil {
		return nil, fmt.Errorf("hyperbolic: invalid base URL %q: %w", raw, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("hyperbolic: base URL %q has no host", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return nil, fmt.Errorf("hyperbolic: refusing plaintext base URL %q: API keys are only sent over https (http is allowed for loopback test servers)", raw)
		}
	default:
		return nil, fmt.Errorf("hyperbolic: base URL %q must use https", raw)
	}
	return u, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// call describes one API request.
type call struct {
	method string
	path   string
	query  url.Values
	body   any
	// idempotent marks calls that are safe to repeat. They are retried on
	// transient failures. Other calls are retried only when the server
	// provably did not process them (429, or the connection was never made),
	// and an ambiguous failure is reported as ErrOutcomeUnknown.
	idempotent bool
}

func (t *transport) do(ctx context.Context, cl call, out any) error {
	var payload []byte
	if cl.body != nil {
		b, err := json.Marshal(cl.body)
		if err != nil {
			return fmt.Errorf("hyperbolic: encode %s %s request: %w", cl.method, cl.path, err)
		}
		payload = b
	}

	maxAttempts := t.retry.attempts()
	for attempt := 1; ; attempt++ {
		data, err := t.send(ctx, cl, payload)
		if err == nil {
			if out != nil && len(bytes.TrimSpace(data)) > 0 {
				if err := json.Unmarshal(data, out); err != nil {
					return decodeError(cl, err)
				}
			}
			return nil
		}
		if ctx.Err() != nil {
			return finalize(cl, err)
		}
		retry, wait := t.retry.decide(attempt, maxAttempts, cl.idempotent, err)
		if !retry {
			return finalize(cl, err)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return finalize(cl, err)
		case <-timer.C:
		}
	}
}

func (t *transport) send(ctx context.Context, cl call, payload []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, &transportError{method: cl.method, path: cl.path, err: err}
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	u := t.baseURL.JoinPath(cl.path)
	u.RawQuery = cl.query.Encode()
	req, err := http.NewRequestWithContext(ctx, cl.method, u.String(), body)
	if err != nil {
		return nil, &transportError{method: cl.method, path: cl.path, err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", t.userAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if t.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	}

	resp, err := t.http.Do(req)
	if err != nil {
		return nil, &transportError{method: cl.method, path: cl.path, err: err, mayHaveBeenSent: !neverSent(err)}
	}
	defer func() { _ = resp.Body.Close() }()

	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, newAPIError(cl.method, cl.path, resp, data)
	}
	if readErr != nil {
		// The server answered 2xx, so the call took effect, but the body was lost.
		return nil, &transportError{method: cl.method, path: cl.path, err: readErr, mayHaveBeenSent: true}
	}
	return data, nil
}

// neverSent reports whether a transport error happened before the request
// could reach the server (DNS failure, connection refused).
func neverSent(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// finalize turns an ambiguous failure of a non-idempotent call into an
// OutcomeUnknownError so callers cannot mistake it for a clean failure.
func finalize(cl call, err error) error {
	if !cl.idempotent && outcomeAmbiguous(err) {
		return &OutcomeUnknownError{Method: cl.method, Path: cl.path, Err: err}
	}
	return err
}

func outcomeAmbiguous(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= 500
	}
	var te *transportError
	if errors.As(err, &te) {
		return te.mayHaveBeenSent
	}
	return false
}
