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

// Package hyperbolictest provides an in-memory fake of the Hyperbolic API for
// tests, with fault injection for the failures that matter most to callers:
// rate limiting, 5xx responses, timeouts, and failures that happen after the
// server has already applied a request.
//
// The fake's routes, parameters and payloads are generated from the same
// OpenAPI spec as the client, so they cannot drift from it. It models the
// rental lifecycle (rental options, create, get, list, reboot, terminate),
// saved SSH keys (save, list, rename, delete, and attach to or detach from a
// rental) and the balance. Other operations answer 501 Not Implemented. Its
// behavior follows what was observed against the live API. When the two
// disagree, the live API wins and this package should be fixed.
//
// A typical test injects a failure and checks what the caller does with it.
// Here a create fails after the server has already created the rental:
//
//	func TestCreateRetryIsSafe(t *testing.T) {
//		srv := hyperbolictest.NewServer(t)
//		client := srv.Client(t)
//		srv.InjectFault(hyperbolictest.Fault{
//			Method: http.MethodPost, Path: hyperbolictest.PathRentals,
//			Status: http.StatusBadGateway, AfterCommit: true,
//		})
//
//		_, err := client.Rentals.Create(ctx, params)
//		// err matches hyperbolic.ErrOutcomeUnknown, the create was sent
//		// once, and srv.ActiveRentals() holds exactly one rental.
//	}
package hyperbolictest

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oapi-codegen/nullable"

	hyperbolic "github.com/HyperbolicLabs/hyperbolic-go"
	"github.com/HyperbolicLabs/hyperbolic-go/hyperbolictest/internal/server"
)

// DefaultSSHPublicKey is the SSH key a new Server's account has saved, so a
// rental created without naming keys gets it, as on the live API.
const DefaultSSHPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEh5cGVyYm9saWN0ZXN0LWRlZmF1bHQta2V5LTAwMDA hyperbolictest"

// DefaultAPIKey is the key the fake server accepts unless changed.
const DefaultAPIKey = "sk_test_hyperbolictest" //nolint:gosec // a fixture for the in-memory fake, not a credential. gitleaks:allow

// Paths served by the fake, exported so tests can target faults precisely.
const (
	PathRentalOptions = "/v2/on-demand/rental-options"
	// PathRentals creates rentals (POST).
	PathRentals = "/v2/on-demand/rentals"
	// PathActiveRentals lists active rentals, a page at a time (GET).
	PathActiveRentals = "/v2/on-demand/rentals/active"
	PathTerminate     = "/v2/on-demand/rentals/terminate"
	PathReboot        = "/v2/on-demand/rentals/reboot-instance"
	PathBalance       = "/v2/customer/balance"
	// PathSSHKeys lists (GET) and saves (POST) SSH keys.
	PathSSHKeys      = "/v2/ssh-keys"
	PathSSHKeyDelete = "/v2/ssh-keys/delete"
	PathAttachSSHKey = "/v2/on-demand/rentals/ssh-keys/attach"
	PathDetachSSHKey = "/v2/on-demand/rentals/ssh-keys/detach"
)

// maxPageSize is the largest page the active-rental list serves.
const maxPageSize = 100

// Fault makes matching requests fail or misbehave.
type Fault struct {
	// Method and Path select the requests the fault applies to. Path is
	// matched without the query.
	Method string
	Path   string
	// Status is the HTTP status to respond with. Zero means respond normally
	// (useful with Delay alone).
	Status int
	// Code and Message fill the error body.
	Code    string
	Message string
	// RetryAfter sets the Retry-After header.
	RetryAfter time.Duration
	// Delay waits before responding, to simulate a slow server or a timeout.
	Delay time.Duration
	// AfterCommit applies the request first and then fails, which is how a
	// create can fail while the rental is actually created.
	AfterCommit bool
	// Times is how many matching requests the fault applies to. Zero means 1.
	Times int
}

