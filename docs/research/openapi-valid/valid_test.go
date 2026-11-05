// Package openapivalid checks that the schemas internal/openapi generates are valid OpenAPI
// 3.1. It is its own module because it needs a third-party validator, and the core imports the
// standard library only (ADR 0006). It reads the golden documents of internal/openapi.
//
// Three checks per golden: the golden wrapped in a minimal OpenAPI 3.1 document validates
// against the official OAS 3.1 schema; every schema in it validates against the OAS 3.1
// dialect (JSON Schema 2020-12 plus the OAS vocabulary), which the document schema alone does
// not do because it leaves a Schema Object open (the three files in schemas/ were fetched from spec.openapis.org on 2026-10-31); and every $ref names a component that exists.
package openapivalid

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const goldens = "../../../internal/openapi/testdata/*.json"

func load(t *testing.T, path string) any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(path, err)
	}
	return v
}

func compiler(t *testing.T) *jsonschema.Compiler {
	t.Helper()
	c := jsonschema.NewCompiler()
	for url, file := range map[string]string{
		"https://spec.openapis.org/oas/3.1/schema/2022-10-07": "oas-3.1-schema-2022-10-07.json",
		"https://spec.openapis.org/oas/3.1/dialect/base":      "oas-3.1-dialect-base.json",
		"https://spec.openapis.org/oas/3.1/meta/base":         "oas-3.1-meta-base.json",
	} {
		if err := c.AddResource(url, load(t, filepath.Join("schemas", file))); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestTheGeneratedSchemasAreValidOpenAPI31(t *testing.T) {
	files, err := filepath.Glob(goldens)
	if err != nil || len(files) == 0 {
		t.Fatalf("no goldens found at %s: %v", goldens, err)
	}
	c := compiler(t)
	docSchema, err := c.Compile("https://spec.openapis.org/oas/3.1/schema/2022-10-07")
	if err != nil {
		t.Fatal(err)
	}
	dialect, err := c.Compile("https://spec.openapis.org/oas/3.1/dialect/base")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		t.Run(strings.TrimSuffix(filepath.Base(file), ".json"), func(t *testing.T) {
			golden := load(t, file).(map[string]any)
			root := golden["schema"]
			comps := golden["components"].(map[string]any)

			doc := map[string]any{
				"openapi": "3.1.0",
				"info":    map[string]any{"title": "golden", "version": "1"},
				"paths": map[string]any{"/x": map[string]any{"post": map[string]any{
					"requestBody": map[string]any{"content": map[string]any{
						"application/json": map[string]any{"schema": root}}},
					"responses": map[string]any{"200": map[string]any{"description": "ok"}},
				}}},
				"components": map[string]any{"schemas": comps},
			}
			if err := docSchema.Validate(doc); err != nil {
				t.Errorf("the document is not valid OpenAPI 3.1: %v", err)
			}
			all := map[string]any{"(root)": root}
			for n, s := range comps {
				all[n] = s
			}
			for n, s := range all {
				if err := dialect.Validate(s); err != nil {
					t.Errorf("schema %s is not valid in the OAS 3.1 dialect: %v", n, err)
				}
				for _, ref := range refs(s) {
					name, ok := strings.CutPrefix(ref, "#/components/schemas/")
					if _, found := comps[name]; !ok || !found {
						t.Errorf("schema %s refers to %q, which is not a component", n, ref)
					}
				}
			}
		})
	}
}

func refs(v any) (out []string) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if r, ok := e.(string); ok && k == "$ref" {
				out = append(out, r)
				continue
			}
			out = append(out, refs(e)...)
		}
	case []any:
		for _, e := range x {
			out = append(out, refs(e)...)
		}
	}
	return
}

func TestTheCheckRejectsAnInvalidSchema(t *testing.T) {
	dialect, err := compiler(t).Compile("https://spec.openapis.org/oas/3.1/dialect/base")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"type":"strng"}`, `{"type":"object","required":"a"}`, `{"minimum":"0"}`} {
		v, _ := jsonschema.UnmarshalJSON(strings.NewReader(bad))
		if dialect.Validate(v) == nil {
			t.Errorf("%s passed the dialect check", bad)
		}
	}
}
