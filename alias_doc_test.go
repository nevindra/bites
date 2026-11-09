package bites_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/nevindra/bites"
)

// aliasTypes is every alias the root package declares, at a type argument where it is generic.
var aliasTypes = map[string]reflect.Type{
	"Opt":             reflect.TypeFor[bites.Opt[int]](),
	"CSV":             reflect.TypeFor[bites.CSV[int]](),
	"Null":            reflect.TypeFor[bites.Null[int]](),
	"RawJSON":         reflect.TypeFor[bites.RawJSON[int]](),
	"Date":            reflect.TypeFor[bites.Date](),
	"NamingPolicy":    reflect.TypeFor[bites.NamingPolicy](),
	"Clock":           reflect.TypeFor[bites.Clock](),
	"NonEmpty":        reflect.TypeFor[bites.NonEmpty](),
	"PositiveInt":     reflect.TypeFor[bites.PositiveInt](),
	"PositiveInt64":   reflect.TypeFor[bites.PositiveInt64](),
	"JSONSchema":      reflect.TypeFor[bites.JSONSchema](),
	"StreamWriter":    reflect.TypeFor[bites.StreamWriter](),
	"CloseInfo":       reflect.TypeFor[bites.CloseInfo](),
	"ServerOnlyError": reflect.TypeFor[bites.ServerOnlyError](),
	"CloseCause":      reflect.TypeFor[bites.CloseCause](),
}

// go doc shows an alias with its own doc comment only, never the methods and fields of the type
// it names, so an agent reading go doc bites.Date would not learn Date.Sub exists. The comment
// names each of them instead, and this holds it to the type.
func TestTheDocCommentOfEveryAliasNamesEachMethodAndFieldOfItsType(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.TYPE {
				continue
			}
			for _, sp := range g.Specs {
				ts := sp.(*ast.TypeSpec)
				if !ts.Assign.IsValid() || !ts.Name.IsExported() {
					continue
				}
				name := ts.Name.Name
				seen[name] = true
				typ, ok := aliasTypes[name]
				if !ok {
					t.Errorf("%s: the alias %s is not in aliasTypes: add it, so its doc comment is checked", f, name)
					continue
				}
				doc := ts.Doc.Text()
				if doc == "" {
					doc = g.Doc.Text()
				}
				for _, m := range members(typ) {
					if !regexp.MustCompile(`\b` + m + `\b`).MatchString(doc) {
						t.Errorf("%s: the doc comment of %s does not name %s, which go doc will not show through the alias", f, name, m)
					}
				}
			}
		}
	}
	for name := range aliasTypes {
		if !seen[name] {
			t.Errorf("aliasTypes names %s, which is no longer an alias of the package", name)
		}
	}
}

// members is the exported methods of t and *t and the exported fields of a struct t.
func members(t reflect.Type) []string {
	var out []string
	mt := t
	if t.Kind() != reflect.Interface {
		mt = reflect.PointerTo(t)
	}
	for i := range mt.NumMethod() {
		out = append(out, mt.Method(i).Name)
	}
	if t.Kind() == reflect.Struct {
		for i := range t.NumField() {
			if f := t.Field(i); f.IsExported() {
				out = append(out, f.Name)
			}
		}
	}
	return out
}
