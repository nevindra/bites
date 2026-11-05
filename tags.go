package bites

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/nevindra/bites/internal/wire"
)

// A foreignTag is a struct tag key that another Go framework reads and bites does not (ADR 0021):
// written on an Input, it would state a rule or a place that nothing keeps, so a request that breaks
// it would be accepted without a word. Each group says what the tag would have done and where
// bites says it instead. Tags that change nothing a request may carry (`doc`, `example`, `db`,
// `gorm`) are left alone, because a shared type may carry them for another reader.
type foreignTag struct {
	keys      []string
	lost      string // what would not happen
	instead   string // where bites says it; for a validator tag, %s is the Input's name
	validator bool   // read by a validator library, which the Input's Validate may run
}

var foreignTags = []foreignTag{
	{[]string{"validate", "binding"}, "its rules would never run",
		"give the field a type of your own with a rule method (Length, Range, Items, Pattern, Format, Check), or run your validator in %s.Validate", true},
	{[]string{"path", "query", "header", "cookie", "form", "uri", "param", "params", "reqHeader"}, "it would not say where the value comes from",
		"the section the field sits in says that, and its wire name comes from the naming policy or a WireNames method", false},
	{[]string{"minLength", "maxLength"}, "the bound would never be checked",
		"a length is a Length method on the field's type", false},
	{[]string{"minimum", "maximum"}, "the bound would never be checked",
		"a bound is a Range method on the field's type", false},
	{[]string{"exclusiveMinimum", "exclusiveMaximum"}, "the bound would never be checked",
		"an exclusive bound is an ExclusiveMin or ExclusiveMax method on a float type (an integer's is a Range with the next integer)", false},
	{[]string{"multipleOf"}, "the step would never be checked",
		"a step is a MultipleOf method on the field's type", false},
	{[]string{"minItems", "maxItems"}, "the bound would never be checked",
		"a list's bound is an Items method on the field's type", false},
	{[]string{"uniqueItems"}, "the items would never be compared",
		"a list that holds no item twice is a Unique method on the field's type", false},
	{[]string{"minProperties", "maxProperties"}, "the bound would never be checked",
		"a map's bound is a Members method on the field's type", false},
	{[]string{"pattern"}, "the pattern would never be checked",
		"a pattern is a Pattern method on the field's type", false},
	{[]string{"format"}, "the format would never be checked",
		"a format is a Format method on the field's type", false},
	{[]string{"enum"}, "the values would never be checked",
		"an enum is a Values method on the field's type", false},
	{[]string{"required"}, "it would not change whether the field may be missing",
		"a plain field is required and an Opt[T] may be absent", false},
	{[]string{"default"}, "the default would never be applied",
		"a Query, Header, Cookie or Form section states its defaults in a Defaults method, and a JSON Body reads an Opt[T] with Or in the handler", false},
}

// refuseForeignTag refuses a field whose tag holds a key of foreignTags. A validate or binding tag
// is let through when the Input has a Validate method, the one place an application's own
// validator can read it.
func (c *compiler) refuseForeignTag(fpath string, tag reflect.StructTag) error {
	if tag == "" {
		return nil
	}
	for _, g := range foreignTags {
		for _, k := range g.keys {
			if _, ok := tag.Lookup(k); !ok {
				continue
			}
			if g.validator && c.inValidates {
				continue
			}
			instead := g.instead
			if g.validator {
				instead = fmt.Sprintf(instead, c.inName)
			}
			return refusalf("%s has a %s tag, which bites does not read, so %s: %s", fpath, k, g.lost, instead)
		}
	}
	return nil
}

// refuseJSONRename refuses a json tag on a text field (Path, Query, Header, Cookie, Form) that
// names it otherwise than its wire name: the tag is read in a JSON Body only, so the field would
// be read under a name its author did not write. A json tag that agrees with the wire name, as
// on a struct shared with a JSON body, changes nothing and is let through.
func refuseJSONRename(fpath string, kind secKind, tag reflect.StructTag, name string) error {
	v, ok := tag.Lookup("json")
	if !ok {
		return nil
	}
	jname, _, _ := strings.Cut(v, ",")
	if jname == "" || jname == name || (kind == secHeader && strings.EqualFold(jname, name)) {
		return nil
	}
	return refusalf("%s has a json tag naming it %q, but its wire name is %q: a json tag is read in a JSON Body only, so name it with a WireNames method", fpath, jname, name)
}

var jsonUnmarshalerType = reflect.TypeFor[interface{ UnmarshalJSON([]byte) error }]()

// refuseForeignTagsIn walks a JSON Body's type and refuses the first field, at any depth, whose tag
// holds a key of foreignTags. A type that decodes itself is not walked: its fields are its own.
func (c *compiler) refuseForeignTagsIn(goPath string, t reflect.Type, seen map[reflect.Type]bool) error {
	for {
		if k, elem, _, _ := wire.PresenceOf(t); k != wire.None {
			t = elem
			continue
		}
		if k := t.Kind(); k == reflect.Pointer || k == reflect.Slice || k == reflect.Array || k == reflect.Map {
			t = t.Elem()
			continue
		}
		break
	}
	if t.Kind() != reflect.Struct || seen[t] || implementsText(t) || reflect.PointerTo(t).Implements(jsonUnmarshalerType) {
		return nil
	}
	seen[t] = true
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		fpath := goPath + "." + f.Name
		if err := c.refuseForeignTag(fpath, f.Tag); err != nil {
			return err
		}
		if err := c.refuseForeignTagsIn(fpath, f.Type, seen); err != nil {
			return err
		}
	}
	return nil
}
