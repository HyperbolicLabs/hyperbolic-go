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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	hyperbolic "github.com/HyperbolicLabs/hyperbolic-go"
)

// recorder is a scripted API: each route answers with a fixed status and
// body, and every request is recorded for assertions. A route is
// "METHOD /path", or "METHOD /path?query" to answer one query only.
type recorder struct {
	mu       sync.Mutex
	routes   map[string]reply
	requests []recorded
}

type reply struct {
	status int
	body   string
}

type recorded struct {
	method, path, query, body string
}

func newRecorder(t *testing.T, routes map[string]reply) (*recorder, *hyperbolic.Client) {
	t.Helper()
	rec := &recorder{routes: routes}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.requests = append(rec.requests, recorded{r.Method, r.URL.Path, r.URL.RawQuery, string(body)})
		rep, ok := rec.routes[r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery]
		if !ok {
			rep, ok = rec.routes[r.Method+" "+r.URL.Path]
		}
		rec.mu.Unlock()
		if !ok {
			rep = reply{http.StatusNotFound, `{"code":"NOT_FOUND","message":"no route"}`}
		}
		w.WriteHeader(rep.status)
		_, _ = w.Write([]byte(rep.body))
	}))
	t.Cleanup(ts.Close)
	c, err := hyperbolic.NewClient("sk_test", hyperbolic.WithBaseURL(ts.URL),
		hyperbolic.WithRetryPolicy(hyperbolic.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	return rec, c
}

func (r *recorder) count(method, path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, q := range r.requests {
		if q.method == method && q.path == path {
			n++
		}
	}
	return n
}

func (r *recorder) lastBody(t *testing.T, method, path string) map[string]any {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.requests) - 1; i >= 0; i-- {
		if q := r.requests[i]; q.method == method && q.path == path {
			var m map[string]any
			if err := json.Unmarshal([]byte(q.body), &m); err != nil {
				t.Fatalf("request body %q: %v", q.body, err)
			}
			return m
		}
	}
	t.Fatalf("no %s %s request", method, path)
	return nil
}

const (
	pathOptions   = "/v2/on-demand/rental-options"
	pathRentals   = "/v2/on-demand/rentals"
	pathActive    = "/v2/on-demand/rentals/active"
	pathTerminate = "/v2/on-demand/rentals/terminate"
	pathReboot    = "/v2/on-demand/rentals/reboot-instance"
	pathBalance   = "/v2/customer/balance"
)

// rentalJSON is one rental as the active list returns it.
const rentalJSON = `{"rentalId":"rnt_a","label":"train-run-42","region":"us-central-1","gpuType":"h100","gpuCount":8,"status":"Running","rentalType":"virtual-machine","connectionType":"infiniband","currentTerm":{"termType":"on-demand","costPerHour":2200,"effectiveAt":"2026-10-21T06:54:58Z"},"sshEndpoints":[{"host":"69.19.140.158","port":22,"user":"ubuntu"}],"name":null}`

func TestOptionsDecodesEachMachineType(t *testing.T) {
	_, c := newRecorder(t, map[string]reply{
		"GET " + pathOptions: {200, `[{"machineType":"virtual-machine","enabled":true,"costPerHourCents":275,"region":"us-central-1","gpuCount":1,"gpuType":"h100","connectionType":"ethernet","totalAvailable":9},
		  {"machineType":"bare-metal","enabled":true,"costPerHourCents":2900,"region":"us-central-2","gpuCount":8,"gpuType":"h200","connectionType":"infiniband"}]`},
	})
	opts, err := c.Rentals.Options(context.Background())
	if err != nil || len(opts) != 2 {
		t.Fatalf("options = %d, err %v", len(opts), err)
	}
	vm, err := opts[0].AsVirtualMachineOption()
	if err != nil || vm.CostPerHourCents != 275 || vm.TotalAvailable == nil || *vm.TotalAvailable != 9 {
		t.Fatalf("vm option = %+v, err %v", vm, err)
	}
	if kind, err := opts[1].Discriminator(); err != nil || kind != "bare-metal" {
		t.Fatalf("second option is %q (err %v), want bare-metal", kind, err)
	}
}

func TestListActiveSendsQueryAndDecodesPage(t *testing.T) {
	rec, c := newRecorder(t, map[string]reply{"GET " + pathActive: {200, `{"items":[` + rentalJSON + `],"page":1,"pageSize":50,"total":1,"totalPages":1}`}})
	page, err := c.Rentals.ListActive(context.Background(), hyperbolic.ListActiveRentalsParams{
		Page: hyperbolic.Ptr(1), PageSize: hyperbolic.Ptr(50), RentalType: hyperbolic.Ptr(hyperbolic.RentalTypeVirtualMachine),
	})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("page = %+v, err %v", page, err)
	}
	if q := rec.requests[0].query; q != "page=1&pageSize=50&rentalType=virtual-machine" {
		t.Errorf("query = %q", q)
	}
	r := page.Items[0]
	if r.Status != hyperbolic.RentalStatusRunning || r.GPUType != hyperbolic.GPUTypeH100 || r.SSHEndpoints[0].Port != 22 {
		t.Errorf("rental = %+v", r)
	}
	if label, err := r.Label.Get(); err != nil || label != "train-run-42" {
		t.Errorf("label = %q, err %v", label, err)
	}
	if !r.Name.IsNull() {
		t.Error("an explicit null name must decode as null")
	}
}