// Server is a fake Hyperbolic API backed by memory.
type Server struct {
	// URL is the base URL of the fake API.
	URL string
	// APIKey is the key the fake accepts.
	APIKey string

	srv     *httptest.Server
	handler http.Handler
	mu      sync.Mutex
	options []hyperbolic.RentalOption
	rentals map[string]*hyperbolic.RentalWithStatus
	// order holds rental ids oldest first. The list serves newest first.
	order   []string
	balance hyperbolic.AccountBalance
	nextID  int64
	// keys holds the saved SSH keys, oldest first, and rentalKeys the ids
	// of the keys authorized on each rental.
	keys       []hyperbolic.SSHKey
	nextKeyID  int
	rentalKeys map[string][]int
	pageSize   int
	faults     []*Fault
	requests   map[string]int
}

// NewServer starts a fake API with a small default set of rental options and
// a $1,000 balance. It is closed automatically when the test ends.
func NewServer(tb testing.TB) *Server {
	tb.Helper()
	s := &Server{
		APIKey:   DefaultAPIKey,
		options:  DefaultRentalOptions(),
		rentals:  map[string]*hyperbolic.RentalWithStatus{},
		balance:  hyperbolic.AccountBalance{BalanceCents: 100_000},
		nextID:   1000,
		pageSize: maxPageSize,
		requests: map[string]int{},

		nextKeyID:  1,
		rentalKeys: map[string][]int{},
	}
	s.saveKeyLocked(DefaultSSHPublicKey, "default")
	// Routes the spec does not have answer like the live API's 404.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Not found")
	})
	s.handler = server.HandlerWithOptions(&api{s: s}, server.StdHTTPServerOptions{
		BaseRouter: mux,
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		},
	})
	s.srv = httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	s.URL = s.srv.URL
	tb.Cleanup(s.srv.Close)
	return s
}

// VirtualMachineOption returns a rental option for a virtual machine.
func VirtualMachineOption(o hyperbolic.VirtualMachineOption) hyperbolic.RentalOption {
	o.MachineType = hyperbolic.VirtualMachineOptionMachineTypeVirtualMachine
	var opt hyperbolic.RentalOption
	if err := opt.FromVirtualMachineOption(o); err != nil {
		panic(fmt.Sprintf("hyperbolictest: encode rental option: %v", err))
	}
	return opt
}

// DefaultRentalOptions is the set of rental options a new Server starts with.
func DefaultRentalOptions() []hyperbolic.RentalOption {
	return []hyperbolic.RentalOption{
		VirtualMachineOption(hyperbolic.VirtualMachineOption{Enabled: true, CostPerHourCents: 275, Region: "us-central-1", GPUType: hyperbolic.GPUTypeH100, GPUCount: 1, ConnectionType: hyperbolic.ConnectionTypeEthernet, TotalAvailable: hyperbolic.Ptr(9)}),
		VirtualMachineOption(hyperbolic.VirtualMachineOption{Enabled: true, CostPerHourCents: 2200, Region: "us-central-1", GPUType: hyperbolic.GPUTypeH100, GPUCount: 8, ConnectionType: hyperbolic.ConnectionTypeInfiniband, TotalAvailable: hyperbolic.Ptr(2)}),
		VirtualMachineOption(hyperbolic.VirtualMachineOption{Enabled: true, CostPerHourCents: 3192, Region: "ca-east-1", GPUType: hyperbolic.GPUTypeH200, GPUCount: 8, ConnectionType: hyperbolic.ConnectionTypeEthernet}),
	}
}

// Client returns a client pointed at the fake, with fast retries.
func (s *Server) Client(tb testing.TB, opts ...hyperbolic.Option) *hyperbolic.Client {
	tb.Helper()
	all := append([]hyperbolic.Option{
		hyperbolic.WithBaseURL(s.URL),
		hyperbolic.WithRetryPolicy(hyperbolic.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 50 * time.Millisecond}),
	}, opts...)
	c, err := hyperbolic.NewClient(s.APIKey, all...)
	if err != nil {
		tb.Fatalf("hyperbolictest: new client: %v", err)
	}
	return c
}

// SetRentalOptions replaces the rental options.
func (s *Server) SetRentalOptions(opts ...hyperbolic.RentalOption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.options = append([]hyperbolic.RentalOption(nil), opts...)
}

// SetBalance sets the account balance and overdraft allowance, in cents.
func (s *Server) SetBalance(balanceCents, maxOverdraftCents int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.balance = hyperbolic.AccountBalance{BalanceCents: float64(balanceCents), MaxOverdraftCents: float64(maxOverdraftCents)}
}

