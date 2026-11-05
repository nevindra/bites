package openapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/openapi/fixture/a"
	"github.com/nevindra/bites/internal/openapi/fixture/b"
	"github.com/nevindra/bites/internal/wire"
)

var update = flag.Bool("update", false, "rewrite the golden documents in testdata")

// document is what a golden holds: the schema of the root and the components it needs.
func document(t *testing.T, r *Registry, typ reflect.Type, m Mode) []byte {
	t.Helper()
	s, err := r.Schema(typ, m)
	if err != nil {
		t.Fatal(err)
	}
	return render1(t, r, s)
}

func render1(t *testing.T, r *Registry, s Schema) []byte {
	t.Helper()
	rs, err := r.Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	comps, err := r.Components()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"schema": rs, "components": comps}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// schemaOf returns the component schema of the one named type T.
func schemaOf[T any](t *testing.T, m Mode) Schema { return schemaOfMode[T](t, m) }

func asJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

type Level string

func (Level) Values() []string { return []string{"low", "high"} }

type withPresence struct {
	Plain  string
	Opt    wire.Opt[string]
	Null   wire.Null[string]
	Patch  wire.Opt[wire.Null[int32]]
	Status wire.Null[Level]
}

func TestAnOptFieldIsNotRequired(t *testing.T) {
	s := schemaOf[withPresence](t, Request)
	req := asJSON(s["required"])
	if !strings.Contains(req, `"plain"`) || strings.Contains(req, `"opt"`) || strings.Contains(req, `"patch"`) {
		t.Errorf("required: %s", req)
	}
	if !strings.Contains(req, `"null"`) {
		t.Errorf("a Null field is present on the wire, so it is required: %s", req)
	}
}

func TestANullFieldIsNullable(t *testing.T) {
	props := schemaOf[withPresence](t, Request)["properties"].(map[string]any)
	if got := asJSON(props["null"]); got != `{"type":["string","null"]}` {
		t.Errorf("%s", got)
	}
	if got := asJSON(props["patch"]); got != `{"format":"int32","type":["integer","null"]}` {
		t.Errorf("Opt[Null[T]] is absent or null: %s", got)
	}
	if got := asJSON(props["opt"]); got != `{"type":"string"}` {
		t.Errorf("%s", got)
	}
	if got := asJSON(props["status"]); !strings.Contains(got, `anyOf`) || !strings.Contains(got, `"type":"null"`) {
		t.Errorf("a nullable reference is a union with null: %s", got)
	}
}

type wireNames struct {
	ItemID   int64
	HTTPCode int32
	Tagged   string `json:"as_tagged"`
}

func TestTheSchemaUsesTheAppsNamingPolicy(t *testing.T) {
	for _, tc := range []struct {
		pol  wire.Policy
		want []string
	}{
		{nil, []string{"item_id", "http_code", "as_tagged"}},
		{wire.CamelCase, []string{"itemId", "httpCode", "as_tagged"}},
	} {
		r := New(Options{Names: tc.pol})
		s, err := r.Schema(reflect.TypeFor[wireNames](), Request)
		if err != nil {
			t.Fatal(err)
		}
		s, _ = r.Resolve(s)
		comps, _ := r.Components()
		props := comps["wireNames"]["properties"].(Schema)
		for _, n := range tc.want {
			if _, ok := props[n]; !ok {
				t.Errorf("%v: no property %q in %s (root %s)", tc.pol != nil, n, asJSON(props), asJSON(s))
			}
		}
	}
}

type Tree struct {
	Name     string
	Children []Tree
	Parent   *Tree
}

type Bag map[string]Bag

