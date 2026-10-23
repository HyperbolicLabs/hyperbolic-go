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
	"encoding/json"
	"fmt"
	"slices"

	"github.com/getkin/kin-openapi/openapi3"
)

// enumConf names an enum the spec repeats inline. With Values, it matches
// inline enums on Property whose values are among them. Without Values, it
// matches every inline enum on Property, which must then share one value set.
type enumConf struct {
	Property string   `yaml:"property"`
	Values   []string `yaml:"values"`
}

// unionConf names a oneOf the spec repeats inline (by property) or that a
// method's response hoisted (by schema), and its variants. Discriminator,
// when set, is the constant string property that tells the variants apart.
type unionConf struct {
	Property      string   `yaml:"property"`
	Schema        string   `yaml:"schema"`
	Discriminator string   `yaml:"discriminator"`
	Variants      []string `yaml:"variants"`
}

// slot is a place in the spec that holds a schema: a property of an object
// or a query parameter's schema. set replaces what it holds.
type slot struct {
	name string
	ref  *openapi3.SchemaRef
	set  func(*openapi3.SchemaRef)
}

// slots lists every property and query parameter schema reachable from the
// components and the given operations, in a stable order.
func slots(spec *openapi3.T, ops []*openapi3.Operation) []slot {
	var out []slot
	seen := map[*openapi3.Schema]bool{}
	var walk func(r *openapi3.SchemaRef)
	walk = func(r *openapi3.SchemaRef) {
		if r == nil || r.Value == nil || seen[r.Value] {
			return
		}
		s := r.Value
		seen[s] = true
		for _, name := range sortedKeys(s.Properties) {
			p := s.Properties[name]
			out = append(out, slot{name: name, ref: p, set: func(n *openapi3.SchemaRef) { s.Properties[name] = n }})
			// A nullable property is anyOf [schema, null], and its schema
			// branch counts as the property too.
			if p.Value != nil && len(p.Value.AnyOf) == 2 && isNull(p.Value.AnyOf[1]) {
				pv := p.Value
				out = append(out, slot{name: name, ref: pv.AnyOf[0], set: func(n *openapi3.SchemaRef) { pv.AnyOf[0] = n }})
			}
			walk(p)
		}
		walk(s.Items)
		for _, list := range []openapi3.SchemaRefs{s.OneOf, s.AnyOf, s.AllOf} {
			for _, x := range list {
				walk(x)
			}
		}
	}
	for _, name := range sortedKeys(spec.Components.Schemas) {
		walk(spec.Components.Schemas[name])
	}
	for _, op := range ops {
		for _, p := range op.Parameters {
			if p.Value == nil || p.Value.In != "query" {
				continue
			}
			pv := p.Value
			out = append(out, slot{name: pv.Name, ref: pv.Schema, set: func(n *openapi3.SchemaRef) { pv.Schema = n }})
		}
		if op.RequestBody != nil && op.RequestBody.Value != nil {
			if mt := op.RequestBody.Value.Content.Get("application/json"); mt != nil {
				walk(mt.Schema)
			}
		}
		if r := op.Responses.Status(200); r != nil && r.Value != nil {
			if mt := r.Value.Content.Get("application/json"); mt != nil {
				walk(mt.Schema)
			}
		}
	}
	return out
}

