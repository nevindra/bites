package config

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/nevindra/bites"
)

// field is one setting, or a nested struct of settings.
type field struct {
	goName string       // the field's own Go name
	goPath string       // from the root: DB.PoolMax
	seg    string       // the key's last segment: pool_max
	key    string       // the canonical key: db.pool_max
	index  []int        // from the root struct
	typ    reflect.Type // the declared type
	elem   reflect.Type // the type after Opt is removed
	opt    bool
	list   bool    // a slice of leaves: comma-separated in the environment and in a flag
	cplx   bool    // a map or a slice of structs: read from a file only
	sub    *schema // a nested struct
	lf     *leaf   // the leaf, or the element leaf of a list
	secret bool
	def    string
	hasDef bool
	desc   string
}

type schema struct {
	typ    reflect.Type
	fields []*field
}

// leaves lists the settings in field order, depth first.
func (s *schema) leaves() []*field {
	var out []*field
	for _, f := range s.fields {
		if f.sub != nil {
			out = append(out, f.sub.leaves()...)
			continue
		}
		out = append(out, f)
	}
	return out
}

func (s *schema) find(goName string) *field {
	for _, f := range s.fields {
		if f.goName == goName {
			return f
		}
	}
	return nil
}

type compiler struct {
	root string // the root type's name, for messages
	site string
	envP string // the Env prefix, "" when Env was not given
}

func (c *compiler) fail(format string, args ...any) {
	panic(fmt.Sprintf("config.Load[%s]: %s\n\tcalled at %s", c.root, fmt.Sprintf(format, args...), c.site))
}

var wirePath = reflect.TypeFor[bites.Opt[int]]().PkgPath()

// presence says whether t is a bites.Opt (opt) or a bites.Null, and the type it holds.
func presence(t reflect.Type) (opt, null bool, elem reflect.Type) {
	if t.Kind() != reflect.Struct || t.PkgPath() != wirePath || t.NumField() != 2 {
		return false, false, nil
	}
	switch {
	case strings.HasPrefix(t.Name(), "Opt["):
		return true, false, t.Field(0).Type
	case strings.HasPrefix(t.Name(), "Null["):
		return false, true, t.Field(0).Type
	}
	return false, false, nil
}

func isLeafStruct(t reflect.Type) bool {
	return t == timeType || t == dateType || reflect.PointerTo(t).Implements(textUnm)
}

var wordRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func (c *compiler) compileStruct(t reflect.Type, goPrefix, keyPrefix string, index []int) *schema {
	sc := &schema{typ: t}
	wire := c.stringMap(t, "WireNames")
	own := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		if sf := t.Field(i); sf.IsExported() || sf.Anonymous {
			own[sf.Name] = true
		}
	}
	c.checkNames("WireNames", t, wire, own)
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		path := goPrefix + sf.Name
		idx := append(slices.Clone(index), i)
		if !sf.IsExported() {
			c.fail("field %s is unexported, so no origin can fill it; export it or move it out of %s", path, c.root)
		}
		ft := sf.Type
		if sf.Anonymous && ft.Kind() == reflect.Struct && !isLeafStruct(ft) {
			if o, n, _ := presence(ft); !o && !n {
				inner := c.compileStruct(ft, goPrefix, keyPrefix, idx)
				sc.fields = append(sc.fields, inner.fields...)
				continue
			}
		}
		seg := bites.SnakeCase(sf.Name)
		if w, ok := wire[sf.Name]; ok {
			if !wordRe.MatchString(w) {
				c.fail("WireNames gives %s the word %q, which is not letters, digits and underscores", sf.Name, w)
			}
			seg = w
		}
		f := &field{goName: sf.Name, goPath: path, seg: seg, key: joinKey(keyPrefix, seg), index: idx, typ: ft, elem: ft}
		c.classify(f, sc, t)
		sc.fields = append(sc.fields, f)
	}
	c.applyDefaults(sc, t)
	return sc
}