// SetMaxPageSize caps the page size the active-rental list serves, so tests
// can exercise pagination without creating a hundred rentals.
func (s *Server) SetMaxPageSize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pageSize = n
}

// InjectFault adds a fault. Faults are matched in the order they were added.
func (s *Server) InjectFault(f Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Times <= 0 {
		f.Times = 1
	}
	s.faults = append(s.faults, &f)
}

// Requests returns how many requests the fake received for method and path
// (without the query), including ones answered with a fault.
func (s *Server) Requests(method, path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[method+" "+path]
}

// ActiveRentals returns the rentals that are not terminated, newest first.
func (s *Server) ActiveRentals() []hyperbolic.RentalWithStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeLocked()
}

// SetRentalStatus moves a rental to status. Moving to Running fills in one
// instance per 8 GPUs, with SSH endpoints. It reports whether the rental
// exists.
func (s *Server) SetRentalStatus(rentalID string, status hyperbolic.RentalStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rentals[rentalID]
	if !ok {
		return false
	}
	r.Status = status
	if status == hyperbolic.RentalStatusRunning && (r.Instances == nil || len(*r.Instances) == 0) {
		var instances []hyperbolic.RentalInstance
		nodes := int(math.Ceil(float64(r.GPUCount) / 8))
		for n := range nodes {
			host := fmt.Sprintf("203.0.113.%d", 10+n)
			instances = append(instances, hyperbolic.RentalInstance{
				InstanceRef:    fmt.Sprintf("%s-node-%d", r.RentalID, n),
				SSH:            nullable.NewNullableWithValue("ubuntu@" + host + ":22"),
				InternalIP:     nullable.NewNullableWithValue(fmt.Sprintf("10.0.0.%d", 10+n)),
				SupportsReboot: true,
			})
			r.SSHEndpoints = append(r.SSHEndpoints, hyperbolic.SSHEndpoint{Host: host, Port: 22, User: "ubuntu"})
			r.NetworkInterfaces = append(r.NetworkInterfaces, hyperbolic.NetworkInterface{PublicIP: host, PrivateIP: fmt.Sprintf("10.0.0.%d", 10+n)})
		}
		r.Instances = &instances
	}
	if r.Instances != nil {
		for i := range *r.Instances {
			(*r.Instances)[i].Phase = string(status)
		}
	}
	return true
}

// AddSSHKey saves an SSH key on the account, as someone would in the
// dashboard, and returns it. Unlike the API, it does not reject a duplicate.
func (s *Server) AddSSHKey(publicKey, name string) hyperbolic.SSHKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveKeyLocked(publicKey, name)
}

// SSHKeys returns the account's saved SSH keys, oldest first.
func (s *Server) SSHKeys() []hyperbolic.SSHKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]hyperbolic.SSHKey(nil), s.keys...)
}

// RentalSSHKeyIDs returns the ids of the SSH keys authorized on a rental.
func (s *Server) RentalSSHKeyIDs(rentalID string) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.rentalKeys[rentalID]...)
}

