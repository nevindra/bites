package openapivalid

import (
	"github.com/santhosh-tekuri/jsonschema/v6"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The whole documents the root package generates for a representative app (step 14, ADR 0020),
// kept as goldens in testdata/openapi at the repository root. Three checks per document: it
// validates against the official OAS 3.1 document schema; every Schema Object in it (the
// component schemas, the schemas of parameters, bodies and responses, and the problem+json
// schemas under components.responses) validates against the OAS 3.1 dialect; and every $ref
// resolves to something in the document.
const documents = "../../../testdata/openapi/*.json"

func TestTheGeneratedDocumentsAreValidOpenAPI31(t *testing.T) {
	files, err := filepath.Glob(documents)
	if err != nil || len(files) == 0 {
		t.Fatalf("no documents found at %s: %v", documents, err)
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
			doc := load(t, file)
			if err := docSchema.Validate(doc); err != nil {
				t.Errorf("the document is not valid OpenAPI 3.1: %v", err)
			}
			root := doc.(map[string]any)
			schemas := schemaObjects(root)
			if len(schemas) == 0 {
				t.Fatal("found no schema in the document")
			}
			for where, s := range schemas {
				if err := dialect.Validate(s); err != nil {
					t.Errorf("the schema at %s is not valid in the OAS 3.1 dialect: %v", where, err)
				}
			}
			for _, ref := range refs(root) {
				if !resolves(root, ref) {
					t.Errorf("%s does not resolve in the document", ref)
				}
			}
		})
	}
}

// schemaObjects finds every Schema Object by where OpenAPI puts one: under a "schema" key of a
// media type or parameter, and the values of components.schemas.
func schemaObjects(root map[string]any) map[string]any {
	out := map[string]any{}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				// x-itemSchema is the event of an Events[T] stream (3.2's itemSchema, under an
				// extension in 3.1): a Schema Object too.
				if k == "schema" || k == "x-itemSchema" {
					if _, isMap := e.(map[string]any); isMap {
						out[path+"/"+k] = e
						continue
					}
				}
				walk(path+"/"+k, e)
			}
		case []any:
			for i, e := range x {
				walk(path+"/"+strconv.Itoa(i), e)
			}
		}
	}
	walk("", root["paths"])
	if comps, ok := root["components"].(map[string]any); ok {
		walk("/components/responses", comps["responses"])
		if ss, ok := comps["schemas"].(map[string]any); ok {
			for n, s := range ss {
				out["/components/schemas/"+n] = s
			}
		}
	}
	return out
}

// resolves follows a local JSON pointer ("#/components/schemas/X") through the document.
func resolves(root map[string]any, ref string) bool {
	p, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return false
	}
	var v any = root
	for _, part := range strings.Split(p, "/") {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		if v, ok = m[part]; !ok {
			return false
		}
	}
	return true
}

func TestTheDocumentCheckRejectsAnInvalidDocument(t *testing.T) {
	docSchema, err := compiler(t).Compile("https://spec.openapis.org/oas/3.1/schema/2022-10-07")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"openapi":"3.1.0","info":{"title":"t"},"paths":{}}`,
		`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{"/x":{"get":{"responses":{"200":{}}}}}}`,
		`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{"/x/{id}":{"get":{"parameters":[{"name":"id","in":"path","schema":{}}],"responses":{"200":{"description":"ok"}}}}}}`,
	} {
		if docSchema.Validate(mustParse(t, bad)) == nil {
			t.Errorf("%s passed", bad)
		}
	}
}

func mustParse(t *testing.T, s string) any {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}
