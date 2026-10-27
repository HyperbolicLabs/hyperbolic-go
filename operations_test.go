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
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	hyperbolic "github.com/HyperbolicLabs/hyperbolic-go"
)

// generatedOperation is one entry of generatedOperations, which internal/gen
// writes from openapi/sdk.yaml.
type generatedOperation struct {
	name, verb, path, id string
	idempotent           bool
	call                 func(context.Context, *hyperbolic.Client) (any, error)
}

// TestEveryOperation sends each operation to a server that answers with each
// of the operation's sample responses, which internal/gen builds from the
// spec. The method must send the right request and decode the response
// without losing any of it.
func TestEveryOperation(t *testing.T) {
	responses := os.DirFS(filepath.Join("testdata", "responses"))
	for _, op := range generatedOperations {
		samples, err := fs.Glob(responses, op.id+".*.json")
		if err != nil || len(samples) == 0 {
			t.Errorf("%s: no sample responses (err %v)", op.name, err)
			continue
		}
		for _, file := range samples {
			t.Run(op.name+"/"+file, func(t *testing.T) {
				body, err := fs.ReadFile(responses, file)
				if err != nil {
					t.Fatal(err)
				}
				var got *http.Request
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					got = r
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(body)
				}))
				defer ts.Close()
				c, err := hyperbolic.NewClient("sk_test", hyperbolic.WithBaseURL(ts.URL), hyperbolic.WithRetryPolicy(hyperbolic.NoRetries()))
				if err != nil {
					t.Fatal(err)
				}

				out, err := op.call(context.Background(), c)
				if err != nil {
					t.Fatalf("call: %v", err)
				}
				if got.Method != op.verb || got.URL.Path != op.path {
					t.Errorf("sent %s %s, want %s %s", got.Method, got.URL.Path, op.verb, op.path)
				}
				if a := got.Header.Get("Authorization"); a != "Bearer sk_test" {
					t.Errorf("Authorization = %q", a)
				}

				var want, back any
				if err := json.Unmarshal(body, &want); err != nil {
					t.Fatal(err)
				}
				enc, err := json.Marshal(out)
				if err != nil {
					t.Fatalf("re-encode: %v", err)
				}
				if err := json.Unmarshal(enc, &back); err != nil {
					t.Fatal(err)
				}
				for _, d := range diff("$", want, back) {
					t.Error(d)
				}
			})
		}
	}
}

// TestEveryOperationRetries checks each operation's retry class from
// openapi/sdk.yaml: after a 503, idempotent calls try again, and the rest
// report an unknown outcome without retrying.
func TestEveryOperationRetries(t *testing.T) {
	for _, op := range generatedOperations {
		t.Run(op.name, func(t *testing.T) {
			var n atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer ts.Close()
			c, err := hyperbolic.NewClient("sk_test", hyperbolic.WithBaseURL(ts.URL),
				hyperbolic.WithRetryPolicy(hyperbolic.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = op.call(context.Background(), c)
			switch {
			case op.idempotent && n.Load() != 2:
				t.Errorf("sent %d times, want 2: the call is idempotent", n.Load())
			case !op.idempotent && (n.Load() != 1 || !hyperbolic.IsOutcomeUnknown(err)):
				t.Errorf("sent %d times, err %v; want 1 and an unknown outcome", n.Load(), err)
			}
		})
	}
}

// diff lists where got differs from want, by JSON path.
func diff(path string, want, got any) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: want an object, got %v", path, got)}
		}
		var out []string
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			gv, ok := g[k]
			if !ok {
				out = append(out, fmt.Sprintf("%s.%s: dropped", path, k))
				continue
			}
			out = append(out, diff(path+"."+k, w[k], gv)...)
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return []string{fmt.Sprintf("%s: want %d items, got %v", path, len(w), got)}
		}
		var out []string
		for i := range w {
			out = append(out, diff(fmt.Sprintf("%s[%d]", path, i), w[i], g[i])...)
		}
		return out
	}
	if !reflect.DeepEqual(want, got) {
		return []string{fmt.Sprintf("%s: want %v, got %v", path, want, got)}
	}
	return nil
}