func (s *Server) saveKeyLocked(publicKey, name string) hyperbolic.SSHKey {
	k := hyperbolic.SSHKey{
		ID:        s.nextKeyID,
		PublicKey: publicKey,
		UserID:    "user_hyperbolictest",
		CreatedBy: hyperbolic.SSHKeyCreator{Email: "hyperbolictest@example.com"},
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if name != "" {
		k.Name = nullable.NewNullableWithValue(name)
	}
	s.nextKeyID++
	s.keys = append(s.keys, k)
	return k
}

func (s *Server) keyLocked(id int) (hyperbolic.SSHKey, bool) {
	for _, k := range s.keys {
		if k.ID == id {
			return k, true
		}
	}
	return hyperbolic.SSHKey{}, false
}

// sameKey reports whether two public keys have the same type and body, which
// is how the API detects a duplicate. The comment after them does not count.
// liveRentalCountLocked counts the rentals that are not terminated and
// authorize the key.
func (s *Server) liveRentalCountLocked(keyID int) int {
	n := 0
	for rentalID, ids := range s.rentalKeys {
		if s.rentals[rentalID].Status != hyperbolic.RentalStatusTerminated && slices.Contains(ids, keyID) {
			n++
		}
	}
	return n
}

func sameKey(a, b string) bool {
	fa, fb := strings.Fields(a), strings.Fields(b)
	return len(fa) >= 2 && len(fb) >= 2 && fa[0] == fb[0] && fa[1] == fb[1]
}

// TerminateExternally ends a rental the way the platform does when the
// balance runs out or hardware fails: it disappears from the active list.
func (s *Server) TerminateExternally(rentalID string) bool {
	return s.SetRentalStatus(rentalID, hyperbolic.RentalStatusTerminated)
}

func (s *Server) activeLocked() []hyperbolic.RentalWithStatus {
	out := make([]hyperbolic.RentalWithStatus, 0, len(s.rentals))
	for i := len(s.order) - 1; i >= 0; i-- {
		r := s.rentals[s.order[i]]
		if r.Status == hyperbolic.RentalStatusTerminated {
			continue
		}
		c := *r
		// Copy what SetRentalStatus mutates, so callers (and the JSON
		// encoder, which runs outside the lock) never share it.
		if r.Instances != nil {
			instances := append([]hyperbolic.RentalInstance(nil), *r.Instances...)
			c.Instances = &instances
		}
		c.SSHEndpoints = append([]hyperbolic.SSHEndpoint(nil), r.SSHEndpoints...)
		c.NetworkInterfaces = append([]hyperbolic.NetworkInterface(nil), r.NetworkInterfaces...)
		out = append(out, c)
	}
	return out
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests[r.Method+" "+r.URL.Path]++
	f := s.takeFaultLocked(r.Method, r.URL.Path)
	s.mu.Unlock()

	if f == nil {
		s.handler.ServeHTTP(w, r)
		return
	}
	if f.AfterCommit {
		rec := httptest.NewRecorder()
		s.handler.ServeHTTP(rec, r)
		wait(f.Delay, r)
		if f.Status != 0 {
			writeFault(w, f)
			return
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return
	}
	wait(f.Delay, r)
	if f.Status != 0 {
		writeFault(w, f)
		return
	}
	s.handler.ServeHTTP(w, r)
}

func (s *Server) takeFaultLocked(method, path string) *Fault {
	for _, f := range s.faults {
		if f.Times > 0 && f.Method == method && f.Path == path {
			f.Times--
			return f
		}
	}
	return nil
}

func wait(d time.Duration, r *http.Request) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-r.Context().Done():
	}
}

// api implements the generated server.ServerInterface over a Server's
// state. It is separate from Server so the handlers stay out of the fake's
// public API.
type api struct {
	server.Unimplemented
	s *Server
}

func (a *api) authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+a.s.APIKey {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid API key")
		return false
	}
	return true
}

// ListRentalOptions needs no API key, as on the live API.
func (a *api) ListRentalOptions(w http.ResponseWriter, _ *http.Request) {
	a.s.mu.Lock()
	opts := append([]hyperbolic.RentalOption(nil), a.s.options...)
	a.s.mu.Unlock()
	writeJSON(w, http.StatusOK, opts)
}

func (a *api) GetBalance(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r) {
		return
	}
	a.s.mu.Lock()
	b := a.s.balance
	a.s.mu.Unlock()
	writeJSON(w, http.StatusOK, b)
}

// ListActiveRentals serves one page of active rentals, newest first, as the
// live API does.
func (a *api) ListActiveRentals(w http.ResponseWriter, r *http.Request, params hyperbolic.ListActiveRentalsParams) {
	if !a.authorized(w, r) {
		return
	}
	page, size := 1, 25
	if params.Page != nil {
		page = *params.Page
	}
	if params.PageSize != nil {
		size = *params.PageSize
	}
	if page < 1 || size < 1 || size > maxPageSize {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid page or pageSize")
		return
	}
	a.s.mu.Lock()
	size = min(size, a.s.pageSize)
	var items []hyperbolic.RentalWithStatus
	for _, rental := range a.s.activeLocked() {
		if params.RentalType == nil || *params.RentalType == rental.RentalType {
			items = append(items, rental)
		}
	}
	a.s.mu.Unlock()
	from, to := min((page-1)*size, len(items)), min(page*size, len(items))
	writeJSON(w, http.StatusOK, hyperbolic.ActiveRentalsPage{
		Items:      append([]hyperbolic.RentalWithStatus{}, items[from:to]...),
		Page:       page,
		PageSize:   size,
		Total:      len(items),
		TotalPages: max(1, (len(items)+size-1)/size),
	})
}