// classify decides what a field is, or refuses it.
func (c *compiler) classify(f *field, parent *schema, owner reflect.Type) {
	t := f.typ
	if t.Kind() == reflect.Pointer {
		c.fail("field %s is a %s; use bites.Opt[%s] for a setting that may be missing", f.goPath, t, t.Elem())
	}
	if opt, null, el := presence(t); opt {
		f.opt, f.elem = true, el
		t = el
		if t.Kind() == reflect.Pointer {
			c.fail("field %s holds a %s inside an Opt; use bites.Opt[%s]", f.goPath, t, t.Elem())
		}
		if o, n, _ := presence(t); o || n {
			c.fail("field %s has type %s, which cannot be read from text; an Opt holds a plain value", f.goPath, f.typ)
		}
	} else if null {
		c.fail("field %s has type %s, which cannot be read from text; a setting is present or absent, use bites.Opt[%s]", f.goPath, typeName(t), el)
	}
	c.noRules(f, t)
	if lf, refusal := compileLeaf(t); lf != nil {
		f.lf = lf
		f.secret = t == secretType
		return
	} else if refusal != "" {
		c.fail("field %s %s", f.goPath, refusal)
	}
	switch t.Kind() {
	case reflect.Struct:
		if f.opt {
			c.fail("field %s is an Opt of a struct, which cannot be read from text; make the struct's fields Opt", f.goPath)
		}
		if hasMethod(reflect.PointerTo(t), "Validate") {
			c.fail("field %s has a Validate method, which is never called; declare it on %s", f.goPath, c.root)
		}
		f.sub = c.compileStruct(t, f.goPath+".", f.key, f.index)
		return
	case reflect.Slice:
		el := t.Elem()
		if el.Kind() == reflect.Pointer {
			c.fail("field %s is a %s; use bites.Opt[%s] for a setting that may be missing", f.goPath, el, el.Elem())
		}
		if lf, refusal := compileLeaf(el); lf != nil {
			f.list, f.lf, f.secret = true, lf, el == secretType
			return
		} else if refusal != "" {
			c.fail("field %s %s", f.goPath, refusal)
		}
		c.complexOK(f, el)
		f.cplx = true
		return
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			c.fail("field %s has type %s, whose key is not a string, so it cannot be read from a file; use a string key", f.goPath, t)
		}
		c.complexOK(f, t.Elem())
		f.cplx = true
		return
	}
	c.fail("field %s has type %s, which cannot be read from text; use a string and open it in main", f.goPath, typeName(t))
}

// noRules refuses the rules a setting does not check: they would be accepted and not run.
func (c *compiler) noRules(f *field, t reflect.Type) {
	for _, rule := range []string{"Items", "Format", "Pattern"} {
		if isRule(reflect.PointerTo(t), rule) {
			c.fail("field %s has %s %s rule, which settings do not check; put the rule in a Check method", f.goPath, article(rule), rule)
		}
	}
	for _, r := range numberAndCollectionRules {
		if r.kind(t.Kind()) && isRule(reflect.PointerTo(t), r.name) {
			c.fail("field %s has %s %s rule, which settings do not check; put the rule in a Check method", f.goPath, article(r.name), r.name)
		}
	}
	if t.Kind() == reflect.Slice {
		if lf, _ := compileLeaf(t.Elem()); lf != nil {
			for _, rule := range []string{"Format", "Pattern"} {
				if isRule(reflect.PointerTo(t.Elem()), rule) {
					c.fail("field %s has %s %s rule, which settings do not check; put the rule in a Check method", f.goPath, article(rule), rule)
				}
			}
		}
	}
}

// numberAndCollectionRules are the rules of ADR 0021 a type states for a request and a setting
// does not check, each read only on the kind it applies to: a struct's own Unique is not one.
var numberAndCollectionRules = []struct {
	name string
	kind func(reflect.Kind) bool
}{
	{"Unique", func(k reflect.Kind) bool { return k == reflect.Slice }},
	{"Members", func(k reflect.Kind) bool { return k == reflect.Map }},
	{"MultipleOf", isNumberKind},
	{"ExclusiveMin", isNumberKind},
	{"ExclusiveMax", isNumberKind},
}

