package bites

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"unsafe"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/router"
	"github.com/nevindra/bites/internal/wire"
)

// Providers (ADR 0023). A Provider is a function registered for a type T on the app, a group or a
// route. An Input field of type T that is not a section is filled by it, and a Provider may need
// other provided types through the fields of its third parameter, a struct. Everything about the
// graph is decided at Registration: which Provider each type resolves to on a route (nearest scope
// first, Override replacing an outer one), which run, and in which order. A request runs each
// Provider its route needs exactly once, in that order, after the group and route middleware and
// before the Input is decoded (ADR 0048).
//
// The values live on the stack. Each Provider's run is a frame that keeps its value while the next
// Provider and finally the handler run below it, and the frame passes the value on by pointer, so
// a route with Providers allocates nothing more than one without (ADR 0003). The pointers are to
// stack frames that are still running and are never stored anywhere else.

// maxProviders is the most Providers one route runs; their values are reached through a fixed
// array on the stack.
const maxProviders = 16

// Provide registers fn as the Provider of T for a route, or for every route of a group when it is
// given to Group (ADR 0023, ADR 0048). It is the option spelling of App.Provide and Group.Provide.
//
// fn receives the request view and its dependencies: D is a struct whose fields are other provided
// types, filled before fn runs, or struct{} when it needs none:
//
//	func(ctx context.Context, req bites.Request, deps struct{ Session Session }) (User, error)
//
// An error from fn is the answer (a 401 or a 403, ADR 0049), and the handler does not run. A
// second Provider of T where one already covers the scope is refused; Override replaces one.
func Provide[T, D any](fn func(context.Context, Request, D) (T, error)) RouteOption {
	def, err := newProvider(fn, false, callerWhere())
	return provideOption(def, err)
}

// Override replaces the Provider of T that an enclosing scope registered, for this route or group
// and everything inside it (ADR 0023): a fake User in a test, or a subtree that authenticates
// differently. With nothing to replace it is refused. The Providers that need T, wherever they
// were registered, receive the replacement on the routes it covers.
func Override[T, D any](fn func(context.Context, Request, D) (T, error)) RouteOption {
	def, err := newProvider(fn, true, callerWhere())
	return provideOption(def, err)
}

