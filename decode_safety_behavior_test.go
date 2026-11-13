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

package hyperbolic_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	hyperbolic "github.com/HyperbolicLabs/hyperbolic-go"
	"github.com/HyperbolicLabs/hyperbolic-go/hyperbolictest"
)

func TestUndecodableCreateRequiresLookupAfterCommit(t *testing.T) {
	server := hyperbolictest.NewServer(t)
	client := server.Client(t, hyperbolic.WithHTTPClient(&http.Client{
		Transport: corruptCreateResponse{base: http.DefaultTransport},
	}))
	ctx := context.Background()
	const label = "decode-failed-create"

	rental, err := client.Rentals.Create(ctx, h100x1(label))
	if rental != nil || err == nil {
		t.Fatalf("expected an unusable create response, got %v, %v", rental, err)
	}
	if got := server.Requests(http.MethodPost, hyperbolictest.PathRentals); got != 1 {
		t.Fatalf("create reached the server %d times, want one", got)
	}
	if found := findByLabel(ctx, t, client, label); len(found) != 1 {
		t.Fatalf("lookup found %d rentals, want the one already created", len(found))
	}
	if !errors.Is(err, hyperbolic.ErrOutcomeUnknown) {
		t.Fatalf("caller would treat an existing rental as a definite failure: %v", err)
	}
}

// --- Test transport helpers ---

// corruptCreateResponse models a lost result after the fake has committed a rental.
type corruptCreateResponse struct {
	base http.RoundTripper
}

func (c corruptCreateResponse) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := c.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if request.Method != http.MethodPost || request.URL.Path != hyperbolictest.PathRentals ||
		response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, nil
	}
	_, drainErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if drainErr != nil {
		return nil, drainErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	const body = `{"rentalId":`
	response.Body = io.NopCloser(strings.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Del("Content-Length")
	return response, nil
}

// --- End test transport helpers ---