func TestARecursiveTypeBecomesARefToItsComponent(t *testing.T) {
	r := New(Options{})
	s, err := r.Schema(reflect.TypeFor[Tree](), Response)
	if err != nil {
		t.Fatal(err)
	}
	s, _ = r.Resolve(s)
	if asJSON(s) != `{"$ref":"#/components/schemas/Tree"}` {
		t.Errorf("%s", asJSON(s))
	}
	comps, _ := r.Components()
	got := asJSON(comps["Tree"]["properties"])
	if !strings.Contains(got, `"items":{"$ref":"#/components/schemas/Tree"}`) {
		t.Errorf("%s", got)
	}
	// A named map that holds itself needs a component too, or the schema would never end.
	r = New(Options{})
	s, err = r.Schema(reflect.TypeFor[Bag](), Request)
	if err != nil {
		t.Fatal(err)
	}
	s, _ = r.Resolve(s)
	if asJSON(s) != `{"$ref":"#/components/schemas/Bag"}` {
		t.Errorf("%s", asJSON(s))
	}
}

func TestAnEnumListsTheValuesItsValuesMethodReturns(t *testing.T) {
	r := New(Options{})
	s, _ := r.Schema(reflect.TypeFor[Level](), Request)
	_ = s
	comps, _ := r.Components()
	if got := asJSON(comps["Level"]); got != `{"enum":["low","high"],"type":"string"}` {
		t.Errorf("%s", got)
	}
}

func TestANullableEnumAllowsNull(t *testing.T) {
	type in struct{ L wire.Null[string] }
	_ = in{}
	got := asJSON(nullable(Schema{"type": "string", "enum": []any{"a"}}))
	if got != `{"enum":["a",null],"type":["string","null"]}` {
		t.Errorf("%s", got)
	}
}

type skipping struct {
	Shown   string
	Hidden  string `json:"-"`
	private int    `json:"-"`
	Renamed string `json:"other,omitempty"`
}

func TestAFieldTheCodecSkipsIsNotInTheSchema(t *testing.T) {
	s := schemaOf[skipping](t, Request)
	_ = s
	r := New(Options{})
	r.Schema(reflect.TypeFor[skipping](), Request)
	comps, _ := r.Components()
	props := comps["skipping"]["properties"].(Schema)
	if len(props) != 2 || props["shown"] == nil || props["other"] == nil {
		t.Errorf("%s", asJSON(props))
	}
	_ = skipping{}.private
}

func TestOmitemptyMakesAResponseFieldOptionalButNotARequestField(t *testing.T) {
	r := New(Options{})
	r.Schema(reflect.TypeFor[skipping](), Request)
	r.Schema(reflect.TypeFor[skipping](), Response)
	comps, _ := r.Components()
	if asJSON(comps["skipping.Request"]["required"]) != `["shown","other"]` ||
		asJSON(comps["skipping.Response"]["required"]) != `["shown"]` {
		t.Errorf("%s", asJSON(comps))
	}
}

type pointers struct {
	P *string
}

type iface struct{ V any }

func TestATypeTheCodecRefusesIsRefusedHereWithTheSameReason(t *testing.T) {
	type bad struct{ C chan int }
	type mapKey struct{ M map[int]string }
	type optOpt struct{ O wire.Opt[wire.Opt[int]] }
	for _, tc := range []struct {
		typ reflect.Type
		m   Mode
	}{
		{reflect.TypeFor[pointers](), Request},
		{reflect.TypeFor[iface](), Request},
		{reflect.TypeFor[bad](), Request},
		{reflect.TypeFor[bad](), Response},
		{reflect.TypeFor[mapKey](), Response},
		{reflect.TypeFor[optOpt](), Request},
		{reflect.TypeFor[wire.Opt[int]](), Request},
	} {
		_, got := New(Options{}).Schema(tc.typ, tc.m)
		var want error
		if tc.m == Response {
			_, want = jsoncodec.CompileEncoder(tc.typ, jsoncodec.Options{})
		} else {
			_, want = jsoncodec.CompileDecoder(tc.typ, jsoncodec.Options{})
		}
		if got == nil || want == nil || got.Error() != want.Error() {
			t.Errorf("%s %s: got %v, codec says %v", tc.typ, tc.m, got, want)
		}
	}
}