// nameUnions hoists each configured union into a component, names its
// variants, and adds its discriminator.
func nameUnions(spec *openapi3.T, conf *sdkFile, ops []*openapi3.Operation) error {
	for _, name := range sortedKeys(conf.Unions) {
		u := conf.Unions[name]
		var union *openapi3.SchemaRef
		switch {
		case u.Schema != "":
			union = spec.Components.Schemas[u.Schema]
			if union == nil {
				return fmt.Errorf("union %s: no schema %s", name, u.Schema)
			}
		case u.Property != "":
			var shape string
			for _, sl := range slots(spec, ops) {
				if sl.name != u.Property || sl.ref.Ref != "" || sl.ref.Value == nil || len(sl.ref.Value.OneOf) == 0 {
					continue
				}
				b, _ := json.Marshal(sl.ref.Value)
				if union == nil {
					union, shape = hoist(spec, name, sl.ref), string(b)
				} else if string(b) != shape {
					return fmt.Errorf("union %s: property %s has more than one shape", name, u.Property)
				}
				sl.set(union)
			}
			if union == nil {
				return fmt.Errorf("union %s: no oneOf property %s", name, u.Property)
			}
		default:
			return fmt.Errorf("union %s needs property or schema", name)
		}
		s := union.Value
		if len(s.OneOf) != len(u.Variants) {
			return fmt.Errorf("union %s has %d variants, sdk.yaml names %d", name, len(s.OneOf), len(u.Variants))
		}
		d := &openapi3.Discriminator{PropertyName: u.Discriminator, Mapping: openapi3.StringMap[openapi3.MappingRef]{}}
		for i, vname := range u.Variants {
			v := s.OneOf[i]
			if v.Ref == "" {
				v = hoist(spec, vname, v)
				s.OneOf[i] = v
			}
			if u.Discriminator == "" {
				continue
			}
			tag := v.Value.Properties[u.Discriminator]
			if tag == nil || tag.Value == nil {
				return fmt.Errorf("union %s: variant %s has no %s", name, vname, u.Discriminator)
			}
			value, ok := tag.Value.Const.(string)
			if !ok && len(tag.Value.Enum) == 1 {
				value, ok = tag.Value.Enum[0].(string)
			}
			if !ok {
				return fmt.Errorf("union %s: variant %s's %s is not a constant string", name, vname, u.Discriminator)
			}
			d.Mapping[value] = openapi3.MappingRef{Ref: v.Ref}
		}
		if u.Discriminator != "" {
			s.Discriminator = d
		}
	}
	return nil
}

// nameEnums replaces each inline enum that matches a configured enum with a
// reference to one component, so a value set has one Go type.
func nameEnums(spec *openapi3.T, conf *sdkFile, ops []*openapi3.Operation) error {
	found := map[string][]string{}
	for _, sl := range slots(spec, ops) {
		s := sl.ref.Value
		if sl.ref.Ref != "" || s == nil || !s.Type.Is("string") || len(s.Enum) < 2 {
			continue
		}
		values := make([]string, 0, len(s.Enum))
		for _, v := range s.Enum {
			str, _ := v.(string)
			values = append(values, str)
		}
		slices.Sort(values)
		for _, name := range sortedKeys(conf.Enums) {
			e := conf.Enums[name]
			if e.Property != sl.name {
				continue
			}
			if len(e.Values) > 0 && !subset(values, e.Values) {
				continue
			}
			if prev, ok := found[name]; ok && len(e.Values) == 0 && !slices.Equal(prev, values) {
				return fmt.Errorf("enum %s: property %s has more than one value set, so list the values in sdk.yaml", name, e.Property)
			}
			if _, ok := found[name]; !ok {
				found[name] = values
				enum := s.Enum
				if len(e.Values) > 0 {
					enum = make([]any, len(e.Values))
					for i, v := range e.Values {
						enum[i] = v
					}
				}
				spec.Components.Schemas[name] = &openapi3.SchemaRef{Value: &openapi3.Schema{Type: s.Type, Enum: enum}}
			}
			sl.set(&openapi3.SchemaRef{Ref: "#/components/schemas/" + name, Value: spec.Components.Schemas[name].Value})
			break
		}
	}
	for _, name := range sortedKeys(conf.Enums) {
		if _, ok := found[name]; !ok {
			return fmt.Errorf("enum %s matches nothing in the spec", name)
		}
	}
	return nil
}

// subset reports whether every value of a is in b.
func subset(a, b []string) bool {
	for _, v := range a {
		if !slices.Contains(b, v) {
			return false
		}
	}
	return true
}
