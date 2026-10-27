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
	"net/http"
	"testing"
	"time"

	hyperbolic "github.com/HyperbolicLabs/hyperbolic-go"
	"github.com/HyperbolicLabs/hyperbolic-go/hyperbolictest"
)

// These tests run the client end to end against the in-memory fake, which
// can fail after it has already applied a request: the failure that rents
// GPUs twice if a client gets it wrong.

func h100x1(label string) hyperbolic.CreateRentalParams {
	p := hyperbolic.CreateRentalParams{
		RentalType: hyperbolic.RentalTypeVirtualMachine,
		GPUType:    hyperbolic.Ptr(hyperbolic.GPUTypeH100),
		GPUCount:   1,
		Region:     hyperbolic.Ptr("us-central-1"),
	}
	if label != "" {
		p.Label = hyperbolic.Ptr(label)
	}
	return p
}

// findByLabel lists every active rental carrying label.
func findByLabel(ctx context.Context, t *testing.T, c *hyperbolic.Client, label string) []hyperbolic.RentalWithStatus {
	t.Helper()
	var out []hyperbolic.RentalWithStatus
	for r, err := range c.Rentals.ListActiveAll(ctx, hyperbolic.ListActiveRentalsParams{}) {
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if l, err := r.Label.Get(); err == nil && l == label {
			out = append(out, r)
		}
	}
	return out
}