func TestAResponsePointerIsNullableAndARequestPointerIsRefused(t *testing.T) {
	r := New(Options{})
	if _, err := r.Schema(reflect.TypeFor[pointers](), Response); err != nil {
		t.Fatal(err)
	}
	comps, _ := r.Components()
	if got := asJSON(comps["pointers"]["properties"]); got != `{"p":{"type":["string","null"]}}` {
		t.Errorf("%s", got)
	}
	if asJSON(comps["pointers"]["required"]) != `["p"]` {
		t.Errorf("the encoder writes null for nil, so the key is always there")
	}
}

type unknownPolicy struct{ A string }

func TestUnknownMembersAreRefusedInARequestAndAllowedWhenTheRouteAllowsThem(t *testing.T) {
	cases := map[Mode]string{Request: `"additionalProperties":false`, RequestOpen: ``, Response: ``}
	for m, want := range cases {
		got := asJSON(schemaOfMode[unknownPolicy](t, m))
		if want == "" && strings.Contains(got, "additionalProperties") {
			t.Errorf("%s: %s", m, got)
		}
		if want != "" && !strings.Contains(got, want) {
			t.Errorf("%s: %s", m, got)
		}
	}
}

func schemaOfMode[T any](t *testing.T, m Mode) Schema {
	t.Helper()
	r := New(Options{})
	r.Schema(reflect.TypeFor[T](), m)
	comps, err := r.Components()
	if err != nil {
		t.Fatal(err)
	}
	return comps[reflect.TypeFor[T]().Name()]
}

func TestScalarsMapToTheirJSONSchemaTypes(t *testing.T) {
	type scalars struct {
		B    bool
		I    int
		I8   int8
		I32  int32
		U8   uint8
		U32  uint32
		U64  uint64
		F32  float32
		F64  float64
		S    string
		Raw  []byte
		Arr  [4]byte
		T    time.Time
		ID   uuid.UUID
		List []string
		Pair [2]int
		Map  map[string]float64
		Any  any
	}
	r := New(Options{})
	if _, err := r.Schema(reflect.TypeFor[scalars](), Response); err != nil {
		t.Fatal(err)
	}
	comps, _ := r.Components()
	props := comps["scalars"]["properties"].(Schema)
	want := map[string]string{
		"b":    `{"type":"boolean"}`,
		"i":    `{"format":"int64","type":"integer"}`,
		"i8":   `{"maximum":127,"minimum":-128,"type":"integer"}`,
		"i32":  `{"format":"int32","type":"integer"}`,
		"u8":   `{"maximum":255,"minimum":0,"type":"integer"}`,
		"u32":  `{"maximum":4294967295,"minimum":0,"type":"integer"}`,
		"u64":  `{"maximum":18446744073709551615,"minimum":0,"type":"integer"}`,
		"f32":  `{"format":"float","type":"number"}`,
		"f64":  `{"format":"double","type":"number"}`,
		"s":    `{"type":"string"}`,
		"raw":  `{"contentEncoding":"base64","type":"string"}`,
		"arr":  `{"contentEncoding":"base64","type":"string"}`,
		"t":    `{"format":"date-time","type":"string"}`,
		"id":   `{"format":"uuid","type":"string"}`,
		"list": `{"items":{"type":"string"},"type":"array"}`,
		"pair": `{"items":{"format":"int64","type":"integer"},"maxItems":2,"minItems":2,"type":"array"}`,
		"map":  `{"additionalProperties":{"format":"double","type":"number"},"type":"object"}`,
		"any":  `{}`,
	}
	for k, w := range want {
		if got := asJSON(props[k]); got != w {
			t.Errorf("%s: got %s, want %s", k, got, w)
		}
	}
}

