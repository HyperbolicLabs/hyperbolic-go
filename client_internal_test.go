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
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fastRetries keeps retry tests quick while still exercising backoff.
func fastRetries() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 50 * time.Millisecond}
}

// countingServer answers every request with the next status in statuses
// (repeating the last one) and counts the requests it received.
func countingServer(t *testing.T, body string, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(n.Add(1)) - 1
		status := statuses[len(statuses)-1]
		if i < len(statuses) {
			status = statuses[i]
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts, &n
}

func newTestClient(t *testing.T, baseURL string, opts ...Option) *Client {
	t.Helper()
	all := append([]Option{WithBaseURL(baseURL), WithRetryPolicy(fastRetries())}, opts...)
	c, err := NewClient("sk_test_key", all...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The core safety property: a non-idempotent call that fails after the server
// may have applied it is sent once and reported as outcome-unknown.
func TestNonIdempotentServerErrorIsOutcomeUnknownAndNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ts, n := countingServer(t, `{"code":"INTERNAL","message":"boom"}`, status)
			c := newTestClient(t, ts.URL)

			err := c.t.do(context.Background(), call{method: http.MethodPost, path: "/v2/things", body: map[string]int{"n": 1}}, nil)
			if !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
			}
			if StatusCode(err) != status {
				t.Fatalf("wrapped status = %d, want %d", StatusCode(err), status)
			}
			if got := n.Load(); got != 1 {
				t.Fatalf("sent %d times, want exactly 1", got)
			}
		})
	}
}