func (a *api) CreateRental(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r) {
		return
	}
	var req hyperbolic.CreateRentalParams
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	if req.RentalType != hyperbolic.RentalTypeVirtualMachine {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "hyperbolictest models virtual-machine rentals only")
		return
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()

	var opt *hyperbolic.VirtualMachineOption
	for _, o := range a.s.options {
		vm, err := o.AsVirtualMachineOption()
		if err != nil || vm.MachineType != hyperbolic.VirtualMachineOptionMachineTypeVirtualMachine || !vm.Enabled {
			continue
		}
		inStock := vm.TotalAvailable == nil || *vm.TotalAvailable > 0
		if inStock && req.GPUType != nil && vm.GPUType == *req.GPUType && vm.GPUCount == req.GPUCount &&
			(req.Region == nil || vm.Region == *req.Region) &&
			(req.NetworkType == nil || string(vm.ConnectionType) == string(*req.NetworkType)) {
			opt = &vm
			break
		}
	}
	if opt == nil {
		writeError(w, http.StatusBadRequest, hyperbolic.CodeFailedToCalculateCostPerHour, "no matching offering")
		return
	}
	if a.s.balance.BalanceCents+a.s.balance.MaxOverdraftCents < opt.CostPerHourCents {
		writeError(w, http.StatusBadRequest, hyperbolic.CodeInsufficientBalance, "balance does not cover one hour")
		return
	}
	// The keys to authorize: the ones named, or else the newest saved key.
	var keyIDs []int
	switch {
	case req.SSHPublicKeyIds != nil && len(*req.SSHPublicKeyIds) > 0:
		keyIDs = append(keyIDs, *req.SSHPublicKeyIds...)
	case req.SSHPublicKeyID != nil:
		keyIDs = []int{*req.SSHPublicKeyID}
	case len(a.s.keys) > 0:
		keyIDs = []int{a.s.keys[len(a.s.keys)-1].ID}
	default:
		writeError(w, http.StatusBadRequest, hyperbolic.CodeNoPublicKey, "the account has no SSH public key")
		return
	}
	for _, id := range keyIDs {
		if _, ok := a.s.keyLocked(id); !ok {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", fmt.Sprintf("no SSH key %d", id))
			return
		}
	}

	id := a.s.nextID
	a.s.nextID++
	now := time.Now().UTC().Truncate(time.Second)
	var term hyperbolic.RentalTerm
	if err := term.FromOnDemandTerm(hyperbolic.OnDemandTerm{TermType: hyperbolic.OnDemandTermTermTypeOnDemand, CostPerHour: opt.CostPerHourCents, EffectiveAt: now}); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", err.Error())
		return
	}
	rental := &hyperbolic.RentalWithStatus{
		RentalID:       "rnt_" + strconv.FormatInt(id, 36),
		Name:           nullable.NewNullableWithValue("fake-rental-" + strconv.FormatInt(id, 10)),
		RentalType:     hyperbolic.RentalTypeVirtualMachine,
		Region:         opt.Region,
		GPUType:        opt.GPUType,
		GPUCount:       req.GPUCount,
		GPUFormFactor:  opt.GPUFormFactor,
		Status:         hyperbolic.RentalStatusPending,
		ConnectionType: opt.ConnectionType,
		CurrentTerm:    term,
		CreatedAt:      now,
	}
	if req.Label != nil {
		rental.Label = nullable.NewNullableWithValue(*req.Label)
	}
	if req.CustomPorts != nil {
		rental.ExposedPorts = append([]int(nil), *req.CustomPorts...)
	}
	a.s.rentals[rental.RentalID] = rental
	a.s.order = append(a.s.order, rental.RentalID)
	a.s.rentalKeys[rental.RentalID] = keyIDs
	// The create response is a Rental: everything but the live status,
	// which listing reports.
	writeJSON(w, http.StatusOK, asRental(rental))
}