func TestBodyHookSeesThroughAWrapper(t *testing.T) {
	type wrapper struct{ Value wireNames }
	r := New(Options{Body: func(t reflect.Type) (reflect.Type, bool) {
		if t == reflect.TypeFor[wrapper]() {
			return t.Field(0).Type, true
		}
		return nil, false
	}})
	s, err := r.Schema(reflect.TypeFor[wrapper](), Request)
	if err != nil {
		t.Fatal(err)
	}
	s, _ = r.Resolve(s)
	if asJSON(s) != `{"$ref":"#/components/schemas/wireNames"}` {
		t.Errorf("%s", asJSON(s))
	}
}

func TestTwoTypesWithOneNameInDifferentPackagesAreToldApartByPackage(t *testing.T) {
	type both struct {
		A a.User
		B b.User
	}
	r := New(Options{})
	if _, err := r.Schema(reflect.TypeFor[both](), Response); err != nil {
		t.Fatal(err)
	}
	comps, err := r.Components()
	if err != nil {
		t.Fatal(err)
	}
	if comps["a.User"] == nil || comps["b.User"] == nil || comps["User"] != nil {
		t.Errorf("names: %v", keysOf(comps))
	}
	// The order types were added in does not change the names.
	r2 := New(Options{})
	r2.Schema(reflect.TypeFor[b.User](), Response)
	r2.Schema(reflect.TypeFor[a.User](), Response)
	c2, _ := r2.Components()
	if asJSON(c2["a.User"]) != asJSON(comps["a.User"]) {
		t.Errorf("%v", keysOf(c2))
	}
}

func keysOf(m map[string]Schema) (out []string) {
	for k := range m {
		out = append(out, k)
	}
	return
}

func TestTwoTypesThatCannotBeToldApartAreRefused(t *testing.T) {
	f1 := func() reflect.Type { type Dup struct{ A int }; return reflect.TypeFor[Dup]() }
	f2 := func() reflect.Type { type Dup struct{ B int }; return reflect.TypeFor[Dup]() }
	r := New(Options{})
	r.Schema(f1(), Response)
	r.Schema(f2(), Response)
	if _, err := r.Components(); err == nil || !strings.Contains(err.Error(), "cannot get distinct component names") {
		t.Errorf("%v", err)
	}
}

type Page[T any] struct{ Items []T }

func TestAGenericInstanceGetsAComponentNameMadeOfItsArguments(t *testing.T) {
	r := New(Options{})
	r.Schema(reflect.TypeFor[Page[a.User]](), Response)
	comps, _ := r.Components()
	if comps["Page_User"] == nil {
		t.Errorf("%v", keysOf(comps))
	}
}

func TestATypeUsedInARequestAndAResponseIsOneComponentWhenTheSchemasAgree(t *testing.T) {
	r := New(Options{})
	r.Schema(reflect.TypeFor[wireNames](), Response)
	r.Schema(reflect.TypeFor[wireNames](), RequestOpen)
	comps, _ := r.Components()
	if len(comps) != 1 || comps["wireNames"] == nil {
		t.Errorf("%v", keysOf(comps))
	}
}

type order struct {
	ID      uuid.UUID
	Status  Level
	Placed  time.Time
	Lines   []orderLine
	Notes   wire.Opt[string]
	Paid    wire.Null[time.Time]
	Tags    map[string]string
	Replies []order
}

type orderLine struct {
	SKU string
	Qty uint16
}

func TestTwoRunsProduceByteIdenticalDocuments(t *testing.T) {
	var first []byte
	for i := range 20 {
		r := New(Options{})
		// The order of registration changes between runs; the bytes must not.
		if i%2 == 0 {
			r.Schema(reflect.TypeFor[order](), Response)
			r.Schema(reflect.TypeFor[order](), Request)
		} else {
			r.Schema(reflect.TypeFor[order](), Request)
			r.Schema(reflect.TypeFor[order](), Response)
		}
		s, _ := r.Schema(reflect.TypeFor[order](), Response)
		got := render1(t, r, s)
		if first == nil {
			first = got
		} else if !bytes.Equal(first, got) {
			t.Fatalf("run %d differs", i)
		}
	}
}

