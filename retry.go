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
	"errors"
	"math/rand/v2"
	"net/http"
	"time"
)

// RetryPolicy controls how the client retries failed requests.
//
// Reads and other idempotent calls are retried on 429, 502, 503, 504 and
// transport errors. Non-idempotent calls (create, reboot) are retried only on
// 429 or when the connection was never established, because anything else
// might have taken effect.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts, including the first.
	// Values below 1 mean a single attempt.
	MaxAttempts int
	// BaseDelay is the delay before the first retry. It doubles each attempt.
	BaseDelay time.Duration
	// MaxDelay caps the delay between attempts. If the server asks for a
	// longer Retry-After than this, the client stops and returns the error so
	// the caller can reschedule instead of blocking.
	MaxDelay time.Duration
}

// DefaultRetryPolicy is 3 attempts with exponential backoff from 500ms,
// capped at 10s.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, BaseDelay: 500 * time.Millisecond, MaxDelay: 10 * time.Second}
}

// NoRetries disables retries. Controllers that requeue on error often prefer
// this, so a reconcile never blocks on backoff.
func NoRetries() RetryPolicy { return RetryPolicy{MaxAttempts: 1} }

func (p RetryPolicy) attempts() int {
	if p.MaxAttempts < 1 {
		return 1
	}
	return p.MaxAttempts
}

// decide reports whether to retry after a failed attempt, and how long to wait.
func (p RetryPolicy) decide(attempt, maxAttempts int, idempotent bool, err error) (bool, time.Duration) {
	if attempt >= maxAttempts || !retryable(err, idempotent) {
		return false, 0
	}
	wait := p.backoff(attempt)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		if p.MaxDelay > 0 && apiErr.RetryAfter > p.MaxDelay {
			return false, 0
		}
		wait = apiErr.RetryAfter
	}
	return true, wait
}

func (p RetryPolicy) backoff(attempt int) time.Duration {
	base := p.BaseDelay
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if p.MaxDelay > 0 && d >= p.MaxDelay {
			d = p.MaxDelay
			break
		}
	}
	if p.MaxDelay > 0 && d > p.MaxDelay {
		d = p.MaxDelay
	}
	// Jitter in [d/2, d] so many clients do not retry in lockstep.
	half := d / 2
	return half + rand.N(half+1) //nolint:gosec // retry jitter does not need a cryptographic RNG
}

func retryable(err error, idempotent bool) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusTooManyRequests:
			// Rejected before processing, so safe for any call.
			return true
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return idempotent
		}
		return false
	}
	var te *transportError
	if errors.As(err, &te) {
		return idempotent || !te.mayHaveBeenSent
	}
	return false
}
