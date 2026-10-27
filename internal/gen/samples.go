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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/getkin/kin-openapi/openapi3"
)

// writeSamples writes, for every mapped operation, sample 200 responses
// built from its schema into testdata/responses: every property set, one
// file per union branch, with nullable fields filled in and again as null.
// The generated operation tests decode each sample with the method that
// returns it and check that nothing was lost.
func writeSamples(root string, spec *openapi3.T, v *view) error {
	dir := filepath.Join(root, "testdata", "responses")
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	byID := map[string]*openapi3.Operation{}
	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			byID[op.OperationID] = op
		}
	}
	for _, s := range v.Services {
		for _, m := range s.Methods {
			op := byID[m.ID]
			if op == nil {
				return fmt.Errorf("samples: no operation %s", m.ID)
			}
			schema := op.Responses.Status(200).Value.Content.Get("application/json").Schema
			var seen [][]byte
			for branch := range 3 {
				for _, nulls := range []bool{false, true} {
					b, err := json.MarshalIndent(sample(schema, branch, nulls, 0), "", "  ")
					if err != nil {
						return err
					}
					if slices.ContainsFunc(seen, func(x []byte) bool { return bytes.Equal(x, b) }) {
						continue
					}
					seen = append(seen, b)
					name := fmt.Sprintf("%s.%d.json", m.ID, len(seen))
					if err := os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0o644); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// sample builds a JSON value for s. branch picks the union variant and enum
// value, and nulls makes nullable values null instead of filled in.
func sample(r *openapi3.SchemaRef, branch int, nulls bool, depth int) any {
	if r == nil || r.Value == nil || depth > 12 {
		return nil
	}
	s := r.Value
	for _, union := range []openapi3.SchemaRefs{s.OneOf, s.AnyOf} {
		if len(union) == 0 {
			continue
		}
		var options openapi3.SchemaRefs
		for _, o := range union {
			if !isNull(o) {
				options = append(options, o)
			}
		}
		if len(options) < len(union) && nulls {
			return nil
		}
		return sample(options[branch%len(options)], branch, nulls, depth+1)
	}
	if len(s.AllOf) > 0 {
		out := map[string]any{}
		for _, part := range s.AllOf {
			if m, ok := sample(part, branch, nulls, depth+1).(map[string]any); ok {
				for k, v := range m {
					out[k] = v
				}
			}
		}
		return out
	}
	if s.Const != nil {
		return s.Const
	}
	if len(s.Enum) > 0 {
		return s.Enum[branch%len(s.Enum)]
	}
	if s.Type.Includes("null") && nulls {
		return nil
	}
	switch {
	case s.Type.Includes("object") || len(s.Properties) > 0:
		out := map[string]any{}
		for _, name := range sortedKeys(s.Properties) {
			out[name] = sample(s.Properties[name], branch, nulls, depth+1)
		}
		return out
	case s.Type.Includes("array"):
		return []any{sample(s.Items, branch, nulls, depth+1)}
	case s.Type.Includes("string"):
		switch s.Format {
		case "date-time":
			return "2026-10-21T07:00:00Z"
		case "date":
			return "2026-10-21"
		case "email":
			return "dev@example.com"
		case "uri", "url":
			return "https://example.com/x"
		case "uuid":
			return "3f0b8c2e-8d7a-4c1e-9b5d-2a6f1e4c7d90"
		}
		return "sample"
	case s.Type.Includes("integer"):
		return 7
	case s.Type.Includes("number"):
		return 7.5
	case s.Type.Includes("boolean"):
		return true
	}
	return nil
}