func isNumberKind(k reflect.Kind) bool {
	return k >= reflect.Int && k <= reflect.Uint64 || k == reflect.Float32 || k == reflect.Float64
}

// isRule says whether t has the method of a core rule with the core's signature: Format on a
// Secret is fmt.Formatter's, not a rule.
func isRule(t reflect.Type, name string) bool {
	m, ok := t.MethodByName(name)
	if !ok || m.Type.NumIn() != 1 {
		return false
	}
	switch name {
	case "Items", "Members":
		return m.Type.NumOut() == 2
	case "Unique":
		return m.Type.NumOut() == 1 && m.Type.Out(0).Kind() == reflect.Bool
	case "MultipleOf", "ExclusiveMin", "ExclusiveMax":
		return m.Type.NumOut() == 1 && (m.Type.Out(0).Kind() == reflect.Int64 || m.Type.Out(0).Kind() == reflect.Float64)
	}
	return m.Type.NumOut() == 1 && m.Type.Out(0).Kind() == reflect.String
}

func hasMethod(t reflect.Type, name string) bool {
	_, ok := t.MethodByName(name)
	return ok
}

// complexOK checks that the element of a map or a list of structs can be read from a file.
func (c *compiler) complexOK(f *field, el reflect.Type) {
	if el.Kind() == reflect.Pointer {
		c.fail("field %s is a %s; use bites.Opt[%s] for a setting that may be missing", f.goPath, el, el.Elem())
	}
	if lf, refusal := compileLeaf(el); lf != nil {
		return
	} else if refusal != "" {
		c.fail("field %s %s", f.goPath, refusal)
	}
	switch el.Kind() {
	case reflect.Struct:
		if o, n, _ := presence(el); o || n {
			c.fail("field %s has type %s, which cannot be read from text; use a plain struct", f.goPath, el)
		}
		if hasMethod(reflect.PointerTo(el), "Defaults") {
			c.fail("field %s holds %s, which has a Defaults method; a default is not applied inside a map or a list, so it would be ignored: drop the method or flatten the struct", f.goPath, el)
		}
		sub := &compiler{root: c.root, site: c.site, envP: c.envP}
		s := sub.compileStruct(el, f.goPath+".", "", f.index)
		_ = s
	case reflect.Slice:
		c.complexOK(f, el.Elem())
	case reflect.Map:
		if el.Key().Kind() != reflect.String {
			c.fail("field %s has type %s, whose key is not a string, so it cannot be read from a file", f.goPath, el)
		}
		c.complexOK(f, el.Elem())
	default:
		c.fail("field %s has type %s, which cannot be read from text; use a string and open it in main", f.goPath, el)
	}
}

// stringMap calls a method func() map[string]string on a zero T, or returns nil.
func (c *compiler) stringMap(t reflect.Type, name string) map[string]string {
	m, ok := reflect.PointerTo(t).MethodByName(name)
	if !ok {
		return nil
	}
	if m.Type.NumIn() != 1 || m.Type.NumOut() != 1 || m.Type.Out(0) != reflect.TypeFor[map[string]string]() {
		c.fail("%s has a %s method, but it must be func() map[string]string: rename the method if it means something else", t.Name(), name)
	}
	return m.Func.Call([]reflect.Value{reflect.New(t)})[0].Interface().(map[string]string)
}

func (c *compiler) checkNames(method string, t reflect.Type, m map[string]string, own map[string]bool) {
	var names []string
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !own[n] {
			var have []string
			for k := range own {
				have = append(have, k)
			}
			sort.Strings(have)
			hint := ""
			if near, ok := nearest(n, have); ok {
				hint = "; did you mean " + near + "?"
			}
			c.fail("%s names %s, which is not a field of %s%s", method, n, t.Name(), hint)
		}
	}
}

