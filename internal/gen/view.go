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
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oapi-codegen/oapi-codegen/v2/pkg/codegen"
)

// view is what the templates render.
type view struct {
	Services []serviceView
}

type serviceView struct {
	Name    string
	Doc     string
	Methods []methodView
}

type methodView struct {
	Service string
	Name    string
	// ID is the operation's Go id from sdk.yaml, which names its types and
	// the fake server's handler.
	ID          string
	Summary     string
	Description string
	Deprecated  string
	// HTTPMethod is the net/http constant, such as MethodGet, and Verb the
	// method itself.
	HTTPMethod string
	Verb       string
	Path       string
	// Params is the Go type of the method's parameters, or empty.
	Params string
	Body   bool
	Query  []queryParam
	// Result is the Go type the method returns, and Pointer whether it is
	// returned as a pointer. Structs are, slices and scalars are not.
	Result         string
	Pointer        bool
	Zero           string
	Idempotent     bool
	NullIsNotFound bool
	// Paginate is "page" or "cursor", and Item is the element type it iterates.
	Paginate string
	Item     string
}

type queryParam struct {
	Name    string
	GoName  string
	Pointer bool
}

func buildView(conf *sdkFile, spec *openapi3.T, ops []codegen.OperationDefinition) (*view, error) {
	byID := map[string]codegen.OperationDefinition{}
	for _, op := range ops {
		byID[op.SpecOperationId] = op
	}
	v := &view{}
	for _, sname := range sortedKeys(conf.Services) {
		s := conf.Services[sname]
		sv := serviceView{Name: sname, Doc: s.Doc}
		for _, mname := range sortedKeys(s.Methods) {
			m := s.Methods[mname]
			op, ok := byID[m.ID]
			if !ok {
				return nil, fmt.Errorf("%s.%s: oapi-codegen did not generate operation %s", sname, mname, m.ID)
			}
			mv, err := method(spec, sname, mname, m, op)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", sname, mname, err)
			}
			sv.Methods = append(sv.Methods, mv)
		}
		v.Services = append(v.Services, sv)
	}
	return v, nil
}

func method(spec *openapi3.T, service, name string, m *methodConf, op codegen.OperationDefinition) (methodView, error) {
	mv := methodView{
		Service:        service,
		Name:           name,
		ID:             op.OperationId,
		Summary:        op.Spec.Summary,
		Description:    op.Spec.Description,
		HTTPMethod:     "Method" + strings.ToUpper(op.Method[:1]) + strings.ToLower(op.Method[1:]),
		Verb:           strings.ToUpper(op.Method),
		Path:           op.Path,
		Idempotent:     op.Method != http.MethodPost,
		NullIsNotFound: m.NullIsNotFound,
		Paginate:       m.Paginate,
	}
	if m.Idempotent != nil {
		mv.Idempotent = *m.Idempotent
	}
	if len(op.PathParams)+len(op.HeaderParams)+len(op.CookieParams) > 0 {
		return mv, fmt.Errorf("only query and body parameters are supported")
	}
	if len(op.QueryParams) > 0 {
		mv.Params = op.OperationId + "Params"
		for _, p := range op.QueryParams {
			if t := p.Schema.GoType; strings.HasPrefix(t, "[]") || strings.HasPrefix(t, "map[") {
				return mv, fmt.Errorf("query parameter %s: %s is not supported", p.ParamName, t)
			}
			mv.Query = append(mv.Query, queryParam{Name: p.ParamName, GoName: p.GoName(), Pointer: p.HasOptionalPointer()})
		}
	}
	for _, b := range op.Bodies {
		if b.ContentType != "application/json" {
			continue
		}
		if mv.Params != "" {
			return mv, fmt.Errorf("both query and body parameters")
		}
		mv.Params = b.Schema.GoType
		mv.Body = true
	}
	for _, r := range op.Responses {
		if r.StatusCode != "200" {
			continue
		}
		for _, c := range r.Contents {
			if c.ContentType == "application/json" {
				mv.Result = c.Schema.GoType
			}
		}
	}
	if mv.Result == "" {
		return mv, fmt.Errorf("no application/json 200 response")
	}
	mv.Pointer = !strings.HasPrefix(mv.Result, "[]") && !isScalar(mv.Result)
	switch {
	case mv.Pointer, strings.HasPrefix(mv.Result, "[]"):
		mv.Zero = "nil"
	case mv.Result == "bool":
		mv.Zero = "false"
	case mv.Result == "string":
		mv.Zero = `""`
	default:
		mv.Zero = "0"
	}
	if op.Spec.Deprecated {
		mv.Deprecated = "the operation is deprecated"
	}
	if m.Paginate != "" {
		item, err := pageItem(spec, mv.Result)
		if err != nil {
			return mv, err
		}
		mv.Item = item
		want := map[string]string{"page": "page", "cursor": "cursor"}[m.Paginate]
		if !slices.ContainsFunc(mv.Query, func(q queryParam) bool { return q.Name == want && q.Pointer }) {
			return mv, fmt.Errorf("paginate %s needs an optional %q query parameter", m.Paginate, want)
		}
	}
	return mv, nil
}

// pageItem returns the Go type of a page response's items.
func pageItem(spec *openapi3.T, result string) (string, error) {
	for name, s := range spec.Components.Schemas {
		if codegen.SchemaNameToTypeName(name) != result || s.Value == nil {
			continue
		}
		items := s.Value.Properties["items"]
		if items == nil || items.Value == nil || items.Value.Items == nil || items.Value.Items.Ref == "" {
			return "", fmt.Errorf("page type %s needs an items array of a named schema", result)
		}
		ref := items.Value.Items.Ref
		return codegen.SchemaNameToTypeName(ref[strings.LastIndex(ref, "/")+1:]), nil
	}
	return "", fmt.Errorf("page type %s is not a component schema", result)
}

func isScalar(t string) bool {
	switch t {
	case "bool", "string", "int", "int32", "int64", "float32", "float64":
		return true
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
