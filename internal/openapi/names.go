package openapi

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// finalized is the naming of every component, computed when the types are all known.
type finalized struct {
	names  map[key]string
	comps  map[string]Schema
	inline map[key]Schema // an anonymous Body several Inputs share: written in place, not a component
}

// ref is what a reference to k becomes: the component's "#/components/schemas/Name", or, for a
// schema written in place, that schema.
func (f *finalized) ref(k key) (string, Schema) {
	if s, ok := f.inline[k]; ok {
		return "", s
	}
	return "#/components/schemas/" + f.names[k], nil
}

// Components returns every component schema by name, with references resolved.
//
// Names are decided here, from the set of types and not from the order they were added in:
//
//   - the name is the Go type name; a generic instance has its type arguments appended
//     (Page[users.User] is Page_User), and anything outside A-Z a-z 0-9 . - _ becomes "_";
//   - two different types with one name are told apart by the last element of their package
//     path (users.User and billing.User), then by the whole path with "/" written as ".";
//   - two types still alike (same package path and name, such as one type declared inside two
//     functions) are refused, because no stable name exists for them;
//   - the app's schema namer, when it has one, is asked once for each type with its package
//     path and the name above; a name it returns is the type's name, in every case and not only
//     on a collision, and the types it leaves alone (it returns "") follow the rules above among
//     themselves. A name outside A-Z a-z 0-9 . - _, one given to two types, or one that is the
//     name another type has without the namer, is an error;
//   - a type used in more than one mode whose schemas differ (a struct with an omitempty field
//     is optional in a response and required in a request) gets one component per mode, named
//     Name.Request, Name.RequestOpen or Name.Response; when the schemas are equal there is one.
func (r *Registry) Components() (map[string]Schema, error) {
	f, err := r.finalize()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Schema, len(f.comps))
	for k, v := range f.comps {
		out[k] = v
	}
	return out, nil
}

// Resolve returns a copy of s with every reference to a component replaced by its
// "#/components/schemas/Name" form.
func (r *Registry) Resolve(s Schema) (Schema, error) {
	f, err := r.finalize()
	if err != nil {
		return nil, err
	}
	return render(s, f.ref).(Schema), nil
}

// render resolves every reference of v: to a component's path, or, for a schema written in
// place, to that schema with the reference's sibling keywords (a description) kept over it.
func render(v any, ref func(key) (string, Schema)) any {
	switch x := v.(type) {
	case Schema:
		if r, ok := x["$ref"].(refTo); ok {
			if _, in := ref(r.k); in != nil {
				out := render(in, ref).(Schema)
				for k, e := range x {
					if k != "$ref" {
						out[k] = render(e, ref)
					}
				}
				return out
			}
		}
		out := make(Schema, len(x))
		for k, e := range x {
			if r, ok := e.(refTo); ok && k == "$ref" {
				out[k], _ = ref(r.k)
				continue
			}
			out[k] = render(e, ref)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = render(e, ref)
		}
		return out
	}
	return v
}

