package bites

import (
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/wire"
)

// Registration reads an Input type once and turns it into a plan: for each section, its fields'
// wire names, offsets and compiled decoders (ADR 0007). Everything in this file runs when a
// route is added; nothing here runs per request. Every mistake is returned as a refusal whose
// text is the reason, and register adds the route and the file and line.

const (
	maxSectionFields = 64 // the seen-mask of a section is one machine word
	maxLists         = 8  // lists in one text section, counted on the stack while decoding
)

type secKind uint8

const (
	secPath secKind = iota
	secQuery
	secHeader
	secCookie
	secForm      // the fields of a Form[T] body (ADR 0044)
	secMultipart // the fields and files of a Multipart[T] body (ADR 0044)
)

var secNames = [...]string{"path", "query", "header", "cookie", "body", "body"}

// textField is one field of a Path, Query, Header or Cookie section.
type textField struct {
	goPath   string // GetUser.Query.Page, for messages
	wire     string // the name a client uses
	loc      string // query.page, the location in a 400
	off      uintptr
	dec      wire.TextDecoder
	required bool
	list     int // -1 for a scalar, else the index in the section's lists
	sliceT   reflect.Type
	elemSize uintptr
	listOff  uintptr // where the slice header is, relative to the section
	flagOff  uintptr // Opt[[]T]: the presence flag; valid when hasFlag
	hasFlag  bool
	csv      bool          // a CSV: one value split at commas, not repeated keys
	items    *wire.Items   // a list: its type's Items bound (ADR 0021), nil when it has none
	typ      reflect.Type  // the type the text holds, for the API description: the element's for an Opt, the slice for a list
	file     *fileSpec     // Multipart: the field holds files, not text; dec is nil
	hasDef   bool          // the section's Defaults names this field (ADR 0041)
	def      string        // the default as a client would send it, decoded again on each request that omits the field
	defVal   reflect.Value // the default decoded once at Registration, for the API description only
}

type textSection struct {
	kind    secKind
	off     uintptr // of the section inside the Input
	fields  []textField
	byName  map[string]int // lower-cased for a header, as written otherwise
	keys    []string       // byName's keys in field order, scanned instead of the map when few
	lists   int
	typ     reflect.Type // the section's struct, for the API description
	goPath  string       // GetUser.Query, for messages
	pathMap []int        // Path: the field for each wildcard of the pattern, in pattern order
	unknown bool         // Form: a field the type does not have is accepted (AcceptUnknownMembers)
	strict  bool         // Query: a key the type does not have is a 400 (RejectUnknownQueryKeys)
	// elsewhere are the query keys the route's Providers read, which a strict Query lets through.
	elsewhere []string
}

// lookup finds the field whose key is name. A section of a few fields is scanned, which is
// cheaper than hashing the name (docs/research/hotpath-mac); a larger one uses the map.
func (s *textSection) lookup(name []byte) (int, bool) {
	if len(s.keys) <= 8 {
		for i, k := range s.keys {
			if k == string(name) {
				return i, true
			}
		}
		return 0, false
	}
	i, ok := s.byName[string(name)]
	return i, ok
}

type bodyKind uint8

const (
	bodyJSON bodyKind = iota
	bodyRaw
	bodyBytes
	bodyForm      // Form[T]: see form.go
	bodyUpload    // an Upload: read by the handler, part by part (ADR 0044)
	bodyMultipart // Multipart[T], or a Submitted[T] with a file field: see multipart.go
)

type bodyPlan struct {
	kind   bodyKind
	off    uintptr
	dec    *jsoncodec.Decoder
	rawOff uintptr      // Raw[T]: where the exact bytes are kept
	valOff uintptr      // Raw[T]: where the decoded value is
	form   *textSection // Form[T]: the fields, compiled like a query section
	sub    bool         // Submitted[T]: a Form whose mistakes the handler shows (ADR 0078)
	subOff uintptr      // Submitted[T]: where its submission pointer is
	opt    bool         // Opt[T]: a request with no body leaves it unset (ADR 0044)
	optOff uintptr      // Opt[T]: where its presence flag is
}

// inputPlan is everything known about an Input at Registration.
type inputPlan struct {
	path, query, header, cookie *textSection
	body                        *bodyPlan
	provs                       []provStep // the Providers the route runs, in order; nil when none
	validates                   bool       // the Input has a Validate() error (ADR 0021)
}

