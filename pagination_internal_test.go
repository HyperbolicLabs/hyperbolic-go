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
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
)

func TestListHistoryAllCursorProgress(t *testing.T) {
	type page struct {
		cursor string
		next   *string
		id     string
	}
	for _, tt := range []struct {
		name    string
		first   *string
		pages   []page
		wantIDs []string
		wantErr bool
	}{
		{
			name:    "repeated cursor",
			pages:   []page{{next: Ptr("a"), id: "r1"}, {cursor: "a", next: Ptr("a"), id: "r2"}},
			wantIDs: []string{"r1", "r2"}, wantErr: true,
		},
		{
			name:    "cycle",
			pages:   []page{{next: Ptr("a"), id: "r1"}, {cursor: "a", next: Ptr("b"), id: "r2"}, {cursor: "b", next: Ptr("a"), id: "r3"}},
			wantIDs: []string{"r1", "r2", "r3"}, wantErr: true,
		},
		{
			name: "starting cursor repeated", first: Ptr("a"),
			pages:   []page{{cursor: "a", next: Ptr("a"), id: "r1"}},
			wantIDs: []string{"r1"}, wantErr: true,
		},
		{
			name:    "nil cursor terminates",
			pages:   []page{{next: Ptr("a"), id: "r1"}, {cursor: "a", id: "r2"}},
			wantIDs: []string{"r1", "r2"},
		},
		{
			name:    "empty cursor terminates",
			pages:   []page{{next: Ptr(""), id: "r1"}},
			wantIDs: []string{"r1"},
		},
		{
			name:    "empty page can advance",
			pages:   []page{{next: Ptr("a")}, {cursor: "a", id: "r1"}},
			wantIDs: []string{"r1"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i := int(calls.Add(1)) - 1
				if i >= len(tt.pages) {
					http.Error(w, "unexpected extra request", http.StatusBadRequest)
					return
				}
				p := tt.pages[i]
				if got := r.URL.Query().Get("cursor"); got != p.cursor {
					t.Errorf("request %d cursor = %q, want %q", i, got, p.cursor)
				}
				items := []map[string]string{}
				if p.id != "" {
					items = append(items, map[string]string{"rentalId": p.id})
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"items": items, "nextCursor": p.next}); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			client := newTestClient(t, server.URL)
			var ids []string
			var iterationErr error
			for item, err := range client.Rentals.ListHistoryAll(context.Background(), ListRentalHistoryParams{Cursor: tt.first}) {
				if err != nil {
					iterationErr = err
					break
				}
				ids = append(ids, item.RentalID)
			}
			if (iterationErr != nil) != tt.wantErr {
				t.Errorf("iterator error = %v, want error %v", iterationErr, tt.wantErr)
			}
			if got := int(calls.Load()); got != len(tt.pages) {
				t.Errorf("requests = %d, want %d; iterator error: %v", got, len(tt.pages), iterationErr)
			}
			if !slices.Equal(ids, tt.wantIDs) {
				t.Errorf("IDs = %v, want %v", ids, tt.wantIDs)
			}
		})
	}
}

func TestCursorsIteratorStateAndEarlyStop(t *testing.T) {
	calls := 0
	seq := cursors(Ptr("a"), func(*string) ([]int, *string, error) {
		calls++
		return []int{1}, nil, nil
	})
	for range 2 {
		for _, err := range seq {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls != 2 {
		t.Fatalf("requests = %d, want 2 for two independent iterations", calls)
	}

	calls = 0
	for _, err := range cursors(nil, func(*string) ([]int, *string, error) {
		calls++
		return []int{1, 2}, Ptr("a"), nil
	}) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if calls != 1 {
		t.Fatalf("requests after early stop = %d, want 1", calls)
	}
}

func TestCursorsPreservesFetchError(t *testing.T) {
	wantErr := errors.New("fetch failed")
	calls := 0
	yields := 0
	for _, err := range cursors(nil, func(*string) ([]int, *string, error) {
		calls++
		return nil, nil, wantErr
	}) {
		yields++
		if !errors.Is(err, wantErr) {
			t.Errorf("error = %v, want %v", err, wantErr)
		}
	}
	if yields != 1 {
		t.Fatalf("yields = %d, want one error", yields)
	}
	if calls != 1 {
		t.Fatalf("requests = %d, want 1", calls)
	}
}