func TestListActiveAllReadsEveryPage(t *testing.T) {
	page := func(n int, ids ...string) string {
		var items []string
		for _, id := range ids {
			items = append(items, `{"rentalId":"`+id+`","status":"Running"}`)
		}
		return `{"items":[` + strings.Join(items, ",") + `],"page":` + strconv.Itoa(n) + `,"pageSize":2,"total":4,"totalPages":2}`
	}
	rec, c := newRecorder(t, map[string]reply{
		"GET " + pathActive + "?page=1&pageSize=2": {200, page(1, "rnt_a", "rnt_b")},
		"GET " + pathActive + "?page=2&pageSize=2": {200, page(2, "rnt_c", "rnt_d")},
	})
	var ids []string
	for r, err := range c.Rentals.ListActiveAll(context.Background(), hyperbolic.ListActiveRentalsParams{PageSize: hyperbolic.Ptr(2)}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.RentalID)
	}
	if strings.Join(ids, ",") != "rnt_a,rnt_b,rnt_c,rnt_d" {
		t.Fatalf("listed %v", ids)
	}
	if n := rec.count(http.MethodGet, pathActive); n != 2 {
		t.Fatalf("read %d pages, want 2", n)
	}
}

func TestListActiveAllStopsEarlyAndOnErrors(t *testing.T) {
	rec, c := newRecorder(t, map[string]reply{
		"GET " + pathActive + "?page=1": {200, `{"items":[{"rentalId":"rnt_a"},{"rentalId":"rnt_b"}],"page":1,"pageSize":2,"total":9,"totalPages":5}`},
		"GET " + pathActive + "?page=2": {401, `{"code":"UNAUTHORIZED","message":"bad key"}`},
	})
	for _, err := range c.Rentals.ListActiveAll(context.Background(), hyperbolic.ListActiveRentalsParams{}) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if n := rec.count(http.MethodGet, pathActive); n != 1 {
		t.Fatalf("breaking out after the first item read %d pages, want 1", n)
	}
	var seen int
	var lastErr error
	for _, err := range c.Rentals.ListActiveAll(context.Background(), hyperbolic.ListActiveRentalsParams{}) {
		if err != nil {
			lastErr = err
			continue
		}
		seen++
	}
	if seen != 2 || !hyperbolic.IsUnauthorized(lastErr) {
		t.Fatalf("seen %d, err %v; want the first page, then the error", seen, lastErr)
	}
}

func TestListHistoryAllFollowsCursors(t *testing.T) {
	const history = "/v2/on-demand/rentals/history"
	rec, c := newRecorder(t, map[string]reply{
		"GET " + history + "?":              {200, `{"items":[{"rentalId":"rnt_1"}],"nextCursor":"c2","totalCount":2}`},
		"GET " + history + "?cursor=c2":     {200, `{"items":[{"rentalId":"rnt_2"}],"nextCursor":null,"totalCount":2}`},
		"GET " + history + "?cursor=broken": {500, `{}`},
	})
	var ids []string
	for r, err := range c.Rentals.ListHistoryAll(context.Background(), hyperbolic.ListRentalHistoryParams{}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.RentalID)
	}
	if !slices.Equal(ids, []string{"rnt_1", "rnt_2"}) || rec.count(http.MethodGet, history) != 2 {
		t.Fatalf("listed %v in %d requests", ids, rec.count(http.MethodGet, history))
	}
}