type compiler struct {
	a      *App
	method string
	inName string // GetUser
	// inValidates is whether the Input has a Validate method, where a validator library may read
	// the validate tags that are otherwise refused (refuseForeignTag).
	inValidates bool
	params      []string
	cfg         routeConfig

	provDefs []*providerDef // the Providers the route runs, in order, for the API document (ADR 0069)
	provSecs []*textSection // the Header and Cookie sections the route's Providers read, for the API document (ADR 0023)

	// browserOpened is a route a browser opens with no header of its own (a WebSocket, an
	// EventSource), where a Provider may read a Query section; provQueryKeys are its keys, which a
	// strict Input query lets through.
	browserOpened bool
	provQueryKeys []string
}

// eventStream reports whether t is an answer a browser reads with EventSource: SSE or Events[T].
func eventStream(t reflect.Type) bool {
	return t.PkgPath() == reflect.TypeFor[SSE]().PkgPath() && (t.Name() == "SSE" || strings.HasPrefix(t.Name(), "Events["))
}

func typeLabel(t reflect.Type) string {
	if n := t.Name(); n != "" {
		return n
	}
	return t.String()
}

// patternParams lists the named wildcards of a pattern in order: {id} is "id" and {rest...} is
// "rest". {$} and a trailing slash name nothing.
func patternParams(p string) []string {
	var out []string
	for {
		i := strings.IndexByte(p, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(p[i:], '}')
		if j < 0 {
			return out
		}
		name := p[i+1 : i+j]
		p = p[i+j+1:]
		if name == "$" {
			continue
		}
		out = append(out, strings.TrimSuffix(name, "..."))
	}
}

var bodyless = map[string]bool{"GET": true, "HEAD": true, "DELETE": true}

// compileInput builds the plan for the Input type t of a route (ADR 0010).
func (c *compiler) compileInput(t reflect.Type) (*inputPlan, error) {
	if t.Kind() == reflect.Pointer {
		return nil, refusalf("the handler takes %s, a pointer; an Input is taken by value, because a pointer cannot say whether nil meant absent or null (use Opt[T] or Null[T] for a field)", t)
	}
	if t.Kind() != reflect.Struct {
		return nil, refusalf("the handler's input must be a struct whose fields are the sections Path, Query, Header, Cookie and Body, not %s", t)
	}
	c.inName = typeLabel(t)
	c.inValidates = hasValidate(t)
	fields, err := c.inputFields(t)
	if err != nil {
		return nil, err
	}
	p := &inputPlan{}
	var provided []providedField
	for _, f := range fields {
		where := c.inName + "." + f.Name
		if !f.IsExported() {
			return nil, refusalf("%s is unexported, so nothing can fill it", where)
		}
		switch f.Name {
		case "Path", "Query", "Header", "Cookie", "Body":
			if hasValidate(f.Type) {
				return nil, refusalf("%s has a Validate method, but only the Input's Validate runs: move the checks to %s.Validate", where, c.inName)
			}
		}
		switch f.Name {
		case "Path", "Query", "Header", "Cookie":
			kind := map[string]secKind{"Path": secPath, "Query": secQuery, "Header": secHeader, "Cookie": secCookie}[f.Name]
			if f.Type.Kind() == reflect.Pointer {
				return nil, refusalf("%s is a pointer; a section is a struct taken by value, and a pointer cannot say whether nil meant absent or null", where)
			}
			if f.Type.Kind() != reflect.Struct {
				return nil, refusalf("%s must be a struct, not %s", where, f.Type)
			}
			s, err := c.section(kind, where, f.Type, f.Offset)
			if err != nil {
				return nil, err
			}
			switch kind {
			case secPath:
				p.path = s
			case secQuery:
				p.query = s
			case secHeader:
				p.header = s
			default:
				p.cookie = s
			}
		case "Body":
			if bodyless[c.method] && isMultipart(f.Type) {
				return nil, refusalf("%s is a Multipart on a %s route; a body is sent with POST, PUT or PATCH", where, c.method)
			}
			if bodyless[c.method] {
				return nil, refusalf("%s is a Body section, but a %s route has no request body", where, c.method)
			}
			b, err := c.body(where, f.Type, f.Offset)
			if err != nil {
				return nil, err
			}
			if b.kind == bodyUpload && !c.cfg.maxBodySet {
				return nil, refusalf("%s is an Upload, but the route has no MaxBody: how much a route may receive is the application's decision (an avatar wants 2 MiB, a video 2 GiB), so there is no default; give MaxBody(n) to the route or to a group above it", where)
			}
			p.body = b
		default:
			provided = append(provided, providedField{goPath: where, t: f.Type, off: f.Offset})
		}
	}
	if c.cfg.unknownQuery == queryReject || (c.cfg.unknownQuery == queryInherit && c.a.rejectUnknownQuery) {
		if p.query == nil {
			// A route without a Query section takes no query: an empty section makes the same
			// check, so a misspelled key is no more welcome here than where it has neighbours.
			p.query = &textSection{kind: secQuery, byName: map[string]int{}, typ: reflect.TypeFor[struct{}]()}
		}
		p.query.strict = true
	}
	if p.body != nil && (p.body.kind == bodyMultipart || p.body.kind == bodyUpload) {
		other := containsUpload
		if p.body.kind == bodyUpload {
			other = containsMultipart
		}
		for _, f := range provided {
			if other(f.t, map[reflect.Type]bool{}) {
				return nil, refusalf("%s holds a Multipart and an Upload; a request body is read one way", typeLabel(t))
			}
		}
	}
	if err := c.matchPattern(p); err != nil {
		return nil, err
	}
	steps, err := c.planProviders(provided)
	if err != nil {
		return nil, err
	}
	p.provs = steps
	if p.query != nil && p.query.strict {
		p.query.elsewhere = c.provQueryKeys
	}
	v, err := validateOf(t)
	if err != nil {
		return nil, err
	}
	p.validates = v
	return p, nil
}

// inputFields is the fields of the Input t, with each embedded struct of sections replaced by its
// sections, their offsets counted from the Input, so twenty routes can share one Path section by
// embedding it. An embedded struct that holds no section is a field like any other (a type a
// Provider gives); one that holds sections and other fields is refused, as is a section that two
// embedded structs, or one and the Input, both give.
func (c *compiler) inputFields(t reflect.Type) ([]reflect.StructField, error) {
	var out []reflect.StructField
	from := map[string]string{} // section name -> the struct that gave it
	var walk func(t reflect.Type, off uintptr, label string) error
	walk = func(t reflect.Type, off uintptr, label string) error {
		for i := range t.NumField() {
			f := t.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				if has, other := sectionsOf(f.Type); has {
					inner := typeLabel(f.Type)
					if other != "" {
						return refusalf("%s embeds %s, which holds sections and also %s, which is not one: an embedded struct of an Input holds only sections (Path, Query, Header, Cookie, Body); give %s to %s itself", c.inName, inner, other, other, c.inName)
					}
					if err := walk(f.Type, off+f.Offset, inner); err != nil {
						return err
					}
					continue
				}
			}
			if isSectionName(f.Name) {
				if prev, ok := from[f.Name]; ok {
					return refusalf("%s gets a %s section from both %s and %s, and Go would read one and hide the other: keep one", c.inName, f.Name, prev, label)
				}
				from[f.Name] = label
			}
			f.Offset += off
			out = append(out, f)
		}
		return nil
	}
	if err := walk(t, 0, c.inName); err != nil {
		return nil, err
	}
	return out, nil
}

