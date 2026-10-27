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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Error codes returned by the API. The first five were observed against the
// live API while building the operator prototype. RENTAL_NOT_FOUND and
// ALREADY_TERMINATED are handled defensively and still need confirming with
// the platform team.
const (
	CodeInsufficientBalance              = "INSUFFICIENT_BALANCE"
	CodeNoPublicKey                      = "NO_PUBLIC_KEY"
	CodeRegionDisabled                   = "REGION_DISABLED"
	CodeCustomPortsNotSupportedForRegion = "CUSTOM_PORTS_NOT_SUPPORTED_FOR_REGION"
	// CodeFailedToCalculateCostPerHour is what the API returns when no
	// offering matches the requested shape, fabric and region.
	CodeFailedToCalculateCostPerHour = "FAILED_TO_CALCULATE_COST_PER_HOUR"
	CodeRentalNotFound               = "RENTAL_NOT_FOUND"
	CodeAlreadyTerminated            = "ALREADY_TERMINATED"
	CodeNotFound                     = "NOT_FOUND"
	// The SSH key codes are documented by the spec. Saving a key the account
	// already has, with the same type and body, returns
	// SSH_PUBLIC_KEY_ALREADY_EXISTS. Deleting a key that a live rental uses
	// returns SSH_PUBLIC_KEY_IN_USE.
	CodeSSHPublicKeyAlreadyExists = "SSH_PUBLIC_KEY_ALREADY_EXISTS"
	CodeSSHPublicKeyInUse         = "SSH_PUBLIC_KEY_IN_USE"
	CodeSSHPublicKeyNotFound      = "SSH_PUBLIC_KEY_NOT_FOUND"
)

// ErrNotFound is returned (wrapped) when a requested object does not exist.
var ErrNotFound = errors.New("hyperbolic: not found")

// ErrOutcomeUnknown matches errors from non-idempotent calls (such as creating
// a rental) whose effect is unknown: the server may or may not have applied
// the request. Callers must look up the result before retrying.
var ErrOutcomeUnknown = errors.New("hyperbolic: outcome unknown")

// APIError is a non-2xx response from the API.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	// Code is the machine-readable error code (for example
	// INSUFFICIENT_BALANCE), when the API provided one.
	Code    string
	Message string
	// Issues lists what the API said was wrong with the request, if anything.
	Issues []string
	// RetryAfter is the server's Retry-After hint, if any.
	RetryAfter time.Duration
	// Body is the raw response body, truncated.
	Body string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "hyperbolic: %s %s: %d", e.Method, e.Path, e.StatusCode)
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	}
	msg := e.Message
	if msg == "" {
		msg = strings.TrimSpace(e.Body)
	}
	if msg != "" {
		b.WriteString(": " + msg)
	}
	if len(e.Issues) > 0 {
		b.WriteString(" (" + strings.Join(e.Issues, "; ") + ")")
	}
	return b.String()
}

// hasCode matches the parsed code, or the code as a JSON string anywhere in
// the body (the API's error envelope varies between endpoints).
func (e *APIError) hasCode(code string) bool {
	return strings.EqualFold(e.Code, code) || strings.Contains(e.Body, `"`+code+`"`)
}

// OutcomeUnknownError wraps the underlying failure of a non-idempotent call
// whose effect is unknown. It matches [ErrOutcomeUnknown] with errors.Is.
type OutcomeUnknownError struct {
	Method string
	Path   string
	Err    error
}

func (e *OutcomeUnknownError) Error() string {
	return "outcome unknown, the request may have taken effect: " + e.Err.Error()
}

func (e *OutcomeUnknownError) Unwrap() error { return e.Err }

// Is reports whether target is ErrOutcomeUnknown.
func (e *OutcomeUnknownError) Is(target error) bool { return target == ErrOutcomeUnknown }

// transportError is a failure with no HTTP response.
type transportError struct {
	method, path    string
	err             error
	mayHaveBeenSent bool
}

func (e *transportError) Error() string {
	return fmt.Sprintf("hyperbolic: %s %s: %v", e.method, e.path, e.err)
}

func (e *transportError) Unwrap() error { return e.err }

// HasCode reports whether err carries the given API error code.
func HasCode(err error, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.hasCode(code)
}

// StatusCode returns the HTTP status of an API error, or 0.
func StatusCode(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// IsNotFound reports whether err means the object does not exist.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || StatusCode(err) == http.StatusNotFound ||
		HasCode(err, CodeRentalNotFound) || HasCode(err, CodeNotFound) ||
		HasCode(err, CodeSSHPublicKeyNotFound)
}

// IsUnauthorized reports whether the API key was rejected.
func IsUnauthorized(err error) bool {
	code := StatusCode(err)
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

// IsRateLimited reports whether the request was rejected by rate limiting.
func IsRateLimited(err error) bool { return StatusCode(err) == http.StatusTooManyRequests }

// IsOutcomeUnknown is shorthand for errors.Is(err, ErrOutcomeUnknown).
func IsOutcomeUnknown(err error) bool { return errors.Is(err, ErrOutcomeUnknown) }

const maxErrorBody = 2048

func newAPIError(method, path string, resp *http.Response, body []byte) *APIError {
	e := &APIError{
		Method:     method,
		Path:       path,
		StatusCode: resp.StatusCode,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
	}
	if len(body) > maxErrorBody {
		e.Body = string(body[:maxErrorBody])
	} else {
		e.Body = string(body)
	}
	e.Code, e.Message, e.Issues = parseErrorBody(body)
	return e
}

// parseErrorBody extracts the code, message and issues from an error body:
// the {code, message, issues} envelope the spec documents, or the
// tRPC-style nested error some endpoints were seen to return.
func parseErrorBody(body []byte) (code, message string, issues []string) {
	var env struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Issues  []struct {
			Message string `json:"message"`
		} `json:"issues"`
		Error struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Code string `json:"code"`
			} `json:"data"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil {
		return "", "", nil
	}
	switch {
	case env.Code != "":
		code = env.Code
	case env.Error.Data.Code != "":
		code = env.Error.Data.Code
	default:
		if s, ok := env.Error.Code.(string); ok {
			code = s
		}
	}
	message = env.Message
	if message == "" {
		message = env.Error.Message
	}
	for _, i := range env.Issues {
		if i.Message != "" {
			issues = append(issues, i.Message)
		}
	}
	return code, message, issues
}

// isNull reports whether a response body is JSON null.
func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// notFound is the error for a call the API answered with null because
// nothing matched.
func notFound(c call) error {
	return fmt.Errorf("hyperbolic: %s %s: %w", c.method, c.path, ErrNotFound)
}

// decodeError wraps a 2xx response the client could not decode.
func decodeError(c call, err error) error {
	return fmt.Errorf("hyperbolic: decode %s %s response: %w", c.method, c.path, err)
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