// GetRental looks a rental up by id, running or ended. The body is null when
// the account has no such rental, as on the live API.
func (a *api) GetRental(w http.ResponseWriter, r *http.Request, params hyperbolic.GetRentalParams) {
	if !a.authorized(w, r) {
		return
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	rental, ok := a.s.rentals[params.RentalID]
	if !ok {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	state := hyperbolic.ActiveRentalLookupStateActive
	if rental.Status == hyperbolic.RentalStatusTerminated {
		writeJSON(w, http.StatusOK, map[string]any{"state": hyperbolic.TerminatedRentalLookupStateTerminated, "rental": rental})
		return
	}
	writeJSON(w, http.StatusOK, hyperbolic.ActiveRentalLookup{State: state, Rental: *rental})
}

func (a *api) TerminateRental(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r) {
		return
	}
	var req hyperbolic.TerminateRentalParams
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	rental, ok := a.s.rentals[req.RentalID]
	if !ok || rental.Status == hyperbolic.RentalStatusTerminated {
		writeError(w, http.StatusNotFound, hyperbolic.CodeRentalNotFound, "rental not found")
		return
	}
	rental.Status = hyperbolic.RentalStatusTerminated
	rental.TerminatedAt = nullable.NewNullableWithValue(time.Now().UTC().Truncate(time.Second))
	writeJSON(w, http.StatusOK, hyperbolic.RentalTermination{Message: "Rental terminated", RentalID: rental.RentalID})
}

func (a *api) RebootRentalInstance(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r) {
		return
	}
	var req hyperbolic.RebootRentalInstanceParams
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	rental, ok := a.s.rentals[req.RentalID]
	if !ok || rental.Status == hyperbolic.RentalStatusTerminated {
		writeError(w, http.StatusNotFound, hyperbolic.CodeRentalNotFound, "rental not found")
		return
	}
	if rental.Status != hyperbolic.RentalStatusRunning {
		writeError(w, http.StatusBadRequest, "RENTAL_NOT_RUNNING", "rental is not running")
		return
	}
	if rental.Instances != nil {
		for _, inst := range *rental.Instances {
			if inst.InstanceRef == req.InstanceRef {
				writeJSON(w, http.StatusOK, hyperbolic.RentalInstanceReboot{Message: "Rebooting", RentalID: rental.RentalID, InstanceRef: inst.InstanceRef})
				return
			}
		}
	}
	writeError(w, http.StatusBadRequest, "INVALID_INSTANCE_REF", "no such instance")
}

// ListSSHKeys lists the saved SSH keys, each with the number of active
// rentals it is authorized on.
func (a *api) ListSSHKeys(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r) {
		return
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	out := make([]hyperbolic.SSHKeyWithLiveRentalCount, 0, len(a.s.keys))
	for _, k := range a.s.keys {
		out = append(out, hyperbolic.SSHKeyWithLiveRentalCount{
			ID: k.ID, PublicKey: k.PublicKey, Name: k.Name, UserID: k.UserID, OrgID: k.OrgID,
			CreatedBy: k.CreatedBy, CreatedAt: k.CreatedAt, LiveRentalCount: a.s.liveRentalCountLocked(k.ID),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) CreateSSHKey(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r) {
		return
	}
	var req hyperbolic.CreateSSHKeyParams
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.PublicKey) == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "publicKey is required")
		return
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	for _, k := range a.s.keys {
		if sameKey(k.PublicKey, req.PublicKey) {
			writeError(w, http.StatusBadRequest, hyperbolic.CodeSSHPublicKeyAlreadyExists, "an SSH public key with the same type and body already exists")
			return
		}
	}
	name := ""
	if req.Name != nil {
		name = *req.Name
	}
	writeJSON(w, http.StatusOK, a.s.saveKeyLocked(req.PublicKey, name))
}

func (a *api) UpdateSSHKey(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r) {
		return
	}
	var req hyperbolic.UpdateSSHKeyParams
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	for i := range a.s.keys {
		if a.s.keys[i].ID == req.ID {
			a.s.keys[i].Name = nullable.NewNullableWithValue(req.Name)
			writeJSON(w, http.StatusOK, a.s.keys[i])
			return
		}
	}
	writeKeyNotFound(w)
}