func isSectionName(n string) bool {
	return n == "Path" || n == "Query" || n == "Header" || n == "Cookie" || n == "Body"
}

// sectionsOf reports whether the struct t holds a section, through its own embedded structs too,
// and the name of a field it holds that is not one.
func sectionsOf(t reflect.Type) (has bool, other string) {
	for i := range t.NumField() {
		f := t.Field(i)
		switch {
		case isSectionName(f.Name):
			has = true
		case f.Anonymous && f.Type.Kind() == reflect.Struct:
			h, o := sectionsOf(f.Type)
			has = has || h
			if o != "" && other == "" {
				other = o
			}
			if !h && other == "" {
				other = f.Name
			}
		case other == "":
			other = f.Name
		}
	}
	return has, other
}

// matchPattern checks the Path section against the pattern's wildcards, both ways (ADR 0007):
// a {name} with no field would run the handler with a zero, and a field with no {name} would
// never be filled.
func (c *compiler) matchPattern(p *inputPlan) error {
	if p.path == nil {
		if len(c.params) > 0 {
			return refusalf("the pattern has {%s}, but %s has no Path section", c.params[0], c.inName)
		}
		return nil
	}
	s := p.path
	s.pathMap = make([]int, len(c.params))
	for pi, name := range c.params {
		idx, ok := s.byName[name]
		if !ok {
			return refusalf("the pattern has {%s}, but %s.Path has no field for it", name, c.inName)
		}
		s.pathMap[pi] = idx
	}
	for _, f := range s.fields {
		found := false
		for _, name := range c.params {
			if name == f.wire {
				found = true
			}
		}
		if !found {
			return refusalf("%s has no {%s} in the pattern", f.goPath, f.wire)
		}
	}
	return nil
}