func (r *Registry) finalize() (*finalized, error) {
	if r.final != nil {
		return r.final, nil
	}
	keys := make([]key, 0, len(r.named))
	inline := map[key]Schema{}
	for k, e := range r.named {
		if r.shared[k.t] {
			inline[k] = e.schema
			continue
		}
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key) int {
		if c := strings.Compare(a.t.String(), b.t.String()); c != 0 {
			return c
		}
		return int(a.m) - int(b.m)
	})

	// Group the modes of one type, then split a group whose members differ, until stable.
	group := map[key]int{}
	byType := map[reflect.Type]int{}
	next := 0
	for _, k := range keys {
		g, ok := byType[k.t]
		if !ok {
			g = next
			next++
			byType[k.t] = g
		}
		group[k] = g
	}
	for changed := true; changed; {
		changed = false
		groupName := func(k key) string {
			if _, ok := inline[k]; ok {
				return fmt.Sprintf("inline %s %d", k.t, k.m)
			}
			return fmt.Sprintf("g%d", group[k])
		}
		members := map[int][]key{}
		for _, k := range keys {
			members[group[k]] = append(members[group[k]], k)
		}
		for _, g := range slices.Sorted(maps.Keys(members)) {
			ms := members[g]
			if len(ms) < 2 {
				continue
			}
			first := normal(r.named[ms[0]].schema, groupName)
			same := true
			for _, k := range ms[1:] {
				if normal(r.named[k].schema, groupName) != first {
					same = false
				}
			}
			if !same {
				for _, k := range ms[1:] {
					group[k] = next
					next++
				}
				changed = true
			}
		}
	}

	// Names per type, then per group.
	types := map[reflect.Type]bool{}
	for _, k := range keys {
		types[k.t] = true
	}
	// The app's namer is asked once per type, in a fixed order, and what it names is out of the
	// default rules: those then run over the types it left alone.
	typeName := map[reflect.Type]string{}
	chosen := map[string]reflect.Type{}
	if r.opt.Namer != nil {
		sorted := slices.SortedFunc(maps.Keys(types), func(a, b reflect.Type) int {
			return strings.Compare(r.goTypeOf(a), r.goTypeOf(b))
		})
		for _, t := range sorted {
			n := r.opt.Namer(r.pkgOf(t), r.shortOf(t))
			if n == "" {
				continue
			}
			if n != sanitize(n) {
				return nil, fmt.Errorf("openapi: the schema namer named %s %q, but a component name may use only A-Z a-z 0-9 . - and _", r.goTypeOf(t), n)
			}
			if prev, ok := chosen[n]; ok {
				return nil, fmt.Errorf("openapi: the schema namer gave %s and %s the same name %q, but each component needs its own", r.goTypeOf(prev), r.goTypeOf(t), n)
			}
			chosen[n], typeName[t] = t, n
		}
	}
	short := map[string][]reflect.Type{}
	for t := range types {
		if _, ok := typeName[t]; !ok {
			short[r.shortOf(t)] = append(short[r.shortOf(t)], t)
		}
	}
	for base, ts := range short {
		if len(ts) == 1 {
			typeName[ts[0]] = base
			continue
		}
		pkg := map[string][]reflect.Type{}
		for _, t := range ts {
			q := sanitize(lastElem(r.pkgOf(t))) + "." + base
			pkg[q] = append(pkg[q], t)
		}
		for q, ps := range pkg {
			if len(ps) == 1 {
				typeName[ps[0]] = q
				continue
			}
			full := map[string][]reflect.Type{}
			for _, t := range ps {
				n := sanitize(strings.ReplaceAll(r.pkgOf(t), "/", ".")) + "." + base
				full[n] = append(full[n], t)
			}
			for n, fs := range full {
				if len(fs) > 1 {
					return nil, fmt.Errorf("openapi: two different types are both named %s in package %q, so they cannot get distinct component names; give one of them another name", n, r.pkgOf(fs[0]))
				}
				typeName[fs[0]] = n
			}
		}
	}
	for _, t := range slices.SortedFunc(maps.Keys(types), func(a, b reflect.Type) int {
		return strings.Compare(r.goTypeOf(a), r.goTypeOf(b))
	}) {
		if prev, ok := chosen[typeName[t]]; ok && prev != t {
			return nil, fmt.Errorf("openapi: the schema namer named %s %q, which is the name %s has without it: name both or neither", r.goTypeOf(prev), typeName[t], r.goTypeOf(t))
		}
	}
	// A type with more than one group is split; count distinct groups.
	groupsOf := map[reflect.Type]map[int]bool{}
	for _, k := range keys {
		if groupsOf[k.t] == nil {
			groupsOf[k.t] = map[int]bool{}
		}
		groupsOf[k.t][group[k]] = true
	}
	f := &finalized{names: map[key]string{}, comps: map[string]Schema{}, inline: inline}
	used := map[string]key{}
	for _, k := range keys {
		n := typeName[k.t]
		if len(groupsOf[k.t]) > 1 {
			n += "." + k.m.String()
		}
		f.names[k] = n
	}
	// Members of one group share the name of the group's first key.
	first := map[int]string{}
	for _, k := range keys {
		g := group[k]
		if n, ok := first[g]; ok {
			f.names[k] = n
		} else {
			first[g] = f.names[k]
		}
	}
	for _, k := range keys {
		n := f.names[k]
		if prev, ok := used[n]; ok && group[prev] != group[k] {
			return nil, fmt.Errorf("openapi: component name %s is taken by two different schemas", n)
		}
		if _, ok := used[n]; ok {
			continue
		}
		used[n] = k
		f.comps[n] = render(r.named[k].schema, f.ref).(Schema)
	}
	r.final = f
	return f, nil
}

// An anonymous struct that is a request Body is named after the Input that holds it
// (CreateUser.Body is CreateUserBody), so a client has a component to import where it would
// otherwise find the shape inlined at every use.
type ident struct{ pkg, name string }

func (r *Registry) pkgOf(t reflect.Type) string {
	if id, ok := r.anon[t]; ok {
		return id.pkg
	}
	return t.PkgPath()
}

func (r *Registry) shortOf(t reflect.Type) string {
	if id, ok := r.anon[t]; ok {
		return id.name
	}
	return shortName(t)
}

func (r *Registry) goTypeOf(t reflect.Type) string {
	if id, ok := r.anon[t]; ok {
		return id.pkg + "." + id.name
	}
	return goType(t)
}

// goType says which Go type a message means: its package path and name.
func goType(t reflect.Type) string {
	if t.PkgPath() == "" {
		return t.String()
	}
	return t.PkgPath() + "." + t.Name()
}

func normal(s Schema, name func(key) string) string {
	b, _ := json.Marshal(render(s, func(k key) (string, Schema) { return name(k), nil }))
	return string(b)
}

func lastElem(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// shortName is the type name without its package, with a generic instance's type arguments
// reduced to their own short names: Page[example.com/users.User] is Page_User.
func shortName(t reflect.Type) string {
	n := t.Name()
	var parts []string
	for tok := range strings.FieldsFuncSeq(n, func(r rune) bool { return r == '[' || r == ']' || r == ',' }) {
		tok = lastElem(tok)
		tok = strings.TrimLeft(tok, "*")
		if i := strings.LastIndexByte(tok, '.'); i >= 0 {
			tok = tok[i+1:]
		}
		if tok != "" {
			parts = append(parts, sanitize(tok))
		}
	}
	return strings.Join(parts, "_")
}

// sanitize keeps the characters OpenAPI allows in a component name.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, s)
}