// DeleteSSHKey removes a saved key, and refuses while a live rental uses it
// with 409 CONFLICT and the live API's message. The spec documents
// SSH_PUBLIC_KEY_IN_USE instead.
func (a *api) DeleteSSHKey(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r) {
		return
	}
	var req hyperbolic.DeleteSSHKeyParams
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	i := slices.IndexFunc(a.s.keys, func(k hyperbolic.SSHKey) bool { return k.ID == req.ID })
	if i < 0 {
		writeKeyNotFound(w)
		return
	}
	deleted := a.s.keys[i]
	if n := a.s.liveRentalCountLocked(deleted.ID); n > 0 {
		writeError(w, http.StatusConflict, "CONFLICT",
			"SSH public key is still attached to a live instance; it can be deleted once they are terminated")
		return
	}
	a.s.keys = slices.Delete(a.s.keys, i, i+1)
	for rentalID, ids := range a.s.rentalKeys {
		a.s.rentalKeys[rentalID] = slices.DeleteFunc(ids, func(id int) bool { return id == deleted.ID })
	}
	writeJSON(w, http.StatusOK, hyperbolic.DeletedSSHKey{ID: deleted.ID})
}

func writeKeyNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, hyperbolic.CodeSSHPublicKeyNotFound, "SSH public key not found")
}

func (a *api) ListRentalSSHKeys(w http.ResponseWriter, r *http.Request, params hyperbolic.ListRentalSSHKeysParams) {
	if !a.authorized(w, r) {
		return
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	if _, ok := a.s.rentals[params.RentalID]; !ok {
		writeError(w, http.StatusNotFound, hyperbolic.CodeRentalNotFound, "rental not found")
		return
	}
	out := []hyperbolic.SSHKey{}
	for _, id := range a.s.rentalKeys[params.RentalID] {
		if k, ok := a.s.keyLocked(id); ok {
			out = append(out, k)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// AttachSSHKey authorizes a saved key on an active rental. The live API
// refuses on some rentals with 400 "SSH keys cannot be changed on this
// rental", and the spec doesn't say which. The fake always allows it, so
// test that answer with InjectFault.
func (a *api) AttachSSHKey(w http.ResponseWriter, r *http.Request) {
	a.changeRentalKeys(w, r, func(ids []int, id int) []int {
		if slices.Contains(ids, id) {
			return ids
		}
		return append(ids, id)
	})
}

func (a *api) DetachSSHKey(w http.ResponseWriter, r *http.Request) {
	a.changeRentalKeys(w, r, func(ids []int, id int) []int {
		return slices.DeleteFunc(ids, func(x int) bool { return x == id })
	})
}

// changeRentalKeys applies change to the keys authorized on an active rental.
func (a *api) changeRentalKeys(w http.ResponseWriter, r *http.Request, change func(ids []int, id int) []int) {
	if !a.authorized(w, r) {
		return
	}
	var req hyperbolic.AttachSSHKeyParams
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	rental, ok := a.s.rentals[req.RentalID]
	if !ok || rental.Status == hyperbolic.RentalStatusTerminated {
		writeError(w, http.StatusNotFound, hyperbolic.CodeRentalNotFound, "rental not found")
		return
	}
	if _, ok := a.s.keyLocked(req.SSHPublicKeyID); !ok {
		writeKeyNotFound(w)
		return
	}
	a.s.rentalKeys[req.RentalID] = change(a.s.rentalKeys[req.RentalID], req.SSHPublicKeyID)
	writeJSON(w, http.StatusOK, hyperbolic.SuccessResponse{Success: true})
}

// asRental converts a listed rental to the create response's shape, which
// has the same fields less the live status and instances.
func asRental(r *hyperbolic.RentalWithStatus) hyperbolic.Rental {
	b, err := json.Marshal(r)
	if err != nil {
		panic(fmt.Sprintf("hyperbolictest: encode rental: %v", err))
	}
	var out hyperbolic.Rental
	if err := json.Unmarshal(b, &out); err != nil {
		panic(fmt.Sprintf("hyperbolictest: decode rental: %v", err))
	}
	return out
}

func writeFault(w http.ResponseWriter, f *Fault) {
	if f.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(f.RetryAfter.Seconds()))))
	}
	code := f.Code
	if code == "" {
		code = strings.ToUpper(strings.ReplaceAll(http.StatusText(f.Status), " ", "_"))
	}
	writeError(w, f.Status, code, f.Message)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"code": code, "message": message, "issues": []any{}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