type wireNamer interface{ WireNames() map[string]string }

// wireNamesOf is the WireNames method of a struct type, if its method set has one, promoted or not.
func wireNamesOf(t reflect.Type) map[string]string {
	if t.Kind() != reflect.Struct {
		return nil
	}
	if wn, ok := reflect.New(t).Interface().(wireNamer); ok {
		return wn.WireNames()
	}
	return nil
}

// declaredMap is what a map-returning method of t (WireNames, Descriptions) says about t itself:
// nil when t has no such method, or only one promoted from an embedded struct. Promotion hides
// the difference from the method set, and a promoted method would be read as the outer struct's
// own, so the two are told apart by what the method returns: a map equal to the one an embedded
// struct's method returns is that struct's. When t does declare a method returning exactly that
// map, reading it as the embedded one's gives the same entries and accepts the same keys (every
// field of the embedded struct is a field of t), so the answer cannot differ, and nothing about
// how the compiler builds a promoted method is relied on (ADR 0011, ADR 0020).
func declaredMap(t reflect.Type, get func(reflect.Type) map[string]string) map[string]string {
	own := get(t)
	if own == nil || t.Kind() != reflect.Struct {
		return own
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.Anonymous || f.Type.Kind() != reflect.Struct {
			continue
		}
		if sub := get(f.Type); sub != nil && maps.Equal(own, sub) {
			return nil
		}
	}
	return own
}

// checkWireNames refuses a WireNames key that names no field of the struct whose method gave it.
func checkWireNames(t reflect.Type, names map[string]string, goPath string) error {
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if _, ok := t.FieldByName(name); !ok {
			return refusalf("%s.WireNames names %s, which is not a field", goPath, name)
		}
	}
	return nil
}

// section compiles one text section: Path, Query, Header or Cookie (ADR 0042). Each struct in it,
// the section's own and every embedded one, may rename its own fields with WireNames, and a
// struct's names win over those of the structs it embeds: the outer struct is the route's view.
func (c *compiler) section(kind secKind, goPath string, t reflect.Type, off uintptr) (*textSection, error) {
	s := &textSection{kind: kind, off: off, byName: map[string]int{}, typ: t, goPath: goPath}
	override := declaredMap(t, wireNamesOf)
	if err := checkWireNames(t, override, goPath); err != nil {
		return nil, err
	}
	var walk func(t reflect.Type, base uintptr, goPath string, override map[string]string) error
	walk = func(t reflect.Type, base uintptr, goPath string, override map[string]string) error {
		for i := range t.NumField() {
			f := t.Field(i)
			fpath := goPath + "." + f.Name
			if f.Anonymous && f.Type.Kind() == reflect.Pointer {
				return refusalf("%s is an embedded pointer, which cannot be inlined; embed the struct by value", fpath)
			}
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				if k, _, _, _ := wire.PresenceOf(f.Type); k == wire.None {
					inner := declaredMap(f.Type, wireNamesOf)
					if err := checkWireNames(f.Type, inner, fpath); err != nil {
						return err
					}
					merged := maps.Clone(inner)
					if merged == nil {
						merged = map[string]string{}
					}
					maps.Copy(merged, override)
					if err := walk(f.Type, base+f.Offset, fpath, merged); err != nil {
						return err
					}
					continue
				}
			}
			if !f.IsExported() {
				return refusalf("%s is unexported, so nothing can fill it", fpath)
			}
			if err := c.refuseForeignTag(fpath, f.Tag); err != nil {
				return err
			}
			name, ok := override[f.Name]
			switch {
			case ok:
			case kind == secHeader:
				name = wire.HeaderName(f.Name)
			default:
				name = c.a.naming(f.Name)
			}
			if err := refuseJSONRename(fpath, kind, f.Tag, name); err != nil {
				return err
			}
			tf, err := c.textField(kind, fpath, f.Type, base+f.Offset, name)
			if err != nil {
				return err
			}
			key := name
			if kind == secHeader {
				key = strings.ToLower(name)
			}
			if prev, dup := s.byName[key]; dup {
				return refusalf("%s and %s both have the wire name %q", s.fields[prev].goPath, fpath, name)
			}
			if tf.list >= 0 {
				if s.lists == maxLists && kind == secMultipart {
					return refusalf("%s has more than %d lists", fpath, maxLists)
				}
				if s.lists == maxLists {
					return refusalf("%s: a section can hold at most %d lists", goPath, maxLists)
				}
				tf.list = s.lists
				s.lists++
			}
			if len(s.fields) == maxSectionFields {
				return refusalf("%s has more than %d fields", goPath, maxSectionFields)
			}
			s.byName[key] = len(s.fields)
			s.keys = append(s.keys, key)
			s.fields = append(s.fields, tf)
		}
		return nil
	}
	if err := walk(t, 0, goPath, override); err != nil {
		return nil, err
	}
	if err := sectionDefaults(s); err != nil {
		return nil, err
	}
	return s, nil
}