// Shapes of the examples and the budget tests, copied because package main and a _test type
// cannot be imported.
type usersCreate struct {
	Name  string
	Email wire.Opt[string]
}

type usersUser struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email,omitzero"`
	Admin bool   `json:"admin,omitzero"`
}

type budgetBody struct {
	Name  string
	Email string
	Age   int
}

type budgetOut struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age"`
	Page  int    `json:"page"`
}

type budgetList struct {
	Name  string
	Lines wire.Opt[[]orderLine]
	Notes wire.Opt[[]string]
}

func TestGoldenDocumentsMatch(t *testing.T) {
	cases := []struct {
		name string
		typ  reflect.Type
		mode Mode
	}{
		{"users_create_request", reflect.TypeFor[usersCreate](), Request},
		{"users_user_response", reflect.TypeFor[usersUser](), Response},
		{"budget_body_request", reflect.TypeFor[budgetBody](), Request},
		{"budget_out_response", reflect.TypeFor[budgetOut](), Response},
		{"budget_list_request", reflect.TypeFor[budgetList](), Request},
		{"order_response", reflect.TypeFor[order](), Response},
		{"order_request", reflect.TypeFor[order](), Request},
		{"presence_request", reflect.TypeFor[withPresence](), Request},
		{"tree_response", reflect.TypeFor[Tree](), Response},
		{"bag_request", reflect.TypeFor[Bag](), Request},
		{"page_of_user_response", reflect.TypeFor[Page[a.User]](), Response},
		{"names_camel_request", reflect.TypeFor[wireNames](), Request},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := Options{}
			if tc.name == "names_camel_request" {
				o.Names = wire.CamelCase
			}
			got := document(t, New(o), tc.typ, tc.mode)
			path := filepath.Join("testdata", tc.name+".json")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from its golden; run with -update if the change is intended\n got: %s\nwant: %s", path, got, want)
			}
		})
	}
}

type described struct {
	Name  string
	Inner describedInner
	Skip  string `json:"-"`
}

type describedInner struct{ N int }

func TestAFieldDescriptionIsAddedToItsPropertyEvenWhenThePropertyIsAReference(t *testing.T) {
	r := New(Options{Descriptions: func(typ reflect.Type) map[string]string {
		if typ == reflect.TypeFor[described]() {
			return map[string]string{"Name": "who", "Inner": "the nested one"}
		}
		return nil
	}})
	s, err := r.Schema(reflect.TypeFor[described](), Response)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := r.Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := asJSON(rs["$ref"]); got == "" {
		t.Fatalf("%v", rs)
	}
	comps, _ := r.Components()
	props := comps["described"]["properties"].(Schema)
	if got := asJSON(props["name"]); got != `{"description":"who","type":"string"}` {
		t.Errorf("name: %s", got)
	}
	if got := asJSON(props["inner"]); got != `{"$ref":"#/components/schemas/describedInner","description":"the nested one"}` {
		t.Errorf("inner: %s", got)
	}
}

func TestADescriptionForAFieldNotOnTheWireIsAnErrorNamingTheFieldsThatAre(t *testing.T) {
	r := New(Options{Descriptions: func(reflect.Type) map[string]string { return map[string]string{"Skip": "x"} }})
	_, err := r.Schema(reflect.TypeFor[described](), Response)
	var de *DescriptionError
	if !errors.As(err, &de) || de.Name != "Skip" {
		t.Fatalf("%v", err)
	}
	if want := "described.Descriptions names Skip, which is not a field of described on the wire (its fields: Name, Inner)"; err.Error() != want {
		t.Errorf("\n got  %s\n want %s", err, want)
	}
}

