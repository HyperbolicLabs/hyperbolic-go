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
	"net/http"
	"slices"
	"testing"
	"time"
)

func TestParseErrorBody(t *testing.T) {
	cases := []struct {
		name, body, code, msg string
		issues                []string
	}{
		{"flat", `{"code":"INSUFFICIENT_BALANCE","message":"top up"}`, "INSUFFICIENT_BALANCE", "top up", nil},
		{"spec envelope with issues", `{"code":"BAD_REQUEST","message":"Input validation failed","issues":[{"message":"gpuCount: too small"},{"message":""}]}`,
			"BAD_REQUEST", "Input validation failed", []string{"gpuCount: too small"}},
		{"trpc data code", `{"error":{"message":"nope","code":-32600,"data":{"code":"REGION_DISABLED"}}}`, "REGION_DISABLED", "nope", nil},
		{"trpc string code", `{"error":{"message":"gone","code":"NOT_FOUND"}}`, "NOT_FOUND", "gone", nil},
		{"not json", `<html>bad gateway</html>`, "", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg, issues := parseErrorBody([]byte(tc.body))
			if code != tc.code || msg != tc.msg || !slices.Equal(issues, tc.issues) {
				t.Fatalf("got (%q, %q, %q), want (%q, %q, %q)", code, msg, issues, tc.code, tc.msg, tc.issues)
			}
		})
	}
}

func TestHasCodeFindsNestedCodes(t *testing.T) {
	err := &APIError{StatusCode: 400, Body: `{"error":{"data":{"details":[{"reason":"CUSTOM_PORTS_NOT_SUPPORTED_FOR_REGION"}]}}}`}
	if !HasCode(err, CodeCustomPortsNotSupportedForRegion) {
		t.Fatal("expected nested code to match")
	}
	if HasCode(err, "PORTS") {
		t.Fatal("partial code must not match")
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	if d := parseRetryAfter("7", now); d != 7*time.Second {
		t.Errorf("seconds: %v", d)
	}
	if d := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); d != 90*time.Second {
		t.Errorf("http date: %v", d)
	}
	if d := parseRetryAfter("soon", now); d != 0 {
		t.Errorf("garbage: %v", d)
	}
}

func TestBackoffStaysWithinBounds(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 10, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}
	for attempt := 1; attempt <= 9; attempt++ {
		for i := 0; i < 50; i++ {
			d := p.backoff(attempt)
			if d < 0 || d > time.Second {
				t.Fatalf("attempt %d: backoff %v out of [0, 1s]", attempt, d)
			}
		}
	}
}

func TestRetryableRules(t *testing.T) {
	api := func(code int) error { return &APIError{StatusCode: code} }
	sent := &transportError{err: errors.New("reset"), mayHaveBeenSent: true}
	unsent := &transportError{err: errors.New("refused"), mayHaveBeenSent: false}
	cases := []struct {
		err        error
		idempotent bool
		want       bool
	}{
		{api(429), false, true},
		{api(503), true, true},
		{api(503), false, false},
		{api(500), true, false},
		{api(400), true, false},
		{sent, true, true},
		{sent, false, false},
		{unsent, false, true},
	}
	for _, tc := range cases {
		if got := retryable(tc.err, tc.idempotent); got != tc.want {
			t.Errorf("retryable(%v, idempotent=%v) = %v, want %v", tc.err, tc.idempotent, got, tc.want)
		}
	}
}