func TestRentalLifecycle(t *testing.T) {
	srv := hyperbolictest.NewServer(t)
	c := srv.Client(t)
	ctx := context.Background()

	created, err := c.Rentals.Create(ctx, h100x1("train-run-42"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RentalID == "" {
		t.Fatalf("create returned no id: %+v", created)
	}

	active := func() hyperbolic.RentalWithStatus {
		t.Helper()
		got, err := c.Rentals.Get(ctx, hyperbolic.GetRentalParams{RentalID: created.RentalID})
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		a, err := got.AsActiveRentalLookup()
		if err != nil || a.State != hyperbolic.ActiveRentalLookupStateActive {
			t.Fatalf("get: state %q, err %v; want active", a.State, err)
		}
		return a.Rental
	}
	if r := active(); r.Status != hyperbolic.RentalStatusPending {
		t.Fatalf("status = %q, want Pending", r.Status)
	}

	srv.SetRentalStatus(created.RentalID, hyperbolic.RentalStatusRunning)
	r := active()
	if len(r.SSHEndpoints) != 1 || r.SSHEndpoints[0].Host == "" {
		t.Fatalf("ssh endpoints = %+v, want one", r.SSHEndpoints)
	}
	term, err := r.CurrentTerm.AsOnDemandTerm()
	if err != nil || term.CostPerHour != 275 {
		t.Fatalf("term = %+v, err %v; want on-demand at 275 cents/hr", term, err)
	}

	if _, err := c.Rentals.Terminate(ctx, hyperbolic.TerminateRentalParams{RentalID: created.RentalID}); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	got, err := c.Rentals.Get(ctx, hyperbolic.GetRentalParams{RentalID: created.RentalID})
	if err != nil {
		t.Fatalf("get after terminate: %v", err)
	}
	if state, err := got.Discriminator(); err != nil || state != "terminated" {
		t.Fatalf("get after terminate: state %q, err %v; want terminated", state, err)
	}
}

func TestGetUnknownRentalIsNotFound(t *testing.T) {
	srv := hyperbolictest.NewServer(t)
	c := srv.Client(t)
	_, err := c.Rentals.Get(context.Background(), hyperbolic.GetRentalParams{RentalID: "rnt_unknown"})
	if !errors.Is(err, hyperbolic.ErrNotFound) || !hyperbolic.IsNotFound(err) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Terminate reports what the API says. Ending a rental twice is a definite
// "not found", which callers that want idempotence treat as done.
func TestTerminatingAGoneRentalIsNotFound(t *testing.T) {
	srv := hyperbolictest.NewServer(t)
	c := srv.Client(t)
	ctx := context.Background()

	created, err := c.Rentals.Create(ctx, h100x1(""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Rentals.Terminate(ctx, hyperbolic.TerminateRentalParams{RentalID: created.RentalID}); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	for _, id := range []string{created.RentalID, "rnt_unknown"} {
		_, err := c.Rentals.Terminate(ctx, hyperbolic.TerminateRentalParams{RentalID: id})
		if !hyperbolic.IsNotFound(err) || hyperbolic.IsOutcomeUnknown(err) {
			t.Fatalf("terminate %s: err = %v, want a definite not found", id, err)
		}
	}
}

// The core safety property: a create that fails after the server applied it
// must not be retried, and must be reported as outcome-unknown so the caller
// looks the rental up instead of renting twice.
func TestCreateFailingAfterCommitIsOutcomeUnknownAndNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := hyperbolictest.NewServer(t)
			c := srv.Client(t)
			srv.InjectFault(hyperbolictest.Fault{Method: http.MethodPost, Path: hyperbolictest.PathRentals, Status: status, AfterCommit: true})

			_, err := c.Rentals.Create(context.Background(), h100x1("k8s-abc123"))
			if !errors.Is(err, hyperbolic.ErrOutcomeUnknown) {
				t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
			}
			if hyperbolic.StatusCode(err) != status {
				t.Fatalf("wrapped status = %d, want %d", hyperbolic.StatusCode(err), status)
			}
			if n := srv.Requests(http.MethodPost, hyperbolictest.PathRentals); n != 1 {
				t.Fatalf("create sent %d times, want exactly 1", n)
			}
			if found := findByLabel(context.Background(), t, c, "k8s-abc123"); len(found) != 1 {
				t.Fatalf("lookup by label: %d rentals; want exactly the one that was created", len(found))
			}
		})
	}
}

func TestCreateTimeoutIsOutcomeUnknown(t *testing.T) {
	srv := hyperbolictest.NewServer(t)
	c := srv.Client(t, hyperbolic.WithHTTPClient(&http.Client{Timeout: 50 * time.Millisecond}))
	srv.InjectFault(hyperbolictest.Fault{Method: http.MethodPost, Path: hyperbolictest.PathRentals, Delay: 500 * time.Millisecond, AfterCommit: true})

	_, err := c.Rentals.Create(context.Background(), h100x1("slow"))
	if !hyperbolic.IsOutcomeUnknown(err) {
		t.Fatalf("err = %v, want outcome unknown", err)
	}
	if n := srv.Requests(http.MethodPost, hyperbolictest.PathRentals); n != 1 {
		t.Fatalf("create sent %d times, want 1", n)
	}
	if len(srv.ActiveRentals()) != 1 {
		t.Fatalf("the server did not create the rental, so the test setup is wrong")
	}
}

func TestCreateRetriedOnRateLimit(t *testing.T) {
	srv := hyperbolictest.NewServer(t)
	c := srv.Client(t)
	srv.InjectFault(hyperbolictest.Fault{Method: http.MethodPost, Path: hyperbolictest.PathRentals, Status: http.StatusTooManyRequests})

	if _, err := c.Rentals.Create(context.Background(), h100x1("")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if n := srv.Requests(http.MethodPost, hyperbolictest.PathRentals); n != 2 {
		t.Fatalf("create sent %d times, want 2 (one 429, one success)", n)
	}
	if n := len(srv.ActiveRentals()); n != 1 {
		t.Fatalf("%d rentals exist, want 1", n)
	}
}

func TestCreateClientErrorsAreDefinite(t *testing.T) {
	srv := hyperbolictest.NewServer(t)
	c := srv.Client(t)
	srv.SetBalance(100, 0) // $1, less than an hour of anything

	_, err := c.Rentals.Create(context.Background(), h100x1(""))
	if !hyperbolic.HasCode(err, hyperbolic.CodeInsufficientBalance) {
		t.Fatalf("err = %v, want INSUFFICIENT_BALANCE", err)
	}
	if hyperbolic.IsOutcomeUnknown(err) {
		t.Fatal("a 4xx is a definite failure, not outcome unknown")
	}
	if n := srv.Requests(http.MethodPost, hyperbolictest.PathRentals); n != 1 {
		t.Fatalf("create sent %d times, want 1 (4xx must not be retried)", n)
	}
}

func TestReadsRetryTransientFailures(t *testing.T) {
	srv := hyperbolictest.NewServer(t)
	c := srv.Client(t)
	srv.InjectFault(hyperbolictest.Fault{Method: http.MethodGet, Path: hyperbolictest.PathActiveRentals, Status: http.StatusServiceUnavailable, Times: 2})

	if _, err := c.Rentals.ListActive(context.Background(), hyperbolic.ListActiveRentalsParams{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if n := srv.Requests(http.MethodGet, hyperbolictest.PathActiveRentals); n != 3 {
		t.Fatalf("list sent %d times, want 3", n)
	}
}

func TestExternalTerminationIsVisible(t *testing.T) {
	srv := hyperbolictest.NewServer(t)
	c := srv.Client(t)
	ctx := context.Background()

	created, err := c.Rentals.Create(ctx, h100x1("ext"))
	if err != nil {
		t.Fatal(err)
	}
	srv.TerminateExternally(created.RentalID)
	if found := findByLabel(ctx, t, c, "ext"); len(found) != 0 {
		t.Fatalf("found %d, but a rental the platform ended is no longer listed", len(found))
	}
}

func TestUnauthorizedAgainstFake(t *testing.T) {
	srv := hyperbolictest.NewServer(t)
	c, err := hyperbolic.NewClient("sk_wrong", hyperbolic.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Balances.Get(context.Background()); !hyperbolic.IsUnauthorized(err) {
		t.Fatalf("err = %v, want unauthorized", err)
	}
	if n := srv.Requests(http.MethodGet, hyperbolictest.PathBalance); n != 1 {
		t.Fatalf("sent %d times, want 1", n)
	}
}

func TestRentalOptionsNeedNoKey(t *testing.T) {
	srv := hyperbolictest.NewServer(t)
	c, err := hyperbolic.NewClient("", hyperbolic.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	opts, err := c.Rentals.Options(context.Background())
	if err != nil || len(opts) == 0 {
		t.Fatalf("options = %d, err = %v", len(opts), err)
	}
}

func TestUnmodeledOperationsFailLoudly(t *testing.T) {
	srv := hyperbolictest.NewServer(t)
	c := srv.Client(t)
	_, err := c.StorageVolumes.List(context.Background())
	if hyperbolic.StatusCode(err) != http.StatusNotImplemented {
		t.Fatalf("err = %v, want 501 from the fake", err)
	}
}