// defaulter is the optional method of a Section's struct that states what an omitted field
// means (ADR 0041).
type defaulter interface{ Defaults() map[string]string }

var defaulterType = reflect.TypeFor[defaulter]()

// fieldDefaults is the Defaults method of a struct type, if its method set has one.
func fieldDefaults(t reflect.Type) map[string]string {
	if t.Kind() != reflect.Struct {
		return nil
	}
	if d, ok := reflect.New(t).Interface().(defaulter); ok {
		return d.Defaults()
	}
	return nil
}

// sectionDefaults reads the Defaults methods of a text section's struct and of every struct it
// embeds (ADR 0041): each embedded struct's own method states the defaults of its own fields, and
// the section struct's own method wins for a field both name, as Descriptions and WireNames do.
// Each entry is decoded here by the field's own text decoder, so a default its type refuses is
// refused at Registration, and the same decoder fills the field on every request that omits it.
func sectionDefaults(s *textSection) error {
	var collect func(t reflect.Type, prefix, label string) (map[int]string, error)
	collect = func(t reflect.Type, prefix, label string) (map[int]string, error) {
		merged := map[int]string{}
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.Anonymous || f.Type.Kind() != reflect.Struct {
				continue
			}
			if k, _, _, _ := wire.PresenceOf(f.Type); k != wire.None {
				continue
			}
			sub, err := collect(f.Type, prefix+"."+f.Name, typeLabel(f.Type))
			if err != nil {
				return nil, err
			}
			maps.Copy(merged, sub)
		}
		own := declaredMap(t, fieldDefaults)
		if len(own) == 0 {
			return merged, nil
		}
		var names []string
		for _, f := range s.fields {
			if rest, ok := strings.CutPrefix(f.goPath, prefix+"."); ok {
				names = append(names, lastName(rest))
			}
		}
		for _, k := range sortedKeys(own) {
			idx := -1
			for i, f := range s.fields {
				if f.goPath == prefix+"."+k {
					idx = i
					break
				}
			}
			if idx < 0 {
				for i, f := range s.fields {
					if strings.HasPrefix(f.goPath, prefix+".") && lastName(f.goPath) == k {
						idx = i
						break
					}
				}
			}
			if idx < 0 {
				return nil, refusalf("%s.Defaults names %s, which is not a field of %s (its fields: %s)", label, k, typeLabel(t), strings.Join(names, ", "))
			}
			merged[idx] = own[k]
		}
		return merged, nil
	}
	label := sectionLabel(s)
	chosen, err := collect(s.typ, label, label)
	if err != nil {
		return err
	}
	for _, idx := range slices.Sorted(maps.Keys(chosen)) {
		f := &s.fields[idx]
		text := chosen[idx]
		switch {
		case s.kind == secPath:
			return refusalf("%s has a default, but a path parameter is always present", f.goPath)
		case f.list >= 0 || f.csv:
			return refusalf("%s is a list and has a default: a list default would be one slice shared by every request or a copy made for each, so a list has none", f.goPath)
		case !f.required:
			return refusalf("%s is an Opt and has a default: a default removes the absence an Opt exists to report, so make it a plain %s", f.goPath, typeLabel(f.typ))
		}
		v := reflect.New(f.typ)
		if err := f.dec(v.UnsafePointer(), text); err != nil {
			return refusalf("%s has the default %q, which its own type refuses: %v", f.goPath, text, err)
		}
		f.hasDef, f.def, f.defVal, f.required = true, text, v.Elem(), false
	}
	return nil
}