func TestASchemaNamerNamesATypeWhetherOrNotItsNameCollides(t *testing.T) {
	var asked [][2]string
	r := New(Options{Namer: func(pkg, name string) string {
		if name != "both" {
			asked = append(asked, [2]string{pkg, name})
		}
		if strings.HasSuffix(pkg, "fixture/a") {
			return "CommitmentUser"
		}
		return ""
	}})
	type both struct {
		A a.User
		B b.User
	}
	if _, err := r.Schema(reflect.TypeFor[both](), Response); err != nil {
		t.Fatal(err)
	}
	comps, err := r.Components()
	if err != nil {
		t.Fatal(err)
	}
	if comps["CommitmentUser"] == nil || comps["User"] == nil || comps["a.User"] != nil || comps["b.User"] != nil {
		t.Errorf("names: %v", keysOf(comps))
	}
	if len(asked) != 2 || asked[0] != [2]string{"github.com/nevindra/bites/internal/openapi/fixture/a", "User"} || asked[1][1] != "User" {
		t.Errorf("the namer is asked once per type with its package path and default name: %v", asked)
	}
}

func TestASchemaNamerIsGivenTheGenericInstancesDefaultName(t *testing.T) {
	var got string
	r := New(Options{Namer: func(_, name string) string {
		if strings.HasPrefix(name, "Page") {
			got = name
			return "UserPage"
		}
		return ""
	}})
	r.Schema(reflect.TypeFor[Page[a.User]](), Response)
	comps, _ := r.Components()
	if got != "Page_User" || comps["UserPage"] == nil {
		t.Errorf("%q %v", got, keysOf(comps))
	}
}

func TestTheModeSuffixesAreAddedAfterTheNamersName(t *testing.T) {
	type modal struct {
		N int `json:"n,omitempty"`
	}
	r := New(Options{Namer: func(_, name string) string {
		if name == "modal" {
			return "Modal"
		}
		return ""
	}})
	r.Schema(reflect.TypeFor[modal](), Request)
	r.Schema(reflect.TypeFor[modal](), Response)
	comps, _ := r.Components()
	if len(comps) != 2 || comps["Modal.Request"] == nil || comps["Modal.Response"] == nil {
		t.Errorf("%v", keysOf(comps))
	}
}

func TestASchemaNamerThatNamesTwoTypesAlikeIsRefusedNamingBoth(t *testing.T) {
	r := New(Options{Namer: func(_, _ string) string { return "Same" }})
	r.Schema(reflect.TypeFor[a.User](), Response)
	r.Schema(reflect.TypeFor[b.User](), Response)
	_, err := r.Components()
	want := `openapi: the schema namer gave github.com/nevindra/bites/internal/openapi/fixture/a.User and github.com/nevindra/bites/internal/openapi/fixture/b.User the same name "Same", but each component needs its own`
	if err == nil || err.Error() != want {
		t.Errorf("%v", err)
	}
}

func TestASchemaNamerNameWithACharacterOutsideTheAllowedSetIsRefused(t *testing.T) {
	r := New(Options{Namer: func(_, _ string) string { return "Commitment List" }})
	r.Schema(reflect.TypeFor[a.User](), Response)
	_, err := r.Components()
	want := `openapi: the schema namer named github.com/nevindra/bites/internal/openapi/fixture/a.User "Commitment List", but a component name may use only A-Z a-z 0-9 . - and _`
	if err == nil || err.Error() != want {
		t.Errorf("%v", err)
	}
}

func TestASchemaNamerNameThatAnotherTypeHasByDefaultIsRefusedNamingBoth(t *testing.T) {
	r := New(Options{Namer: func(pkg, _ string) string {
		if strings.HasSuffix(pkg, "fixture/a") {
			return "User"
		}
		return ""
	}})
	type one struct{ B b.User }
	r.Schema(reflect.TypeFor[a.User](), Response)
	r.Schema(reflect.TypeFor[one](), Response)
	_, err := r.Components()
	want := `openapi: the schema namer named github.com/nevindra/bites/internal/openapi/fixture/a.User "User", which is the name github.com/nevindra/bites/internal/openapi/fixture/b.User has without it: name both or neither`
	if err == nil || err.Error() != want {
		t.Errorf("%v", err)
	}
}
