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
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestCreateDecodeFailureIsOutcomeUnknown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "malformed JSON", status: http.StatusCreated, body: `{"rentalId":`},
		{name: "incompatible type", status: http.StatusOK, body: `{"rentalId":123}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, attempts := countingServer(t, tc.body, tc.status)
			client := newTestClient(t, server.URL, WithRetryPolicy(fastRetries()))

			rental, err := client.Rentals.Create(context.Background(), CreateRentalParams{})
			if rental != nil || err == nil {
				t.Fatalf("expected no usable rental and a decode error, got %v, %v", rental, err)
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("create was sent %d times, want exactly once", got)
			}
			// Preserve the JSON cause so callers can still inspect the failure.
			var syntaxError *json.SyntaxError
			var typeError *json.UnmarshalTypeError
			if !errors.As(err, &syntaxError) && !errors.As(err, &typeError) {
				t.Fatalf("decode cause was lost: %v", err)
			}
			if !errors.Is(err, ErrOutcomeUnknown) || !IsOutcomeUnknown(err) {
				t.Fatalf("a successful create with an unusable response must require reconciliation: %v", err)
			}
			var unknown *OutcomeUnknownError
			if !errors.As(err, &unknown) || unknown.Method != http.MethodPost || unknown.Path != "/v2/on-demand/rentals" {
				t.Fatalf("missing create context on outcome-unknown error: %v", err)
			}
		})
	}
}

func TestReadDecodeFailureRemainsDefinite(t *testing.T) {
	server, attempts := countingServer(t, `{"balanceCents":`, http.StatusOK)
	client := newTestClient(t, server.URL, WithRetryPolicy(fastRetries()))

	balance, err := client.Balances.Get(context.Background())
	var syntaxError *json.SyntaxError
	if balance != nil || !errors.As(err, &syntaxError) || IsOutcomeUnknown(err) {
		t.Fatalf("read decode classification changed: %v, %v", balance, err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("decode failure triggered %d read attempts, want one", got)
	}
}

func TestCreateDefiniteResponseClassification(t *testing.T) {
	t.Run("rejected create", func(t *testing.T) {
		server, attempts := countingServer(t, `{"code":"INVALID_INPUT","message":"invalid shape"}`, http.StatusBadRequest)
		client := newTestClient(t, server.URL, WithRetryPolicy(fastRetries()))

		rental, err := client.Rentals.Create(context.Background(), CreateRentalParams{})
		if rental != nil || StatusCode(err) != http.StatusBadRequest || IsOutcomeUnknown(err) {
			t.Fatalf("rejected create classification changed: %v, %v", rental, err)
		}
		if got := attempts.Load(); got != 1 {
			t.Fatalf("rejected create was sent %d times, want once", got)
		}
	})
	t.Run("decoded create", func(t *testing.T) {
		server, attempts := countingServer(t, `{"rentalId":"rnt_confirmed"}`, http.StatusCreated)
		client := newTestClient(t, server.URL, WithRetryPolicy(fastRetries()))

		rental, err := client.Rentals.Create(context.Background(), CreateRentalParams{})
		if err != nil || rental == nil || rental.RentalID != "rnt_confirmed" {
			t.Fatalf("successful create changed: %v, %v", rental, err)
		}
		if got := attempts.Load(); got != 1 {
			t.Fatalf("successful create was sent %d times, want once", got)
		}
	})
}