func TestNonIdempotentRetriedOnRateLimit(t *testing.T) {
	ts, n := countingServer(t, `{}`, http.StatusTooManyRequests, http.StatusOK)
	c := newTestClient(t, ts.URL)

	if err := c.t.do(context.Background(), call{method: http.MethodPost, path: "/v2/things"}, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := n.Load(); got != 2 {
		t.Fatalf("sent %d times, want 2 (one 429, one success)", got)
	}
}

func TestNonIdempotentClientErrorIsDefinite(t *testing.T) {
	ts, n := countingServer(t, `{"code":"INSUFFICIENT_BALANCE","message":"top up"}`, http.StatusBadRequest)
	c := newTestClient(t, ts.URL)

	err := c.t.do(context.Background(), call{method: http.MethodPost, path: "/v2/things"}, nil)
	if !HasCode(err, CodeInsufficientBalance) {
		t.Fatalf("err = %v, want INSUFFICIENT_BALANCE", err)
	}
	if IsOutcomeUnknown(err) {
		t.Fatal("a 4xx is a definite failure, not outcome unknown")
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("sent %d times, want 1", got)
	}
}

func TestNonIdempotentTimeoutIsOutcomeUnknown(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	c := newTestClient(t, ts.URL, WithHTTPClient(&http.Client{Timeout: 50 * time.Millisecond}))

	err := c.t.do(context.Background(), call{method: http.MethodPost, path: "/v2/things"}, nil)
	if !IsOutcomeUnknown(err) {
		t.Fatalf("err = %v, want outcome unknown", err)
	}
}

func TestConnectionRefusedIsNotOutcomeUnknown(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	c := newTestClient(t, url, WithRetryPolicy(NoRetries()))

	err := c.t.do(context.Background(), call{method: http.MethodPost, path: "/v2/things"}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if IsOutcomeUnknown(err) {
		t.Fatalf("connection refused means the request never left, but got outcome unknown: %v", err)
	}
}

func TestIdempotentRetriesTransientFailures(t *testing.T) {
	ts, n := countingServer(t, `{"ok":true}`, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusOK)
	c := newTestClient(t, ts.URL)

	var out struct{ OK bool }
	if err := c.t.do(context.Background(), call{method: http.MethodGet, path: "/v2/things", idempotent: true}, &out); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !out.OK {
		t.Fatal("response body was not decoded")
	}
	if got := n.Load(); got != 3 {
		t.Fatalf("sent %d times, want 3", got)
	}
}

func TestIdempotentGivesUpAfterMaxAttempts(t *testing.T) {
	ts, n := countingServer(t, `{}`, http.StatusServiceUnavailable)
	c := newTestClient(t, ts.URL)

	err := c.t.do(context.Background(), call{method: http.MethodGet, path: "/v2/things", idempotent: true}, nil)
	if StatusCode(err) != http.StatusServiceUnavailable || IsOutcomeUnknown(err) {
		t.Fatalf("err = %v, want a plain 503", err)
	}
	if got := n.Load(); got != 3 {
		t.Fatalf("sent %d times, want 3", got)
	}
}

func TestUnauthorizedIsNotRetried(t *testing.T) {
	ts, n := countingServer(t, `{"message":"bad key"}`, http.StatusUnauthorized)
	c := newTestClient(t, ts.URL)

	err := c.t.do(context.Background(), call{method: http.MethodGet, path: "/v2/things", idempotent: true}, nil)
	if !IsUnauthorized(err) {
		t.Fatalf("err = %v, want unauthorized", err)
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("sent %d times, want 1", got)
	}
}

func TestRetryAfterLongerThanMaxDelayStops(t *testing.T) {
	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(ts.Close)
	c := newTestClient(t, ts.URL) // MaxDelay 50ms

	start := time.Now()
	err := c.t.do(context.Background(), call{method: http.MethodGet, path: "/v2/things", idempotent: true}, nil)
	if !IsRateLimited(err) {
		t.Fatalf("err = %v, want rate limited", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("client blocked on a Retry-After it should have handed back to the caller")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 30*time.Second {
		t.Fatalf("RetryAfter not surfaced: %v", err)
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("sent %d times, want 1", got)
	}
}

func TestContextCancelStopsRetrying(t *testing.T) {
	ts, _ := countingServer(t, `{}`, http.StatusServiceUnavailable)
	c := newTestClient(t, ts.URL, WithRetryPolicy(RetryPolicy{MaxAttempts: 10, BaseDelay: time.Second, MaxDelay: time.Second}))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := c.t.do(ctx, call{method: http.MethodGet, path: "/v2/things", idempotent: true}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("client kept retrying after the context was done")
	}
}

func TestHeaders(t *testing.T) {
	var got http.Header
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotPath = r.Header.Clone(), r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)
	c := newTestClient(t, ts.URL+"/base/", WithUserAgent("rental-operator/0.1.0"))

	if err := c.t.do(context.Background(), call{method: http.MethodPost, path: "/v2/things", body: map[string]string{"a": "b"}}, nil); err != nil {
		t.Fatal(err)
	}
	if v := got.Get("Authorization"); v != "Bearer sk_test_key" {
		t.Errorf("Authorization = %q", v)
	}
	if v := got.Get("User-Agent"); v != "rental-operator/0.1.0 hyperbolic-go/"+version {
		t.Errorf("User-Agent = %q", v)
	}
	if v := got.Get("Content-Type"); v != "application/json" {
		t.Errorf("Content-Type = %q", v)
	}
	if gotPath != "/base/v2/things" {
		t.Errorf("path = %q, want the base URL's path kept", gotPath)
	}
}

func TestNoAuthorizationHeaderWithoutKey(t *testing.T) {
	var auth []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Values("Authorization")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(ts.Close)
	c, err := NewClient("  ", WithBaseURL(ts.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.t.do(context.Background(), call{method: http.MethodGet, path: "/v2/things", idempotent: true}, nil); err != nil {
		t.Fatal(err)
	}
	if len(auth) != 0 {
		t.Fatalf("Authorization sent without a key: %q", auth)
	}
}

func TestDecodeErrorIsReported(t *testing.T) {
	ts, _ := countingServer(t, `not json`, http.StatusOK)
	c := newTestClient(t, ts.URL)

	var out struct{ OK bool }
	err := c.t.do(context.Background(), call{method: http.MethodGet, path: "/v2/things", idempotent: true}, &out)
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("err = %v, want a decode error", err)
	}
}

func TestBaseURLRequiresHTTPSExceptLoopback(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://api.hyperbolic.ai":    true,
		"http://127.0.0.1:8080":        true,
		"http://[::1]:8080":            true,
		"http://localhost:8080":        true,
		"http://api.hyperbolic.ai":     false,
		"http://localhost.example.com": false,
		"ftp://api.hyperbolic.ai":      false,
		"https://":                     false,
		"https://staging.example/api":  true,
	} {
		_, err := NewClient("sk", WithBaseURL(raw))
		if (err == nil) != ok {
			t.Errorf("WithBaseURL(%q): err = %v, want ok=%v", raw, err, ok)
		}
	}
}

func TestWithHTTPClientRejectsNil(t *testing.T) {
	if _, err := NewClient("sk", WithHTTPClient(nil)); err == nil {
		t.Fatal("expected an error for a nil *http.Client")
	}
}

func TestClientDoesNotPrintAPIKey(t *testing.T) {
	c, err := NewClient("sk_live_secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{c.String(), c.GoString()} {
		if strings.Contains(s, "sk_live_secret") {
			t.Fatalf("API key leaked: %s", s)
		}
	}
}

func TestErrorMessages(t *testing.T) {
	apiErr := &APIError{Method: http.MethodPost, Path: "/v2/x", StatusCode: 400, Code: "BAD", Message: "nope"}
	if got := apiErr.Error(); got != "hyperbolic: POST /v2/x: 400 BAD: nope" {
		t.Errorf("APIError.Error() = %q", got)
	}
	unknown := &OutcomeUnknownError{Method: http.MethodPost, Path: "/v2/x", Err: apiErr}
	if !strings.Contains(unknown.Error(), "may have taken effect") || !errors.Is(unknown, ErrOutcomeUnknown) {
		t.Errorf("OutcomeUnknownError = %q", unknown.Error())
	}
	if !errors.As(error(unknown), new(*APIError)) {
		t.Error("OutcomeUnknownError must unwrap to the underlying APIError")
	}
	withIssues := &APIError{Method: http.MethodPost, Path: "/v2/x", StatusCode: 400, Code: "BAD_REQUEST", Message: "Input validation failed", Issues: []string{"gpuCount: too small", "region: unknown"}}
	if got := withIssues.Error(); got != "hyperbolic: POST /v2/x: 400 BAD_REQUEST: Input validation failed (gpuCount: too small; region: unknown)" {
		t.Errorf("APIError.Error() with issues = %q", got)
	}
}