// applyDefaults reads the Defaults and Descriptions of a struct onto its fields. It runs after
// the nested structs did, so the outer entry wins for a field both name.
func (c *compiler) applyDefaults(sc *schema, t reflect.Type) {
	for _, method := range []string{"Defaults", "Descriptions"} {
		m := c.stringMap(t, method)
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			f := c.resolve(sc, name)
			if f == nil {
				var have []string
				for _, l := range sc.leaves() {
					have = append(have, strings.TrimPrefix(l.goPath, ""))
				}
				hint := ""
				if near, ok := nearest(name, have); ok {
					hint = "; did you mean " + near + "?"
				}
				c.fail("%s names %s, which is not a field of %s%s", method, name, t.Name(), hint)
			}
			if method == "Descriptions" {
				f.desc = m[name]
				continue
			}
			c.setDefault(f, m[name])
		}
	}
}

// resolve finds a field by Go name, or by a dotted path of Go names through nested structs.
func (c *compiler) resolve(sc *schema, name string) *field {
	head, rest, more := strings.Cut(name, ".")
	f := sc.find(head)
	if f == nil {
		return nil
	}
	if !more {
		return f
	}
	if f.sub == nil {
		return nil
	}
	return c.resolve(f.sub, rest)
}

func (c *compiler) setDefault(f *field, text string) {
	switch {
	case f.sub != nil:
		c.fail("Defaults names %s, which is a struct; name its fields", f.goPath)
	case f.opt:
		c.fail("Defaults names %s, which is an Opt: an Opt is absent when nothing sets it, so a default would make it present; drop the entry or the Opt", f.goPath)
	case f.cplx:
		c.fail("Defaults names %s, which is a map or a list of structs; those are read from a file only, so write its default in a file", f.goPath)
	case f.list:
		// A list's default is the comma-separated text the environment and a flag take, each
		// element held to the element type's rules here, so the list fills as from the environment.
		if strings.TrimSpace(text) != "" {
			for i, p := range strings.Split(text, ",") {
				if err := f.lf.dec(reflect.New(f.typ.Elem()).Elem(), strings.TrimSpace(p)); err != nil {
					c.fail("the default of %s, %q, has element %d, %q, which is refused: %s", f.goPath, text, i, strings.TrimSpace(p), err)
				}
			}
		}
		f.def, f.hasDef = text, true
		return
	}
	if err := f.lf.dec(reflect.New(f.elem).Elem(), text); err != nil {
		if isTypeError(err) {
			c.fail("the default of %s, %q, is not %s: %s", f.goPath, text, f.lf.noun, err)
		}
		c.fail("the default of %s, %q, is refused: %s", f.goPath, text, err)
	}
	f.def, f.hasDef = text, true
}

// normalise folds case and the separators together, for a near miss.
func normalise(s string) string {
	return strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(s))
}

// nearest returns the candidate that differs from s in case, in _ and -, or by one edit.
func nearest(s string, candidates []string) (string, bool) {
	ns := normalise(s)
	for _, c := range candidates {
		if normalise(c) == ns {
			return c, true
		}
	}
	for _, c := range candidates {
		if oneEdit(strings.ToLower(s), strings.ToLower(c)) {
			return c, true
		}
	}
	return "", false
}

func oneEdit(a, b string) bool {
	if a == b {
		return true
	}
	la, lb := len(a), len(b)
	if la-lb > 1 || lb-la > 1 {
		return false
	}
	i := 0
	for i < la && i < lb && a[i] == b[i] {
		i++
	}
	switch {
	case la == lb:
		return a[i+1:] == b[i+1:]
	case la > lb:
		return a[i+1:] == b[i:]
	}
	return a[i:] == b[i+1:]
}

func article(word string) string {
	if word == "Unique" {
		return "a" // pronounced with a consonant
	}
	if strings.ContainsAny(word[:1], "AEIOU") {
		return "an"
	}
	return "a"
}

// typeName writes a type as the application spells it: the core's presence types live in an
// internal package and are exported as bites.Opt and bites.Null.
func typeName(t reflect.Type) string { return strings.ReplaceAll(t.String(), "wire.", "bites.") }