// refuseBodyDefaults refuses a Defaults method anywhere in a type read as JSON (ADR 0041): the
// codec does not read it, and a method that is accepted and ignored would leave a field required
// that its author wrote a default for.
func refuseBodyDefaults(goPath string, t reflect.Type, seen map[reflect.Type]bool) error {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return nil
	}
	seen[t] = true
	if reflect.PointerTo(t).Implements(defaulterType) {
		return refusalf("%s: %s has a Defaults method, but a JSON Body does not read it: use Opt[T] and Or in the handler, or a Form[T], whose fields are read like a Query", goPath, typeLabel(t))
	}
	for i := range t.NumField() {
		if f := t.Field(i); f.IsExported() {
			if err := refuseBodyDefaults(goPath, f.Type, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

// textField reads one field's type. A scalar is decoded by wire.CompileText; a slice is filled
// from repeated keys, with its element compiled the same way (ADR 0042).
func (c *compiler) textField(kind secKind, fpath string, t reflect.Type, off uintptr, name string) (textField, error) {
	tf := textField{goPath: fpath, wire: name, off: off, list: -1, required: true}
	tf.loc = secNames[kind] + "." + name
	if kind == secMultipart {
		if ft, ok, err := c.fileField(tf, t, off); ok {
			return ft, err
		}
	} else if holdsFile(t, map[reflect.Type]bool{}) {
		where := map[secKind]string{secPath: "Path", secQuery: "Query", secHeader: "Header", secCookie: "Cookie", secForm: "Form"}[kind]
		if kind == secForm {
			// The Body of a Form is named as the handler reads it: in.Body.Value.Cover.
			if i := strings.LastIndexByte(fpath, '.'); i >= 0 {
				fpath = fpath[:i] + ".Value" + fpath[i:]
			}
			return tf, refusalf("%s is a FormFile in a Form; a Form is read as application/x-www-form-urlencoded and cannot carry a file, so make the Body a Multipart", fpath)
		}
		return tf, refusalf("%s is a FormFile in a %s; a file is a part of a multipart body, so make the Body a Multipart", fpath, where)
	}
	pk, elem, vOff, fOff := wire.PresenceOf(t)
	inner := t
	switch pk {
	case wire.Nullable:
		return tf, refusalf("%s is a Null; text has no null, so the type would promise a state no request can produce: use Opt[T] for a value that may be missing", fpath)
	case wire.Optional:
		if k, _, _, _ := wire.PresenceOf(elem); k != wire.None {
			return tf, refusalf("%s: an Opt of %s is not allowed in a text section; text has no null", fpath, typeLabel(elem))
		}
		tf.required = false
		inner = elem
	}
	if kind == secPath && pk == wire.Optional {
		return tf, refusalf("%s is an Opt, but a path parameter is always present", fpath)
	}
	if wire.CSVOf(inner) && !implementsText(inner) {
		return c.csvField(kind, tf, inner, pk, off, vOff, fOff)
	}
	if inner.Kind() == reflect.Slice && !implementsText(inner) {
		if kind == secPath {
			return tf, refusalf("%s is a list, but a path parameter holds one value", fpath)
		}
		et := inner.Elem()
		if k, _, _, _ := wire.PresenceOf(et); k != wire.None {
			return tf, refusalf("%s: a list element cannot be %s, absence means nothing there", fpath, typeLabel(et))
		}
		dec, err := wire.CompileTextWith(et, c.a.formats)
		if err != nil {
			return tf, refusalf("%s: %v", fpath, err)
		}
		c.noteFormatless(et, fpath)
		it, hasItems, err := wire.ItemsOf(inner)
		if err != nil {
			return tf, refusalf("%s: %v", fpath, err)
		}
		if hasItems {
			tf.items = &it
		}
		if _, _, err := wire.MembersOf(inner); err != nil {
			return tf, refusalf("%s: %v", fpath, err)
		}
		tf.dec, tf.sliceT, tf.elemSize, tf.typ = dec, inner, et.Size(), inner
		tf.list = 0 // assigned by the section
		tf.listOff = off
		if pk == wire.Optional {
			tf.listOff, tf.flagOff, tf.hasFlag = off+vOff, off+fOff, true
		}
		return tf, nil
	}
	dec, err := wire.CompileTextWith(t, c.a.formats)
	if err != nil {
		return tf, refusalf("%s: %v", fpath, err)
	}
	c.noteFormatless(inner, fpath)
	tf.dec, tf.typ = dec, inner
	return tf, nil
}

// noteFormatless records a text field whose type is a uuid of a package other than the standard
// library's, which the core reads as a plain TextUnmarshaler: a value that does not decode is told
// "expected a valid value" and the document says string with no format. The app says so once per
// type when it starts, with the option that fixes it (WithTextFormat), since nothing else would.
func (c *compiler) noteFormatless(t reflect.Type, fpath string) {
	if t.Name() != "UUID" || t.PkgPath() == "uuid" || !strings.Contains(t.PkgPath(), "uuid") {
		return
	}
	if _, ok := c.a.formats[t]; ok {
		return
	}
	c.a.formatless.LoadOrStore(t, c.method+" "+fpath)
}

// csvField compiles a comma-separated list (ADR 0042). It is decoded as one value, so it takes
// the scalar path for presence and repeats (a second key is "sent more than once"); only its
// storage is a list.
func (c *compiler) csvField(kind secKind, tf textField, t reflect.Type, pk wire.PresenceKind, off, vOff, fOff uintptr) (textField, error) {
	if kind != secQuery && kind != secHeader {
		where := map[secKind]string{secPath: "Path", secCookie: "Cookie", secForm: "Form", secMultipart: "Multipart"}[kind]
		return tf, refusalf("%s is a comma-separated list, which only a Query or a Header section reads; a %s holds one value, and a repeated field is a plain []%s", tf.goPath, where, typeLabel(t.Elem()))
	}
	et := t.Elem()
	if k, _, _, _ := wire.PresenceOf(et); k != wire.None {
		return tf, refusalf("%s: a list element cannot be %s, absence means nothing there", tf.goPath, typeLabel(et))
	}
	dec, err := wire.CompileTextWith(et, c.a.formats)
	if err != nil {
		return tf, refusalf("%s: %v", tf.goPath, err)
	}
	c.noteFormatless(et, tf.goPath)
	it, hasItems, err := wire.ItemsOf(t)
	if err != nil {
		return tf, refusalf("%s: %v", tf.goPath, err)
	}
	if hasItems {
		tf.items = &it
	}
	if _, _, err := wire.MembersOf(t); err != nil {
		return tf, refusalf("%s: %v", tf.goPath, err)
	}
	tf.dec, tf.sliceT, tf.elemSize, tf.typ, tf.csv = dec, t, et.Size(), t, true
	tf.listOff = off
	if pk == wire.Optional {
		tf.listOff, tf.flagOff, tf.hasFlag = off+vOff, off+fOff, true
	}
	return tf, nil
}

var textUnmarshalerType = reflect.TypeFor[interface{ UnmarshalText([]byte) error }]()

func implementsText(t reflect.Type) bool {
	return t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(textUnmarshalerType)
}

var (
	bytesType = reflect.TypeFor[[]byte]()
	ownPkg    = reflect.TypeFor[Raw[int]]().PkgPath()
)

// Raw is a Body that keeps the exact bytes beside the decoded value (ADR 0044), for a webhook
// whose signature covers the bytes as sent: re-encoding Value does not reproduce them. The
// handler checks Bytes against the signature with the secret its struct holds.
type Raw[T any] struct {
	// Value is the request body decoded into T.
	Value T
	raw   []byte
}

// Bytes is the request body exactly as it arrived. It is not modified afterwards and not
// reused for another request (ADR 0008).
func (r Raw[T]) Bytes() []byte { return r.raw }

func isOwn(t reflect.Type, prefix string) bool {
	return t.Kind() == reflect.Struct && t.PkgPath() == ownPkg && strings.HasPrefix(t.Name(), prefix)
}

// body compiles the Body section; its type decides how the request body is read (ADR 0044).
func (c *compiler) body(goPath string, t reflect.Type, off uintptr) (*bodyPlan, error) {
	if o := findOneOf(t, map[reflect.Type]bool{}); o != nil {
		return nil, refusalf("%s holds a %s; a OneOf is a handler's return type, not a request body", goPath, oneOfName(o))
	}
	opts := jsoncodec.Options{Formats: c.a.formats, JSONSchemas: c.a.jsonSchemas, Names: c.a.naming, AllowUnknown: c.cfg.unknown, MaxDepth: c.a.limits.MaxDepth}
	compile := func(dt reflect.Type) (*jsoncodec.Decoder, error) {
		if err := refuseBodyDefaults(goPath, dt, map[reflect.Type]bool{}); err != nil {
			return nil, err
		}
		if err := c.refuseForeignTagsIn(goPath, dt, map[reflect.Type]bool{}); err != nil {
			return nil, err
		}
		d, err := jsoncodec.CompileDecoder(dt, opts)
		if err != nil {
			msg := err.Error()
			if rest, ok := strings.CutPrefix(msg, typeLabel(dt)); ok && dt == t {
				msg = goPath + rest
			}
			return nil, refusalf("%s", msg)
		}
		return d, nil
	}
	if k, elem, vOff, fOff := wire.PresenceOf(t); k == wire.Optional {
		switch elem.Kind() {
		case reflect.Struct, reflect.Slice, reflect.Map, reflect.Array:
		default:
			return nil, refusalf("%s is Opt[%s]; an Opt Body is a JSON body that may be absent, so it holds a struct, slice or map", goPath, typeLabel(elem))
		}
		b, err := c.body(goPath, elem, off+vOff)
		if err != nil {
			return nil, err
		}
		if b.kind != bodyJSON {
			return nil, refusalf("%s is Opt[%s]; an Opt Body is a JSON body that may be absent, and a %[2]s is a Body of its own: give it without Opt", goPath, typeLabel(elem))
		}
		b.opt, b.optOff = true, off+fOff
		return b, nil
	}
	switch {
	case t == bytesType:
		return &bodyPlan{kind: bodyBytes, off: off}, nil
	case isMultipart(t), isSubmitted(t) && submittedHasFile(t):
		return c.multipartBody(goPath, t, off)
	case isOwn(t, "Form["), isSubmitted(t):
		return c.formBody(goPath, t, off)
	case t == uploadType:
		return &bodyPlan{kind: bodyUpload, off: off}, nil
	case containsUpload(t, map[reflect.Type]bool{}):
		return nil, refusalf("%s holds an Upload; an Upload is the whole Body, read part by part, not a field of a JSON body", goPath)
	case containsMultipart(t, map[reflect.Type]bool{}):
		return nil, refusalf("%s holds a Multipart; a Multipart is the whole Body, read as a form, not a field of a JSON body", goPath)
	case isOwn(t, "Raw["):
		d, err := compile(t.Field(0).Type)
		if err != nil {
			return nil, err
		}
		return &bodyPlan{kind: bodyRaw, off: off, dec: d, valOff: t.Field(0).Offset, rawOff: t.Field(1).Offset}, nil
	}
	if p := firstFile(goPath, t, map[reflect.Type]bool{}); p != "" {
		return nil, refusalf("%s is a FormFile in a JSON body; a file is a part of a multipart body, so make the Body a Multipart", p)
	}
	switch t.Kind() {
	case reflect.Struct, reflect.Slice, reflect.Map, reflect.Array:
		d, err := compile(t)
		if err != nil {
			return nil, err
		}
		return &bodyPlan{kind: bodyJSON, off: off, dec: d}, nil
	}
	return nil, refusalf("%s is %s; a Body is a struct, slice or map (read as JSON), []byte, Raw[T], Form[T], Multipart[T], Submitted[T] or Upload", goPath, t)
}
