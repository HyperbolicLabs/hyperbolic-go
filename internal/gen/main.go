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

// Command gen generates the hyperbolic package from the Hyperbolic API's
// OpenAPI spec: its models and services, and the server interface the
// hyperbolictest fake implements.
//
// The spec (openapi/openapi.json) with openapi/overlay.yaml applied is the
// wire format, and openapi/sdk.yaml names each operation for Go. Models come
// from oapi-codegen. Services and the test server's stubs come from this
// module's templates. Run it with `make generate` from the repository root.
//
// gen lives in its own module so that oapi-codegen is never a dependency of
// the SDK itself.
package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oapi-codegen/oapi-codegen/v2/pkg/codegen"
	"github.com/oapi-codegen/oapi-codegen/v2/pkg/util"
	"golang.org/x/tools/imports"
	"gopkg.in/yaml.v3"
)

//go:embed templates/*.tmpl
var templates embed.FS

const modulePath = "github.com/HyperbolicLabs/hyperbolic-go"

// initialisms beyond oapi-codegen's defaults, so gpuType becomes GPUType.
var initialisms = []string{"GPU", "CPU", "SSH", "RAM", "VM", "CUDA", "SXM", "PCIE", "NVLINK"}

func main() {
	root := flag.String("root", ".", "repository root")
	sync := flag.String("sync", "", "first replace openapi/openapi.json with the spec at this URL or path")
	flag.Parse()
	if *sync != "" {
		if err := syncSpec(*root, *sync); err != nil {
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
	}
	if err := run(*root); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(root string) error {
	conf, err := loadSDK(filepath.Join(root, "openapi", "sdk.yaml"))
	if err != nil {
		return err
	}

	// Models and services, in package hyperbolic.
	spec, err := loadSpec(root, conf)
	if err != nil {
		return err
	}
	modelSrc, err := codegen.Generate(spec, codegen.Configuration{
		PackageName:   "hyperbolic",
		Generate:      codegen.GenerateOptions{Models: true},
		Compatibility: compatibility,
		OutputOptions: outputOptions(conf, spec, codegen.OutputOptions{}),
	})
	if err != nil {
		return fmt.Errorf("generate models: %w", err)
	}
	modelSrc = requestBodyAlias.ReplaceAllString(modelSrc, "")
	// Generate pruned the spec to the mapped operations, so ops holds only those.
	ops, err := codegen.OperationDefinitions(spec)
	if err != nil {
		return err
	}
	view, err := buildView(conf, spec, ops)
	if err != nil {
		return err
	}
	// Each service's file holds the service and the types only it uses.
	// shared_gen.go holds the types several services use.
	models, err := splitModels(modelSrc, view)
	if err != nil {
		return err
	}
	files := map[string]string{}
	body, err := render("client.tmpl", view)
	if err != nil {
		return err
	}
	if files["client_gen.go"], err = goFile("client_gen.go", "hyperbolic", nil, body); err != nil {
		return err
	}
	for _, s := range view.Services {
		body, err := render("service.tmpl", s)
		if err != nil {
			return err
		}
		name := snake(s.Name) + "_gen.go"
		if files[name], err = goFile(name, "hyperbolic", append(slices.Clone(models.imports), serviceImports...),
			body, strings.Join(models.byService[s.Name], "\n\n")); err != nil {
			return err
		}
	}
	if files["shared_gen.go"], err = goFile("shared_gen.go", "hyperbolic", models.imports, strings.Join(models.shared, "\n\n")); err != nil {
		return err
	}
	body, err = render("operations_test.tmpl", view)
	if err != nil {
		return err
	}
	operationTests, err := format("operations_gen_test.go", body)
	if err != nil {
		return err
	}
	if err := writeSamples(root, spec, view); err != nil {
		return err
	}

	// The fake's routes and parameter handling, in package server under
	// hyperbolictest/internal, so they stay out of the fake's public API.
	spec, err = loadSpec(root, conf)
	if err != nil {
		return err
	}
	server, err := codegen.Generate(spec, codegen.Configuration{
		PackageName:   "server",
		Generate:      codegen.GenerateOptions{StdHTTPServer: true},
		Compatibility: compatibility,
		OutputOptions: outputOptions(conf, spec, codegen.OutputOptions{}),
	})
	if err != nil {
		return fmt.Errorf("generate server: %w", err)
	}
	if server, err = qualify(server, view); err != nil {
		return err
	}
	body, err = render("unimplemented.tmpl", view)
	if err != nil {
		return err
	}
	stubs, err := format("unimplemented_gen.go", body)
	if err != nil {
		return err
	}

	files["operations_gen_test.go"] = operationTests
	files["hyperbolictest/internal/server/server_gen.go"] = server
	files["hyperbolictest/internal/server/unimplemented_gen.go"] = stubs

	boilerplate, err := os.ReadFile(filepath.Join(root, "hack", "boilerplate.go.txt"))
	if err != nil {
		return err
	}
	if err := removeGenerated(root); err != nil {
		return err
	}
	for name, src := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(header(boilerplate, src)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// serviceImports are what the service template uses.
var serviceImports = []string{`"context"`, `"encoding/json"`, `"fmt"`, `"iter"`, `"net/http"`, `"net/url"`}

// removeGenerated deletes every _gen.go and _gen_test.go file of the SDK
// module, so a renamed or moved file leaves nothing stale behind. The
// generator and tool modules have none of their own. It works inside an
// os.Root, so a symlink cannot lead it outside the repository.
func removeGenerated(root string) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return fs.WalkDir(r.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch path {
			case ".git", "bin", "internal/gen", "internal/tools":
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_gen.go") || strings.HasSuffix(path, "_gen_test.go") {
			return r.Remove(path)
		}
		return nil
	})
}

// snake turns a service name into its file name: SSHKeys becomes ssh_keys.
func snake(name string) string {
	s := regexp.MustCompile(`([a-z0-9])([A-Z])`).ReplaceAllString(name, "${1}_${2}")
	s = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`).ReplaceAllString(s, "${1}_${2}")
	return strings.ToLower(s)
}

// requestBodyAlias matches the <Op>JSONRequestBody aliases oapi-codegen adds
// for its own client. The services take <id>Params directly, so the aliases
// would only add a second name for every request type.
var requestBodyAlias = regexp.MustCompile(`(?m)^// \w+JSONRequestBody defines body for \w+ for application/json ContentType\.\ntype \w+JSONRequestBody = \w+\n\n?`)

// qualify prefixes the SDK types the generated server names, its query
// parameter types, with the package name: oapi-codegen writes them as if
// models and server shared a package.
func qualify(src string, v *view) (string, error) {
	for _, s := range v.Services {
		for _, m := range s.Methods {
			if len(m.Query) == 0 {
				continue
			}
			re := regexp.MustCompile(`([^.\w])` + m.Params + `\b`)
			src = re.ReplaceAllString(src, "${1}hyperbolic."+m.Params)
		}
	}
	// oapi-codegen drops the SDK import as unused before the names above
	// are qualified, so add it back.
	src = strings.Replace(src, "import (\n", "import (\n\thyperbolic \""+modulePath+"\"\n", 1)
	out, err := imports.Process("server_gen.go", []byte(src), nil)
	if err != nil {
		return "", fmt.Errorf("format server: %w", err)
	}
	return string(out), nil
}

// syncSpec replaces openapi/openapi.json with the spec at src, indented two
// spaces with its keys in their published order, so a change to the spec
// shows as a readable diff.
func syncSpec(root, src string) error {
	var raw []byte
	var err error
	if strings.HasPrefix(src, "https://") {
		raw, err = fetch(src)
	} else {
		raw, err = os.ReadFile(src)
	}
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return fmt.Errorf("%s is not JSON: %w", src, err)
	}
	buf.WriteByte('\n')
	return os.WriteFile(filepath.Join(root, "openapi", "openapi.json"), buf.Bytes(), 0o644)
}

func fetch(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// header gives a generated file the repository's license header and a
// banner naming the command that writes it, in place of whatever comments
// preceded its package clause.
func header(boilerplate []byte, src string) string {
	const banner = "// Code generated by internal/gen from openapi/openapi.json and openapi/sdk.yaml. DO NOT EDIT.\n"
	if i := strings.Index(src, "\npackage "); i >= 0 {
		src = src[i:]
	} else {
		src = "\n" + src
	}
	return strings.TrimRight(string(boilerplate), "\n") + "\n\n" + banner + src
}

// compatibility prefixes every enum constant with its type (GPUTypeH100,
// not H100), so a constant names what it is.
var compatibility = codegen.CompatibilityOptions{AlwaysPrefixEnumValues: true}

func outputOptions(conf *sdkFile, spec *openapi3.T, o codegen.OutputOptions) codegen.OutputOptions {
	o.IncludeOperationIDs = conf.ids()
	// The error envelopes are modeled by the hand-written APIError.
	for name := range spec.Components.Schemas {
		if strings.HasPrefix(name, "error.") {
			o.ExcludeSchemas = append(o.ExcludeSchemas, name)
		}
	}
	slices.Sort(o.ExcludeSchemas)
	o.NameNormalizer = "ToCamelCaseWithInitialisms"
	o.AdditionalInitialisms = initialisms
	o.NullableType = true
	// JSON numbers decode to float64, as encoding/json does. oapi-codegen's
	// default, float32, cannot hold every cent of a large balance.
	o.TypeMapping = &codegen.TypeMapping{Number: codegen.FormatMapping{Default: codegen.SimpleTypeSpec{Type: "float64"}}}
	return o
}

// sdkFile is openapi/sdk.yaml.
type sdkFile struct {
	Services map[string]*serviceConf `yaml:"services"`
	Enums    map[string]*enumConf    `yaml:"enums"`
	Unions   map[string]*unionConf   `yaml:"unions"`
}

type serviceConf struct {
	Doc     string                 `yaml:"doc"`
	Methods map[string]*methodConf `yaml:"methods"`
}

type methodConf struct {
	Operation      string `yaml:"operation"`
	ID             string `yaml:"id"`
	Response       string `yaml:"response"`
	Idempotent     *bool  `yaml:"idempotent"`
	Paginate       string `yaml:"paginate"`
	NullIsNotFound bool   `yaml:"nullIsNotFound"`
}

func loadSDK(path string) (*sdkFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f sdkFile
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]string{}
	for sname, s := range f.Services {
		for mname, m := range s.Methods {
			where := sname + "." + mname
			if m.Operation == "" || m.ID == "" {
				return nil, fmt.Errorf("%s: %s needs operation and id", path, where)
			}
			if prev, ok := seen[m.Operation]; ok {
				return nil, fmt.Errorf("%s: %s and %s both map %s", path, prev, where, m.Operation)
			}
			seen[m.Operation] = where
			if m.Paginate != "" && m.Paginate != "page" && m.Paginate != "cursor" {
				return nil, fmt.Errorf("%s: %s: paginate is page or cursor, not %q", path, where, m.Paginate)
			}
		}
	}
	return &f, nil
}

// ids returns the Go ids of every mapped operation, which loadSpec makes
// their operationIds.
func (f *sdkFile) ids() []string {
	var ids []string
	for _, s := range f.Services {
		for _, m := range s.Methods {
			ids = append(ids, m.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// loadSpec loads the spec with the overlay applied, checks it against
// sdk.yaml, and renames it for Go: each mapped operation's operationId
// becomes its id, and inline request and response schemas move into named
// components, so oapi-codegen names their types after the method.
func loadSpec(root string, conf *sdkFile) (*openapi3.T, error) {
	spec, err := util.LoadSwaggerWithOverlay(filepath.Join(root, "openapi", "openapi.json"), util.LoadSwaggerWithOverlayOpts{
		Path:   filepath.Join(root, "openapi", "overlay.yaml"),
		Strict: true,
	})
	if err != nil {
		return nil, err
	}
	byID := map[string]*openapi3.Operation{}
	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			byID[op.OperationID] = op
		}
	}
	mapped := map[string]bool{}
	var mappedOps []*openapi3.Operation
	var problems []string
	for sname, s := range conf.Services {
		for mname, m := range s.Methods {
			op, ok := byID[m.Operation]
			switch {
			case !ok:
				problems = append(problems, fmt.Sprintf("%s.%s: operation %s is not in the spec", sname, mname, m.Operation))
				continue
			case op.Deprecated:
				problems = append(problems, fmt.Sprintf("%s.%s: operation %s is deprecated", sname, mname, m.Operation))
				continue
			}
			mapped[m.Operation] = true
			mappedOps = append(mappedOps, op)
			if err := nameSchemas(spec, op, m); err != nil {
				return nil, fmt.Errorf("%s.%s: %w", sname, mname, err)
			}
			op.OperationID = m.ID
		}
	}
	for id, op := range byID {
		if !mapped[id] && !op.Deprecated && id != "openApiJson" {
			problems = append(problems, fmt.Sprintf("operation %s is in the spec but not in sdk.yaml", id))
		}
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, fmt.Errorf("openapi/sdk.yaml does not match the spec:\n  %s", strings.Join(problems, "\n  "))
	}
	if err := nameUnions(spec, conf, mappedOps); err != nil {
		return nil, err
	}
	if err := nameEnums(spec, conf, mappedOps); err != nil {
		return nil, err
	}
	return spec, nil
}

func nameSchemas(spec *openapi3.T, op *openapi3.Operation, m *methodConf) error {
	if op.RequestBody != nil && op.RequestBody.Value != nil {
		if mt := op.RequestBody.Value.Content.Get("application/json"); mt != nil && mt.Schema != nil && mt.Schema.Ref == "" {
			mt.Schema = hoist(spec, m.ID+"Params", mt.Schema)
		}
	}
	resp := op.Responses.Status(200)
	if resp == nil || resp.Value == nil {
		return fmt.Errorf("operation %s has no 200 response", m.Operation)
	}
	mt := resp.Value.Content.Get("application/json")
	if mt == nil || mt.Schema == nil {
		return nil
	}
	if m.NullIsNotFound {
		s := mt.Schema.Value
		if s == nil || len(s.AnyOf) != 2 || !isNull(s.AnyOf[1]) {
			return fmt.Errorf("nullIsNotFound needs a response of anyOf [schema, null]")
		}
		mt.Schema = s.AnyOf[0]
	}
	name := m.Response
	if name == "" {
		name = m.ID + "Response"
	}
	s := mt.Schema
	switch {
	case s.Ref != "":
	case s.Value.Type.Is("array"):
		if s.Value.Items != nil && s.Value.Items.Ref == "" && isStructured(s.Value.Items.Value) {
			s.Value.Items = hoist(spec, name, s.Value.Items)
		}
	case isStructured(s.Value):
		mt.Schema = hoist(spec, name, s)
	}
	return nil
}

// hoist moves an inline schema into components under name and returns a
// reference to it.
func hoist(spec *openapi3.T, name string, s *openapi3.SchemaRef) *openapi3.SchemaRef {
	spec.Components.Schemas[name] = &openapi3.SchemaRef{Value: s.Value}
	return &openapi3.SchemaRef{Ref: "#/components/schemas/" + name, Value: s.Value}
}

func isNull(s *openapi3.SchemaRef) bool {
	return s != nil && s.Value != nil && s.Value.Type.Is("null")
}

// isStructured reports whether a schema needs a named Go type: an object or
// a union.
func isStructured(s *openapi3.Schema) bool {
	return s != nil && (s.Type.Is("object") || len(s.Properties) > 0 || len(s.OneOf) > 0 || len(s.AnyOf) > 0)
}

func render(name string, data any) (string, error) {
	t, err := template.New(name).Funcs(template.FuncMap{
		"comment":  comment,
		"quote":    func(s string) string { return fmt.Sprintf("%q", s) },
		"sentence": sentence,
	}).ParseFS(templates, "templates/"+name)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return buf.String(), nil
}

// format formats Go source and drops the imports it does not use.
func format(name, src string) (string, error) {
	out, err := imports.Process(name, []byte(src), nil)
	if err != nil {
		return "", fmt.Errorf("%s: format: %w\n%s", name, err, src)
	}
	return string(out), nil
}

// sentence lowercases a summary's first letter and ends it with a period,
// so "Create a rental" reads "create a rental." after a colon.
func sentence(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), ".")
	if s == "" {
		return "."
	}
	if len(s) > 1 && (s[1] < 'A' || s[1] > 'Z') {
		s = strings.ToLower(s[:1]) + s[1:]
	}
	return s + "."
}

// comment formats text as Go comment lines wrapped at 80 columns, without a
// final newline. Each line of text is wrapped on its own, so lists keep their
// shape.
func comment(indent, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	width := 80 - len(strings.ReplaceAll(indent, "\t", "    ")) - len("// ")
	var out []string
	for _, line := range strings.Split(text, "\n") {
		words := strings.Fields(line)
		if len(words) == 0 {
			out = append(out, indent+"//")
			continue
		}
		cur := words[0]
		for _, w := range words[1:] {
			if len(cur)+1+len(w) > width {
				out = append(out, indent+"// "+cur)
				cur = w
				continue
			}
			cur += " " + w
		}
		out = append(out, indent+"// "+cur)
	}
	return strings.Join(out, "\n")
}