func TestGetNullIsNotFound(t *testing.T) {
	rec, c := newRecorder(t, map[string]reply{"GET /v2/on-demand/rental": {200, `null`}})
	_, err := c.Rentals.Get(context.Background(), hyperbolic.GetRentalParams{RentalID: "rnt_x"})
	if !errors.Is(err, hyperbolic.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if q := rec.requests[0].query; q != "rentalId=rnt_x" {
		t.Errorf("query = %q", q)
	}
}

func TestCreateSendsOnlyWhatIsSet(t *testing.T) {
	rec, c := newRecorder(t, map[string]reply{
		"POST " + pathRentals: {200, `{"rentalId":"rnt_e","label":"train-run-42","region":"us-central-1","gpuType":"h100","gpuCount":8,"rentalType":"virtual-machine"}`},
	})
	created, err := c.Rentals.Create(context.Background(), hyperbolic.CreateRentalParams{
		RentalType: hyperbolic.RentalTypeVirtualMachine, GPUType: hyperbolic.Ptr(hyperbolic.GPUTypeH100), GPUCount: 8,
		Region: hyperbolic.Ptr("us-central-1"), Label: hyperbolic.Ptr("train-run-42"), CustomPorts: &[]int{8080},
	})
	if err != nil || created.RentalID != "rnt_e" {
		t.Fatalf("created = %+v, err %v", created, err)
	}
	body := rec.lastBody(t, http.MethodPost, pathRentals)
	want := map[string]any{"rentalType": "virtual-machine", "gpuType": "h100", "gpuCount": float64(8), "region": "us-central-1", "label": "train-run-42", "customPorts": []any{float64(8080)}}
	if len(body) != len(want) {
		t.Fatalf("body = %v, want exactly %v", body, want)
	}
	for k, v := range want {
		if b, _ := json.Marshal(body[k]); string(b) != mustJSON(t, v) {
			t.Errorf("body[%s] = %v, want %v", k, body[k], v)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCreateReportsAPIErrors(t *testing.T) {
	_, c := newRecorder(t, map[string]reply{
		"POST " + pathRentals: {400, `{"code":"BAD_REQUEST","message":"Input validation failed","issues":[{"message":"gpuCount: Too small: expected number to be >=1"}]}`},
	})
	_, err := c.Rentals.Create(context.Background(), hyperbolic.CreateRentalParams{RentalType: hyperbolic.RentalTypeVirtualMachine})
	var apiErr *hyperbolic.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "BAD_REQUEST" || len(apiErr.Issues) != 1 {
		t.Fatalf("err = %v, want a BAD_REQUEST APIError with one issue", err)
	}
	if hyperbolic.IsOutcomeUnknown(err) {
		t.Fatal("a 400 is a definite failure")
	}
}

func TestTerminateIsRetriedButNotFoundIsDefinite(t *testing.T) {
	rec, c := newRecorder(t, map[string]reply{"POST " + pathTerminate: {503, `{}`}})
	if _, err := c.Rentals.Terminate(context.Background(), hyperbolic.TerminateRentalParams{RentalID: "rnt_a"}); hyperbolic.StatusCode(err) != 503 {
		t.Fatalf("err = %v, want the final 503", err)
	}
	if n := rec.count(http.MethodPost, pathTerminate); n != 3 {
		t.Fatalf("sent %d times, want 3 (terminate is safe to repeat)", n)
	}
	if got := rec.lastBody(t, http.MethodPost, pathTerminate); got["rentalId"] != "rnt_a" {
		t.Fatalf("body = %v", got)
	}

	rec, c = newRecorder(t, map[string]reply{"POST " + pathTerminate: {404, `{"code":"RENTAL_NOT_FOUND","message":"gone"}`}})
	if _, err := c.Rentals.Terminate(context.Background(), hyperbolic.TerminateRentalParams{RentalID: "rnt_a"}); !hyperbolic.IsNotFound(err) {
		t.Fatalf("err = %v, want not found", err)
	}
	if n := rec.count(http.MethodPost, pathTerminate); n != 1 {
		t.Fatalf("sent %d times, want 1", n)
	}
}

func TestRebootIsNotRetriedOnAmbiguousFailure(t *testing.T) {
	rec, c := newRecorder(t, map[string]reply{"POST " + pathReboot: {502, `{}`}})
	_, err := c.Rentals.RebootInstance(context.Background(), hyperbolic.RebootRentalInstanceParams{RentalID: "rnt_a", InstanceRef: "n0"})
	if !hyperbolic.IsOutcomeUnknown(err) {
		t.Fatalf("err = %v, want outcome unknown", err)
	}
	if n := rec.count(http.MethodPost, pathReboot); n != 1 {
		t.Fatalf("sent %d times, want 1", n)
	}
}

func TestBalancesGet(t *testing.T) {
	_, c := newRecorder(t, map[string]reply{"GET " + pathBalance: {200, `{"balanceCents":49992,"maxOverdraftCents":0}`}})
	b, err := c.Balances.Get(context.Background())
	if err != nil || b.BalanceCents != 49992 {
		t.Fatalf("balance = %+v, err %v", b, err)
	}
}

func TestBalancesGetUnauthorized(t *testing.T) {
	rec, c := newRecorder(t, map[string]reply{"GET " + pathBalance: {401, `{"code":"UNAUTHORIZED","message":"Authorization not provided"}`}})
	if _, err := c.Balances.Get(context.Background()); !hyperbolic.IsUnauthorized(err) {
		t.Fatalf("err = %v, want unauthorized", err)
	}
	if n := rec.count(http.MethodGet, pathBalance); n != 1 {
		t.Fatalf("sent %d times, want 1 (a bad key is not retried)", n)
	}
}
