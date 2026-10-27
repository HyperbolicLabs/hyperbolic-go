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

package hyperbolictest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	hyperbolic "github.com/HyperbolicLabs/hyperbolic-go"
)

func vm(gpuType hyperbolic.GPUType, gpus int, region, label string) hyperbolic.CreateRentalParams {
	p := hyperbolic.CreateRentalParams{RentalType: hyperbolic.RentalTypeVirtualMachine, GPUType: &gpuType, GPUCount: gpus, Region: &region}
	if label != "" {
		p.Label = &label
	}
	return p
}

func h100x1(label string) hyperbolic.CreateRentalParams {
	return vm(hyperbolic.GPUTypeH100, 1, "us-central-1", label)
}

// listAll lists every active rental through the client.
func listAll(ctx context.Context, t *testing.T, c *hyperbolic.Client) []hyperbolic.RentalWithStatus {
	t.Helper()
	var out []hyperbolic.RentalWithStatus
	for r, err := range c.Rentals.ListActiveAll(ctx, hyperbolic.ListActiveRentalsParams{}) {
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// raw sends a request without the SDK, so the fake's own behavior is tested
// independently of the client's retries and error parsing.
func raw(t *testing.T, s *Server, method, path, key string, body any) (int, map[string]any, http.Header) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(context.Background(), method, s.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func TestDefaults(t *testing.T) {
	s := NewServer(t)
	c := s.Client(t)
	ctx := context.Background()

	opts, err := c.Rentals.Options(ctx)
	if err != nil || len(opts) != len(DefaultRentalOptions()) {
		t.Fatalf("options = %d, err %v", len(opts), err)
	}
	b, err := c.Balances.Get(ctx)
	if err != nil || b.BalanceCents != 100_000 {
		t.Fatalf("balance = %+v, err %v; want $1,000", b, err)
	}
	if rentals := listAll(ctx, t, c); len(rentals) != 0 {
		t.Fatalf("rentals = %d; want none", len(rentals))
	}
}

func TestAuth(t *testing.T) {
	s := NewServer(t)
	if status, _, _ := raw(t, s, http.MethodGet, PathRentalOptions, "", nil); status != http.StatusOK {
		t.Errorf("rental options without a key: %d, want 200", status)
	}
	if status, body, _ := raw(t, s, http.MethodGet, PathBalance, "sk_wrong", nil); status != http.StatusUnauthorized || body["code"] != "UNAUTHORIZED" {
		t.Errorf("wrong key: %d %v, want 401 UNAUTHORIZED", status, body)
	}
	if status, _, _ := raw(t, s, http.MethodGet, PathBalance, DefaultAPIKey, nil); status != http.StatusOK {
		t.Errorf("default key: %d, want 200", status)
	}
	if status, body, _ := raw(t, s, http.MethodGet, "/v2/nope", DefaultAPIKey, nil); status != http.StatusNotFound || body["code"] != "NOT_FOUND" {
		t.Errorf("unknown route: %d %v", status, body)
	}
}

func TestCreateChecksOptionsAndBalance(t *testing.T) {
	s := NewServer(t)
	c := s.Client(t)
	ctx := context.Background()

	_, err := c.Rentals.Create(ctx, vm(hyperbolic.GPUTypeB200, 8, "us-central-1", ""))
	if !hyperbolic.HasCode(err, hyperbolic.CodeFailedToCalculateCostPerHour) {
		t.Errorf("no matching offering: err = %v", err)
	}
	ethernet := vm(hyperbolic.GPUTypeH100, 8, "us-central-1", "")
	ethernet.NetworkType = hyperbolic.Ptr(hyperbolic.CreateRentalParamsNetworkTypeEthernet)
	_, err = c.Rentals.Create(ctx, ethernet)
	if !hyperbolic.HasCode(err, hyperbolic.CodeFailedToCalculateCostPerHour) {
		t.Errorf("wrong fabric: err = %v", err)
	}
	s.SetBalance(100, 100) // $2 available, an h100x1 costs $2.75/hr
	if _, err = c.Rentals.Create(ctx, h100x1("")); !hyperbolic.HasCode(err, hyperbolic.CodeInsufficientBalance) {
		t.Errorf("low balance: err = %v", err)
	}
	if n := len(s.ActiveRentals()); n != 0 {
		t.Fatalf("%d rentals created by failed requests", n)
	}

	s.SetBalance(100_000, 0)
	created, err := c.Rentals.Create(ctx, h100x1("lbl"))
	if err != nil {
		t.Fatal(err)
	}
	if label, _ := created.Label.Get(); label != "lbl" || created.Region != "us-central-1" {
		t.Fatalf("created = %+v", created)
	}
	got := s.ActiveRentals()
	if len(got) != 1 || got[0].Status != hyperbolic.RentalStatusPending {
		t.Fatalf("active = %+v", got)
	}
	if term, err := got[0].CurrentTerm.AsOnDemandTerm(); err != nil || term.CostPerHour != 275 {
		t.Fatalf("term = %+v, err %v; want on-demand at 275 cents/hr", term, err)
	}
}

func TestSetRentalOptions(t *testing.T) {
	s := NewServer(t)
	s.SetRentalOptions(VirtualMachineOption(hyperbolic.VirtualMachineOption{Enabled: true, Region: "eu-north-1", GPUType: hyperbolic.GPUTypeB200, GPUCount: 8, CostPerHourCents: 4800}))
	opts, err := s.Client(t).Rentals.Options(context.Background())
	if err != nil || len(opts) != 1 {
		t.Fatalf("options = %+v, err %v", opts, err)
	}
	if o, err := opts[0].AsVirtualMachineOption(); err != nil || o.GPUType != hyperbolic.GPUTypeB200 || o.MachineType != hyperbolic.VirtualMachineOptionMachineTypeVirtualMachine {
		t.Fatalf("option = %+v, err %v", o, err)
	}
}

func TestSetRentalStatusFillsInstances(t *testing.T) {
	s := NewServer(t)
	s.SetRentalOptions(VirtualMachineOption(hyperbolic.VirtualMachineOption{Enabled: true, Region: "us-central-1", GPUType: hyperbolic.GPUTypeH100, GPUCount: 16, CostPerHourCents: 4400}))
	created, err := s.Client(t).Rentals.Create(context.Background(), vm(hyperbolic.GPUTypeH100, 16, "us-central-1", ""))
	if err != nil {
		t.Fatal(err)
	}
	if s.SetRentalStatus("rnt_unknown", hyperbolic.RentalStatusRunning) {
		t.Error("SetRentalStatus reported an unknown rental as existing")
	}
	if !s.SetRentalStatus(created.RentalID, hyperbolic.RentalStatusRunning) {
		t.Fatal("SetRentalStatus: rental not found")
	}
	r := s.ActiveRentals()[0]
	if r.Instances == nil || len(*r.Instances) != 2 || len(r.SSHEndpoints) != 2 {
		t.Fatalf("16 GPUs = %v instances, %d SSH endpoints; want 2 of each (8 GPUs per node)", r.Instances, len(r.SSHEndpoints))
	}
	for _, in := range *r.Instances {
		if ssh, _ := in.SSH.Get(); in.Phase != string(hyperbolic.RentalStatusRunning) || ssh == "" || !in.SupportsReboot {
			t.Errorf("instance = %+v", in)
		}
	}
	s.SetRentalStatus(created.RentalID, hyperbolic.RentalStatusRebooting)
	if ph := (*s.ActiveRentals()[0].Instances)[0].Phase; ph != string(hyperbolic.RentalStatusRebooting) {
		t.Errorf("instance phase = %q, want it to follow the rental", ph)
	}
}

func TestTerminateExternally(t *testing.T) {
	s := NewServer(t)
	c := s.Client(t)
	created, err := c.Rentals.Create(context.Background(), h100x1(""))
	if err != nil {
		t.Fatal(err)
	}
	if !s.TerminateExternally(created.RentalID) {
		t.Fatal("TerminateExternally: rental not found")
	}
	if n := len(s.ActiveRentals()); n != 0 {
		t.Fatalf("%d active rentals after external termination", n)
	}
	got, err := c.Rentals.Get(context.Background(), hyperbolic.GetRentalParams{RentalID: created.RentalID})
	if err != nil {
		t.Fatal(err)
	}
	if ended, err := got.AsTerminatedRentalLookup(); err != nil || ended.State != hyperbolic.TerminatedRentalLookupStateTerminated {
		t.Fatalf("get: %+v, err %v; want the rental, terminated", ended, err)
	}
}

func TestActiveRentalSummary(t *testing.T) {
	s := NewServer(t)
	ctx := context.Background()
	c := s.Client(t)
	for _, p := range []hyperbolic.CreateRentalParams{h100x1(""), h100x1(""), vm(hyperbolic.GPUTypeH100, 8, "us-central-1", "")} {
		if _, err := c.Rentals.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	s.TerminateExternally(s.ActiveRentals()[0].RentalID) // the newest: the 8x rental

	got, err := c.Rentals.ActiveSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Two 1x rentals at 275 cents an hour remain.
	want := hyperbolic.RentalTermTypeSummary{Count: 2, CostPerHourCents: 550}
	if got.Count != want.Count || got.CostPerHourCents != want.CostPerHourCents || got.ByTermType.OnDemand != want {
		t.Errorf("summary = %+v, want %+v in total and on demand", got, want)
	}
	if got.ByTermType.Reserved != (hyperbolic.RentalTermTypeSummary{}) || got.ByTermType.PrivateCloud != (hyperbolic.RentalTermTypeSummary{}) {
		t.Errorf("summary by term type = %+v, want on-demand only", got.ByTermType)
	}
	if status, _, _ := raw(t, s, http.MethodGet, PathActiveRentalSummary, "", nil); status != http.StatusUnauthorized {
		t.Errorf("summary without a key: %d, want 401", status)
	}
}

func TestTerminateAndReboot(t *testing.T) {
	s := NewServer(t)
	ctx := context.Background()
	c := s.Client(t)
	created, err := c.Rentals.Create(ctx, h100x1(""))
	if err != nil {
		t.Fatal(err)
	}
	status, body, _ := raw(t, s, http.MethodPost, PathReboot, DefaultAPIKey, map[string]any{"rentalId": created.RentalID, "instanceRef": "x"})
	if status != http.StatusBadRequest || body["code"] != "RENTAL_NOT_RUNNING" {
		t.Errorf("reboot while pending: %d %v", status, body)
	}
	s.SetRentalStatus(created.RentalID, hyperbolic.RentalStatusRunning)
	ref := (*s.ActiveRentals()[0].Instances)[0].InstanceRef
	if _, err := c.Rentals.RebootInstance(ctx, hyperbolic.RebootRentalInstanceParams{RentalID: created.RentalID, InstanceRef: ref}); err != nil {
		t.Errorf("reboot while running: %v", err)
	}
	if status, body, _ := raw(t, s, http.MethodPost, PathTerminate, DefaultAPIKey, map[string]any{"rentalId": created.RentalID}); status != http.StatusOK {
		t.Errorf("terminate: %d %v", status, body)
	}
	if status, body, _ := raw(t, s, http.MethodPost, PathTerminate, DefaultAPIKey, map[string]any{"rentalId": created.RentalID}); status != http.StatusNotFound || body["code"] != hyperbolic.CodeRentalNotFound {
		t.Errorf("second terminate: %d %v, want 404 RENTAL_NOT_FOUND", status, body)
	}
	if status, _, _ := raw(t, s, http.MethodPost, PathReboot, DefaultAPIKey, map[string]any{"rentalId": created.RentalID, "instanceRef": "x"}); status != http.StatusNotFound {
		t.Errorf("reboot after terminate: %d, want 404", status)
	}
	for _, p := range []string{PathRentals, PathTerminate, PathReboot} {
		if status, _, _ := raw(t, s, http.MethodPost, p, DefaultAPIKey, "not an object"); status != http.StatusBadRequest {
			t.Errorf("POST %s with a bad body: %d, want 400", p, status)
		}
	}
}

func TestCreateRequiresRentalType(t *testing.T) {
	s := NewServer(t)
	if status, body, _ := raw(t, s, http.MethodPost, PathRentals, DefaultAPIKey, map[string]any{"gpuType": "h100", "gpuCount": 1}); status != http.StatusBadRequest {
		t.Fatalf("create without rentalType: %d %v, want 400", status, body)
	}
	if n := len(s.ActiveRentals()); n != 0 {
		t.Fatalf("%d rentals created by a rejected request", n)
	}
}

func TestListPages(t *testing.T) {
	s := NewServer(t)
	s.SetMaxPageSize(2)
	c := s.Client(t)
	ctx := context.Background()
	var newestFirst []string
	for i := 0; i < 5; i++ {
		created, err := c.Rentals.Create(ctx, h100x1(""))
		if err != nil {
			t.Fatal(err)
		}
		newestFirst = append([]string{created.RentalID}, newestFirst...)
	}

	status, body, _ := raw(t, s, http.MethodGet, PathActiveRentals+"?page=3&pageSize=2", DefaultAPIKey, nil)
	items, _ := body["items"].([]any)
	if status != http.StatusOK || body["total"] != float64(5) || body["totalPages"] != float64(3) || len(items) != 1 {
		t.Fatalf("page 3: %d %v", status, body)
	}
	if status, _, _ := raw(t, s, http.MethodGet, PathActiveRentals+"?pageSize=101", DefaultAPIKey, nil); status != http.StatusBadRequest {
		t.Errorf("pageSize 101: %d, want 400", status)
	}

	before := s.Requests(http.MethodGet, PathActiveRentals)
	var got []string
	for _, r := range listAll(ctx, t, c) {
		got = append(got, r.RentalID)
	}
	if strings.Join(got, ",") != strings.Join(newestFirst, ",") {
		t.Fatalf("listed %v, want %v (every page, newest first)", got, newestFirst)
	}
	if n := s.Requests(http.MethodGet, PathActiveRentals) - before; n != 3 {
		t.Fatalf("the client read %d pages, want 3", n)
	}
}

func TestFaults(t *testing.T) {
	s := NewServer(t)
	s.InjectFault(Fault{Method: http.MethodGet, Path: PathBalance, Status: http.StatusServiceUnavailable, RetryAfter: 1500 * time.Millisecond, Times: 2})

	for i := 0; i < 2; i++ {
		status, body, hdr := raw(t, s, http.MethodGet, PathBalance, DefaultAPIKey, nil)
		if status != http.StatusServiceUnavailable || body["code"] != "SERVICE_UNAVAILABLE" || hdr.Get("Retry-After") != "2" {
			t.Fatalf("faulted request %d: %d %v Retry-After=%q", i+1, status, body, hdr.Get("Retry-After"))
		}
	}
	if status, _, _ := raw(t, s, http.MethodGet, PathBalance, DefaultAPIKey, nil); status != http.StatusOK {
		t.Fatalf("fault applied more than Times: %d", status)
	}
	if n := s.Requests(http.MethodGet, PathBalance); n != 3 {
		t.Fatalf("Requests = %d, want 3 (faulted requests count too)", n)
	}

	s.InjectFault(Fault{Method: http.MethodGet, Path: PathBalance, Status: http.StatusBadRequest, Code: "CUSTOM", Message: "custom fault"})
	if status, body, _ := raw(t, s, http.MethodGet, PathBalance, DefaultAPIKey, nil); status != http.StatusBadRequest || body["code"] != "CUSTOM" || body["message"] != "custom fault" {
		t.Fatalf("custom fault: %d %v", status, body)
	}
}

func TestFaultAfterCommitAppliesThenFails(t *testing.T) {
	s := NewServer(t)
	s.InjectFault(Fault{Method: http.MethodPost, Path: PathRentals, Status: http.StatusBadGateway, AfterCommit: true})
	status, _, _ := raw(t, s, http.MethodPost, PathRentals, DefaultAPIKey, h100x1("x"))
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", status)
	}
	if n := len(s.ActiveRentals()); n != 1 {
		t.Fatalf("%d rentals, want the one created before the failure", n)
	}
}

func TestFaultDelayOnly(t *testing.T) {
	s := NewServer(t)
	s.InjectFault(Fault{Method: http.MethodGet, Path: PathBalance, Delay: 50 * time.Millisecond})
	start := time.Now()
	if status, _, _ := raw(t, s, http.MethodGet, PathBalance, DefaultAPIKey, nil); status != http.StatusOK {
		t.Fatalf("status = %d, want a normal answer after the delay", status)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("delay not applied")
	}
	s.InjectFault(Fault{Method: http.MethodGet, Path: PathBalance, Delay: 10 * time.Millisecond, AfterCommit: true})
	if status, _, _ := raw(t, s, http.MethodGet, PathBalance, DefaultAPIKey, nil); status != http.StatusOK {
		t.Fatalf("after-commit delay without a status: %d, want 200", status)
	}
}

// Runs under the race detector: the fake must be safe for the concurrent
// reconciles an operator test drives against it.
func TestConcurrentUse(t *testing.T) {
	s := NewServer(t)
	c := s.Client(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, err := c.Rentals.Create(ctx, h100x1(""))
			if err != nil {
				t.Error(err)
				return
			}
			s.SetRentalStatus(created.RentalID, hyperbolic.RentalStatusRunning)
			if _, err := c.Rentals.ListActive(ctx, hyperbolic.ListActiveRentalsParams{}); err != nil {
				t.Error(err)
			}
			if _, err := c.Rentals.Get(ctx, hyperbolic.GetRentalParams{RentalID: created.RentalID}); err != nil {
				t.Error(err)
			}
			s.SetRentalStatus(created.RentalID, hyperbolic.RentalStatusRebooting)
		}()
	}
	wg.Wait()
	if n := len(s.ActiveRentals()); n != 8 {
		t.Fatalf("%d rentals, want 8", n)
	}
}

const otherKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE90aGVyLWtleS1mb3ItaHlwZXJib2xpY3Rlc3QtMDAw alice@laptop"

func TestSSHKeys(t *testing.T) {
	s := NewServer(t)
	c := s.Client(t)
	ctx := context.Background()

	keys, err := c.SSHKeys.List(ctx)
	if err != nil || len(keys) != 1 || keys[0].PublicKey != DefaultSSHPublicKey {
		t.Fatalf("keys = %+v, err %v; want the default key", keys, err)
	}
	added, err := c.SSHKeys.Create(ctx, hyperbolic.CreateSSHKeyParams{PublicKey: otherKey, Name: hyperbolic.Ptr("alice")})
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := added.Name.Get(); name != "alice" || added.ID == keys[0].ID {
		t.Fatalf("created %+v", added)
	}
	// The same type and body under another comment is a duplicate.
	dup := strings.TrimSuffix(otherKey, "alice@laptop") + "alice@desktop"
	if _, err := c.SSHKeys.Create(ctx, hyperbolic.CreateSSHKeyParams{PublicKey: dup}); !hyperbolic.HasCode(err, hyperbolic.CodeSSHPublicKeyAlreadyExists) {
		t.Fatalf("duplicate: err %v, want %s", err, hyperbolic.CodeSSHPublicKeyAlreadyExists)
	}
	if status, _, _ := raw(t, s, http.MethodPost, PathSSHKeys, DefaultAPIKey, map[string]any{"publicKey": " "}); status != http.StatusBadRequest {
		t.Errorf("blank key: %d, want 400", status)
	}
	renamed, err := c.SSHKeys.Update(ctx, hyperbolic.UpdateSSHKeyParams{ID: added.ID, Name: "alice-laptop"})
	if name, _ := renamed.Name.Get(); err != nil || name != "alice-laptop" {
		t.Fatalf("rename: %+v, err %v", renamed, err)
	}
	if _, err := c.SSHKeys.Delete(ctx, hyperbolic.DeleteSSHKeyParams{ID: added.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SSHKeys.Delete(ctx, hyperbolic.DeleteSSHKeyParams{ID: added.ID}); !hyperbolic.IsNotFound(err) {
		t.Fatalf("second delete: err %v, want not found", err)
	}
	if got := s.SSHKeys(); len(got) != 1 {
		t.Fatalf("%d keys after delete, want 1", len(got))
	}
}

func TestRentalSSHKeys(t *testing.T) {
	s := NewServer(t)
	c := s.Client(t)
	ctx := context.Background()
	alice := s.AddSSHKey(otherKey, "alice")

	// Without named keys, a rental gets the newest saved key.
	byDefault, err := c.Rentals.Create(ctx, h100x1("default"))
	if err != nil {
		t.Fatal(err)
	}
	if ids := s.RentalSSHKeyIDs(byDefault.RentalID); len(ids) != 1 || ids[0] != alice.ID {
		t.Fatalf("default keys = %v, want [%d]", ids, alice.ID)
	}
	defaultKey := s.SSHKeys()[0].ID
	named := h100x1("named")
	named.SSHPublicKeyIds = &[]int{defaultKey}
	created, err := c.Rentals.Create(ctx, named)
	if err != nil {
		t.Fatal(err)
	}
	if ids := s.RentalSSHKeyIDs(created.RentalID); len(ids) != 1 || ids[0] != defaultKey {
		t.Fatalf("named keys = %v, want [%d]", ids, defaultKey)
	}
	unknown := h100x1("unknown")
	unknown.SSHPublicKeyIds = &[]int{999}
	if _, err := c.Rentals.Create(ctx, unknown); hyperbolic.StatusCode(err) != http.StatusBadRequest {
		t.Fatalf("create with an unknown key: err %v, want 400", err)
	}

	attach := hyperbolic.AttachSSHKeyParams{RentalID: created.RentalID, SSHPublicKeyID: alice.ID}
	for range 2 { // attaching twice is harmless
		if _, err := c.Rentals.AttachSSHKey(ctx, attach); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := c.SSHKeys.ListForRental(ctx, hyperbolic.ListRentalSSHKeysParams{RentalID: created.RentalID})
	if err != nil || len(keys) != 2 {
		t.Fatalf("rental keys = %+v, err %v; want 2", keys, err)
	}
	listed, err := c.SSHKeys.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range listed {
		if k.ID == alice.ID && k.LiveRentalCount != 2 {
			t.Errorf("alice's key is on %d live rentals, want 2", k.LiveRentalCount)
		}
	}
	if _, err := c.Rentals.DetachSSHKey(ctx, hyperbolic.DetachSSHKeyParams{RentalID: created.RentalID, SSHPublicKeyID: alice.ID}); err != nil {
		t.Fatal(err)
	}
	if ids := s.RentalSSHKeyIDs(created.RentalID); len(ids) != 1 {
		t.Fatalf("keys after detach = %v, want one", ids)
	}

	unknownKey := hyperbolic.AttachSSHKeyParams{RentalID: created.RentalID, SSHPublicKeyID: 999}
	if _, err := c.Rentals.AttachSSHKey(ctx, unknownKey); !hyperbolic.IsNotFound(err) {
		t.Fatalf("attach an unknown key: err %v, want not found", err)
	}

	// A key cannot be deleted while a live rental uses it, and the answer is
	// the live API's 409.
	deleteDefault := hyperbolic.DeleteSSHKeyParams{ID: defaultKey}
	if _, err := c.SSHKeys.Delete(ctx, deleteDefault); hyperbolic.StatusCode(err) != http.StatusConflict || !hyperbolic.HasCode(err, "CONFLICT") {
		t.Fatalf("delete a key in use: err %v, want 409 CONFLICT", err)
	}
	s.TerminateExternally(created.RentalID)
	if _, err := c.SSHKeys.Delete(ctx, deleteDefault); err != nil {
		t.Fatalf("delete a key whose rental ended: %v", err)
	}
	if _, err := c.Rentals.AttachSSHKey(ctx, attach); !hyperbolic.HasCode(err, hyperbolic.CodeRentalNotFound) {
		t.Fatalf("attach to an ended rental: err %v, want %s", err, hyperbolic.CodeRentalNotFound)
	}
	if _, err := c.SSHKeys.ListForRental(ctx, hyperbolic.ListRentalSSHKeysParams{RentalID: "rnt_none"}); !hyperbolic.HasCode(err, hyperbolic.CodeRentalNotFound) {
		t.Fatalf("keys of an unknown rental: err %v", err)
	}
}

func TestCreateWithoutAnySSHKey(t *testing.T) {
	s := NewServer(t)
	c := s.Client(t)
	ctx := context.Background()
	if _, err := c.SSHKeys.Delete(ctx, hyperbolic.DeleteSSHKeyParams{ID: s.SSHKeys()[0].ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Rentals.Create(ctx, h100x1("")); !hyperbolic.HasCode(err, hyperbolic.CodeNoPublicKey) {
		t.Fatalf("create with no saved key: err %v, want %s", err, hyperbolic.CodeNoPublicKey)
	}
}