// Provide registers fn as the Provider of T for every route of the app (see the function Provide).
// It must come before the app's first route and first group: a route's Providers are checked at
// its own line, so a later Provide would be missing there.
func (a *App) Provide[T, D any](fn func(context.Context, Request, D) (T, error)) {
	where := callerWhere()
	def, err := newProvider(fn, false, where)
	if err != nil {
		panic(registrationFailure("app", err, where))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.serving {
		panic(&RegistrationError{msg: "app: Provide registered at " + where + " after the app started serving: Providers can be added only before it does"})
	}
	if a.sealed != "" {
		panic(&RegistrationError{msg: "app: Provide is called after " + a.sealed + " was registered, which was checked without it: move Provide above it\n\tregistered at " + where})
	}
	opt := provideOption(def, nil)
	var scratch routeConfig
	if err := applyOptions(&scratch, append(a.provOpts[:len(a.provOpts):len(a.provOpts)], opt)); err != nil {
		panic(registrationFailure("app", err, where))
	}
	a.provOpts = append(a.provOpts, opt)
}

// Provide registers fn as the Provider of T for every route of this group and of the groups made
// in it (see the function Provide). Like Use it must come before the group's first route and
// first child group.
func (g *Group) Provide[T, D any](fn func(context.Context, Request, D) (T, error)) {
	where := callerWhere()
	def, err := newProvider(fn, false, where)
	g.addProvider("Provide", def, err, where)
}

// Override replaces, for this group and the groups made in it, the Provider of T that an enclosing
// scope registered (see the function Override).
func (g *Group) Override[T, D any](fn func(context.Context, Request, D) (T, error)) {
	where := callerWhere()
	def, err := newProvider(fn, true, where)
	g.addProvider("Override", def, err, where)
}

func (g *Group) addProvider(verb string, def *providerDef, err error, where string) {
	label := `group "` + g.prefix + `"`
	if err != nil {
		panic(registrationFailure(label, err, where))
	}
	g.app.mu.Lock()
	sealed := g.sealed
	g.app.mu.Unlock()
	if sealed != "" {
		panic(&RegistrationError{msg: label + ": " + verb + " is called after " + sealed + " was registered in this group, so it would not reach it: move " + verb + " above it\n\tregistered at " + where})
	}
	opt := provideOption(def, nil)
	var scratch routeConfig
	if err := applyOptions(&scratch, append(g.opts[:len(g.opts):len(g.opts)], opt)); err != nil {
		panic(registrationFailure(label, err, where))
	}
	g.opts = append(g.opts[:len(g.opts):len(g.opts)], opt)
}

// providerDef is one Provide or Override call: the type it provides, the fields of its
// dependencies, and the generic step that runs it.
type providerDef struct {
	t        reflect.Type
	deps     []depField
	node     provNode
	copyT    func(dst, src unsafe.Pointer) // *(*T)(dst) = *(*T)(src)
	where    string
	override bool
	auth     *authDoc     // the credentials of an auth Provider the framework made, for the API document (ADR 0069)
	opt      *providerDef // for an auth Provider of T, the optional form an Opt[T] field asks for (ADR 0069)
	base     *providerDef // for that optional form, the Provider it is made from
	secs     []provSecDef // the Header and Cookie sections of its dependencies, decoded before it runs
}

// provSecDef is a Header or Cookie section in a Provider's dependencies: decoded from the request
// as the Input's are, checked by the same rules, and stated in the API document (ADR 0023).
type provSecDef struct {
	kind secKind
	name string
	t    reflect.Type
	off  uintptr
}

type depField struct {
	name string
	t    reflect.Type
	off  uintptr
}

func (d *providerDef) verb() string {
	if d.override {
		return "Override[" + typeLabel(d.t) + "]"
	}
	return "Provide[" + typeLabel(d.t) + "]"
}

// newProvider checks what can be checked about fn without a route: that it exists and that its
// dependencies are a struct of exported fields, none of them T.
func newProvider[T, D any](fn func(context.Context, Request, D) (T, error), override bool, where string) (*providerDef, error) {
	def := &providerDef{t: reflect.TypeFor[T](), where: where, override: override}
	if fn == nil {
		return nil, refusalf("%s: the Provider is nil", def.verb())
	}
	dt := reflect.TypeFor[D]()
	if dt.Kind() != reflect.Struct {
		return nil, refusalf("%s: the Provider's dependencies are %s; they must be a struct whose fields are the provided types it needs, or struct{} when it needs none", def.verb(), dt)
	}
	for i := range dt.NumField() {
		f := dt.Field(i)
		if !f.IsExported() {
			return nil, refusalf("%s: the dependency %s is unexported, so nothing can fill it", def.verb(), f.Name)
		}
		switch f.Name {
		case "Header", "Cookie", "Query":
			if f.Type.Kind() != reflect.Struct {
				return nil, refusalf("%s: the dependency %s is a section and must be a struct, not %s", def.verb(), f.Name, f.Type)
			}
			kind := map[string]secKind{"Header": secHeader, "Cookie": secCookie, "Query": secQuery}[f.Name]
			def.secs = append(def.secs, provSecDef{kind: kind, name: f.Name, t: f.Type, off: f.Offset})
			continue
		case "Path", "Body":
			return nil, refusalf("%s: the dependency %s is a section a Provider cannot read; a Provider reads a Header, a Cookie or (on a route a browser opens without headers) a Query section, and the path and the body are the Input's: ask for the value in the Input, or give the Provider a Header", def.verb(), f.Name)
		}
		if f.Type == def.t {
			return nil, refusalf("%s: the Provider of %s needs %s, which is a cycle; a Provider that refines a value provides a distinct type, such as Admin from User", def.verb(), typeLabel(def.t), typeLabel(def.t))
		}
		def.deps = append(def.deps, depField{name: f.Name, t: f.Type, off: f.Offset})
	}
	def.node = &provider[T, D]{fn: fn}
	def.copyT = copyValue[T]
	if f, ok := any(fn).(func(context.Context, Request, struct{}) (T, error)); ok {
		if def.auth = authOf(f); def.auth != nil {
			alts := append(slices.Clone(def.auth.alts), authAlt{})
			def.opt = &providerDef{
				t: reflect.TypeFor[Opt[T]](), where: where, base: def, copyT: copyValue[Opt[T]],
				auth: &authDoc{alts: alts},
				node: &optProvider[T]{fn: f, doc: def.auth},
			}
		}
	} else {
		def.auth = customAuthOf(fn)
	}
	return def, nil
}

// providerOf is the Provider of t on the route: the one in force for t, or, for an Opt[X] that
// none covers, the optional form of the Provider of X when that one reads a credential
// (ADR 0069). It is nil when there is neither.
func (c *compiler) providerOf(t reflect.Type) *providerDef {
	if i := c.cfg.provIndex(t); i >= 0 {
		return c.cfg.provs[i].def
	}
	if k, elem, _, _ := wire.PresenceOf(t); k == wire.Optional {
		if i := c.cfg.provIndex(elem); i >= 0 {
			return c.cfg.provs[i].def.opt
		}
	}
	return nil
}

// noProvider is the refusal for a type no Provider covers on the route, from what needs it.
func (c *compiler) noProvider(lead string, t reflect.Type) error {
	if k, elem, _, _ := wire.PresenceOf(t); k == wire.Optional {
		if i := c.cfg.provIndex(elem); i >= 0 {
			return refusalf("%s, and no Provider of %s covers this route; the Provider of %s (at %s) does not read a credential the framework knows, so it has no optional form: make it with BearerAuth, BasicAuth, APIKeyAuth, CustomAuth or AnyAuth, or provide %s itself", lead, "Opt["+typeLabel(elem)+"]", typeLabel(elem), c.cfg.provs[i].def.where, "Opt["+typeLabel(elem)+"]")
		}
	}
	return refusalf("%s, and no Provider of %s covers this route", lead, typeLabel(t))
}

func copyValue[T any](dst, src unsafe.Pointer) { *(*T)(dst) = *(*T)(src) }

// provEntry is a Provider in force in a scope, and the depth of the scope that registered it.
type provEntry struct {
	def   *providerDef
	depth int
}

// provideOption is the RouteOption of a Provide or Override: applied in order from the outermost
// scope inward, it adds or replaces the entry for its type, refusing a second Provider of a type
// and an Override with nothing to replace.
func provideOption(def *providerDef, err error) RouteOption {
	return func(c *routeConfig) {
		if err != nil {
			c.refuse("%s", err.Error())
			return
		}
		i := c.provIndex(def.t)
		switch {
		case def.override && i < 0:
			c.refuse("%s: no enclosing scope has a Provider of %s to replace; use Provide[%s]", def.verb(), typeLabel(def.t), typeLabel(def.t))
		case def.override && c.provs[i].depth == c.depth:
			c.refuse("%s: the Provider of %s it would replace is this scope's own (at %s); an Override replaces one from an enclosing scope, so keep one of the two", def.verb(), typeLabel(def.t), c.provs[i].def.where)
		case def.override:
			ps := append([]provEntry(nil), c.provs...)
			ps[i] = provEntry{def: def, depth: c.depth}
			c.provs = ps
		case i >= 0 && c.provs[i].depth == c.depth:
			c.refuse("%s: this scope already has a Provider of %s (at %s); a scope has one Provider for a type", def.verb(), typeLabel(def.t), c.provs[i].def.where)
		case i >= 0:
			c.refuse("%s: an enclosing scope already has a Provider of %s (at %s); use Override[%s] to replace it for this scope and those inside it", def.verb(), typeLabel(def.t), c.provs[i].def.where, typeLabel(def.t))
		default:
			c.provs = append(c.provs[:len(c.provs):len(c.provs)], provEntry{def: def, depth: c.depth})
		}
	}
}

func (c *routeConfig) provIndex(t reflect.Type) int {
	for i, e := range c.provs {
		if e.def.t == t {
			return i
		}
	}
	return -1
}

// enterScope marks where one scope's options end and the next one's begin: the app's Providers,
// then each group's from the outermost, then the route's own. A second Provider of a type is
// refused differently in the same scope and in an enclosing one.
func enterScope(c *routeConfig) { c.depth++ }

// provStep is one Provider a route runs: which one, where its dependencies' values are (the
// earlier steps' slots) and where in its dependency struct they go, and the offsets of the Input
// fields its value fills.
type provStep struct {
	node   provNode
	header *textSection // the Header section of the Provider's dependencies, or nil
	cookie *textSection // its Cookie section, or nil
	query  *textSection // its Query section, or nil; only on a WebSocket or event stream route
	deps   []depCopy
	into   []uintptr
}

type depCopy struct {
	slot int
	off  uintptr
	copy func(dst, src unsafe.Pointer)
}

// providedField is an Input field a Provider fills.
type providedField struct {
	goPath string
	t      reflect.Type
	off    uintptr
}

// isAuthProvider reports whether def is a Provider the framework made for authentication, or the
// optional form of one.
func isAuthProvider(def *providerDef) bool {
	return def.auth != nil || (def.base != nil && def.base.auth != nil)
}

// planProviders resolves the Input's provided fields on a route into the steps that run, in an
// order where the authentication Providers come first and every Provider comes after those it
// needs. A type no Provider covers, and a cycle, are refused naming the chain that led to them.
func (c *compiler) planProviders(fields []providedField) ([]provStep, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	var (
		order []*providerDef
		state = map[reflect.Type]int{} // 1 while its dependencies are being visited, 2 once ordered
		slot  = map[reflect.Type]int{}
	)
	var visit func(def *providerDef, chain []string) error
	visit = func(def *providerDef, chain []string) error {
		switch state[def.t] {
		case 2:
			return nil
		case 1:
			return refusalf("the Providers form a cycle: %s", strings.Join(append(chain, typeLabel(def.t)), " needs "))
		}
		state[def.t] = 1
		chain = append(chain, typeLabel(def.t)+" (at "+def.where+")")
		for _, d := range def.deps {
			dep := c.providerOf(d.t)
			if dep == nil {
				return c.noProvider(strings.Join(append(chain, typeLabel(d.t)), " needs "), d.t)
			}
			if err := visit(dep, chain); err != nil {
				return err
			}
		}
		state[def.t] = 2
		slot[def.t] = len(order)
		order = append(order, def)
		return nil
	}
	// The credential is checked first: the Providers the framework made for authentication
	// (BearerAuth, CustomAuth and the others) are visited before the rest, so a request with no
	// credential is the 401 whatever the order of the Input's fields. The rest follow the fields'
	// order, each after the Providers it needs.
	for _, authFirst := range [2]bool{true, false} {
		for _, f := range fields {
			def := c.providerOf(f.t)
			if def == nil {
				return nil, c.noProvider(f.goPath+" is not a section (Path, Query, Header, Cookie or Body)", f.t)
			}
			if isAuthProvider(def) != authFirst {
				continue
			}
			if err := visit(def, []string{f.goPath}); err != nil {
				return nil, err
			}
		}
	}
	for _, def := range order {
		if def.base != nil && state[def.base.t] == 2 {
			return nil, refusalf("%s needs both %s and %s, so the credential is required on this route: ask for %s", c.inName, typeLabel(def.base.t), "Opt["+typeLabel(def.base.t)+"]", typeLabel(def.base.t))
		}
	}
	c.provDefs = order
	if len(order) > maxProviders {
		return nil, refusalf("%s needs %d Providers; a route runs at most %d", c.inName, len(order), maxProviders)
	}
	steps := make([]provStep, len(order))
	for k, def := range order {
		st := provStep{node: def.node}
		for _, sd := range def.secs {
			if sd.kind == secQuery && !c.browserOpened {
				return nil, refusalf("%s: %s reads a Query section, which a Provider may read only on a route a browser opens without headers of its own, a WebSocket route or one that answers SSE or Events: elsewhere send the value in a header, or ask for it in the Input", c.inName, def.verb())
			}
			sec, err := c.section(sd.kind, def.verb()+" deps."+sd.name, sd.t, sd.off)
			if err != nil {
				return nil, err
			}
			switch sd.kind {
			case secHeader:
				st.header = sec
			case secQuery:
				st.query = sec
				c.provQueryKeys = append(c.provQueryKeys, sec.keys...)
			default:
				st.cookie = sec
			}
			c.provSecs = append(c.provSecs, sec)
		}
		for _, d := range def.deps {
			dep := c.providerOf(d.t)
			st.deps = append(st.deps, depCopy{slot: slot[d.t], off: d.off, copy: dep.copyT})
		}
		steps[k] = st
	}
	for _, f := range fields {
		k := slot[f.t]
		steps[k].into = append(steps[k].into, f.off)
	}
	return steps, nil
}

// provFrame is what the steps of one request share. It lives on the stack of the route's serve and
// is reached through a pointer hidden from escape analysis, as the Input is (unsafe.go).
type provFrame struct {
	a     *App
	ex    *engine.Exchange
	ps    *router.Params
	ctx   context.Context
	req   *Request
	in    unsafe.Pointer
	key   []byte // a WebSocket route's Sec-WebSocket-Key, for the upgrade after the Providers
	steps []provStep
	tail  provTail
	slots [maxProviders]unsafe.Pointer
}

// decodeProviderSections decodes the Header, Cookie and Query sections of a Provider's dependencies into
// d, and answers the 400 that lists what is wrong when something is, as the Input's sections do.
// It reports whether the Provider may run.
func decodeProviderSections(f *provFrame, st *provStep, d unsafe.Pointer) bool {
	rs := getState()
	defer putState(rs)
	head, slab := f.ex.Head(), f.ex.Slab()
	if st.header != nil {
		decodeHeaders(st.header, head, d, slab, &rs.errs)
	}
	if st.cookie != nil {
		decodeCookies(st.cookie, head, d, slab, &rs.errs)
	}
	if st.query != nil {
		decodeQuery(st.query, head.RawQuery, d, slab, &rs.errs)
	}
	if len(rs.errs.List) > 0 {
		invalidInput(f.ex, &rs.errs)
		return false
	}
	return true
}

// provTail is the rest of the route after its Providers: decode the Input, call the handler.
type provTail interface{ afterProviders(f *provFrame) }

// provNode runs step k of f, then everything after it.
type provNode interface{ run(f *provFrame, k int) }

type provider[T, D any] struct {
	fn func(context.Context, Request, D) (T, error)
}

func (p *provider[T, D]) run(f *provFrame, k int) {
	st := &f.steps[k]
	var d D
	dp := noescapeP(unsafe.Pointer(&d))
	if (st.header != nil || st.cookie != nil || st.query != nil) && !decodeProviderSections(f, st, dp) {
		return
	}
	for _, dc := range st.deps {
		dc.copy(unsafe.Add(dp, dc.off), f.slots[dc.slot])
	}
	v, err := p.fn(f.ctx, *f.req, d)
	if err != nil {
		f.a.answerError(f.ex, err)
		return
	}
	f.slots[k] = noescapeP(unsafe.Pointer(&v))
	for _, off := range st.into {
		*(*T)(unsafe.Add(f.in, off)) = v
	}
	if k+1 < len(f.steps) {
		f.steps[k+1].node.run(f, k+1)
	} else {
		f.tail.afterProviders(f)
	}
	f.slots[k] = nil
}
