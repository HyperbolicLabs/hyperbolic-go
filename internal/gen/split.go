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
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
)

// models is oapi-codegen's output split by owner, so each type can be
// written next to the service that uses it.
type models struct {
	// imports are the import lines the models need, ready for an import
	// block.
	imports []string
	// byService holds, per service, the declarations of the types only that
	// service uses. shared holds the rest.
	byService map[string][]string
	shared    []string
}

// splitModels assigns every declaration in src (a type, its enum constants,
// its methods) to the one service whose methods reach the type through their
// parameters, results or page items, or to shared when several services or
// none reach it.
func splitModels(src string, v *view) (*models, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "models.go", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse models: %w", err)
	}
	m := &models{byService: map[string][]string{}}
	for _, imp := range f.Imports {
		line := imp.Path.Value
		if imp.Name != nil {
			line = imp.Name.Name + " " + line
		}
		m.imports = append(m.imports, line)
	}

	// Each declaration, the type it belongs to, and what it is: the type
	// itself (0), its constants (1) or anything else, such as a method (2).
	type decl struct {
		owner string
		kind  int
		text  string
	}
	var decls []decl
	types := map[string]bool{}
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
			for _, s := range g.Specs {
				types[s.(*ast.TypeSpec).Name.Name] = true
			}
		}
	}
	refs := map[string]map[string]bool{} // type -> types its declarations mention
	for _, d := range f.Decls {
		owner := ownerOf(d)
		start := d.Pos()
		if doc := docOf(d); doc != nil {
			start = doc.Pos()
		}
		kind := 2
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
			kind = 0
		} else if ok && g.Tok == token.CONST {
			kind = 1
		}
		decls = append(decls, decl{owner: owner, kind: kind, text: src[fset.Position(start).Offset:fset.Position(d.End()).Offset]})
		if owner == "" {
			continue
		}
		if refs[owner] == nil {
			refs[owner] = map[string]bool{}
		}
		ast.Inspect(d, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && types[id.Name] && id.Name != owner {
				refs[owner][id.Name] = true
			}
			return true
		})
	}

	// Which services reach each type.
	reachedBy := map[string][]string{}
	for _, s := range v.Services {
		seen := map[string]bool{}
		var walk func(string)
		walk = func(t string) {
			if !types[t] || seen[t] {
				return
			}
			seen[t] = true
			for r := range refs[t] {
				walk(r)
			}
		}
		for _, mv := range s.Methods {
			for _, t := range []string{mv.Params, mv.Result, mv.Item} {
				walk(strings.TrimLeft(t, "[]*"))
			}
		}
		for t := range seen {
			reachedBy[t] = append(reachedBy[t], s.Name)
		}
	}

	// Group each type with its declarations, as hand-written Go reads: the
	// type, then its constants, then its methods. oapi-codegen emits all
	// constants and methods ahead of the types.
	at := map[string]int{}
	for i, d := range decls {
		if d.kind == 0 {
			at[d.owner] = i
		}
	}
	slices.SortStableFunc(decls, func(a, b decl) int {
		if a.owner != b.owner {
			return at[a.owner] - at[b.owner]
		}
		return a.kind - b.kind
	})

	for _, d := range decls {
		if services := reachedBy[d.owner]; d.owner != "" && len(services) == 1 {
			m.byService[services[0]] = append(m.byService[services[0]], d.text)
		} else {
			m.shared = append(m.shared, d.text)
		}
	}
	return m, nil
}

// ownerOf returns the type a declaration belongs to: the type it declares,
// the type of the enum constants it declares, or its receiver's type.
func ownerOf(d ast.Decl) string {
	switch d := d.(type) {
	case *ast.GenDecl:
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.TypeSpec:
				return s.Name.Name
			case *ast.ValueSpec:
				if id, ok := s.Type.(*ast.Ident); ok {
					return id.Name
				}
			}
		}
	case *ast.FuncDecl:
		if d.Recv != nil && len(d.Recv.List) == 1 {
			t := d.Recv.List[0].Type
			if star, ok := t.(*ast.StarExpr); ok {
				t = star.X
			}
			if id, ok := t.(*ast.Ident); ok {
				return id.Name
			}
		}
	}
	return ""
}

func docOf(d ast.Decl) *ast.CommentGroup {
	switch d := d.(type) {
	case *ast.GenDecl:
		return d.Doc
	case *ast.FuncDecl:
		return d.Doc
	}
	return nil
}

// goFile assembles a Go file of package pkg from an import list and parts of
// source. Formatting drops the imports it does not use.
func goFile(name, pkg string, imports []string, parts ...string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	if len(imports) > 0 {
		b.WriteString("import (\n")
		for _, imp := range dedupe(imports) {
			b.WriteString("\t" + imp + "\n")
		}
		b.WriteString(")\n")
	}
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			b.WriteString("\n" + p + "\n")
		}
	}
	return format(name, b.String())
}

func dedupe(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return slices.Compact(out)
}
