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
	"iter"

	"github.com/oapi-codegen/nullable"
)

// pages iterates over the items of page-numbered results. fetch returns a
// page's items and whether more pages follow. first is the page to start at,
// 1 when nil. Items added or removed while iterating can shift between
// pages, so one may be skipped or seen twice.
func pages[T any](first *int, fetch func(page int) ([]T, bool, error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		page := 1
		if first != nil {
			page = *first
		}
		for {
			items, more, err := fetch(page)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range items {
				if !yield(item, nil) {
					return
				}
			}
			if !more || len(items) == 0 {
				return
			}
			page++
		}
	}
}

// cursors iterates over the items of cursor-paginated results. fetch
// returns a page's items and the cursor of the next page, nil on the last.
// first is the cursor to start at, the first page when nil.
// Repeated cursors end the iteration with an error before fetching that page again.
func cursors[T any](first *string, fetch func(cursor *string) ([]T, *string, error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		cursor := first
		seen := make(map[string]struct{})
		for {
			if cursor != nil {
				if _, repeated := seen[*cursor]; repeated {
					var zero T
					yield(zero, errors.New("hyperbolic: repeated pagination cursor"))
					return
				}
				seen[*cursor] = struct{}{}
			}
			items, next, err := fetch(cursor)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range items {
				if !yield(item, nil) {
					return
				}
			}
			if next == nil || *next == "" {
				return
			}
			cursor = next
		}
	}
}

// nextCursor returns a page's next cursor, or nil on the last page.
func nextCursor(c nullable.Nullable[string]) *string {
	v, err := c.Get()
	if err != nil || v == "" {
		return nil
	}
	return &v
}

// Ptr returns a pointer to v, for setting optional request fields:
//
//	params.Label = hyperbolic.Ptr("training")
func Ptr[T any](v T) *T { return &v }
