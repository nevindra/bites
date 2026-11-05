package bites

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/router"
)

// RouteOption changes one route, or every route of a group when it is given to Group (ADR 0024,
// ADR 0048), and so are middleware (Use) and Providers (Provide, Override).
// Options apply in order from the app's limits inward: the group's, a nested group's, then the
// route's own, so the innermost one wins.
type RouteOption func(*routeConfig)

type routeConfig struct {
	maxBody        int64
	bodyTimeout    time.Duration
	unknown        bool
	unknownQuery   uint8 // queryInherit, queryReject or queryAccept
	maxBodySet     bool  // MaxBody was given by an option: an Upload route requires it (ADR 0044)
	bodyTimeoutSet bool
	maxParts       int            // MaxParts; zero is the default
	maxInFlight    int            // MaxInFlight; zero is unlimited
	inflight       *atomic.Int64  // the route's counter, made at Registration when maxInFlight is set
	mw             []Middleware   // Use, in the order the options apply: outermost first
	each           []eachHook     // UseEach, in the order the options apply: outermost first (ADR 0048)
	provs          []provEntry    // the Provider in force for each type, nearest scope last applied (ADR 0023)
	maxMessage     int            // MaxMessage; the WebSocket message limit (ADR 0046)
	origin         *originPolicy  // AllowOrigins and AllowAnyOrigin, or the app's; nil allows the route's own host only
	originKind     uint8          // originInherited, originListed or originAny
	originDepth    int            // the scope that set origin, -1 for the app's
	depth          int            // the scope the options now applying belong to: the app's is 0
	inGroup        bool           // the options are being checked for a Group, not applied to a route
	summary        string         // Summary, for the API document (ADR 0020)
	description    string         // Description
	operationID    string         // OperationID; empty to make one from the method and path
	tags           []string       // Tag, outermost group first, without repeats
	returns        []reflect.Type // Returns: what an interface-typed return may hold (ADR 0013)
	setsLocation   bool           // SetsLocation: a Created on this route always carries a Location
	undocumented   bool           // Undocumented: the route is left out of the API document (ADR 0069)
	deprecated     bool           // Deprecated: the operation is marked deprecated, and RouteInfo says so
	errorStatuses  []int          // ErrorStatuses: the error statuses the handler returns, for the API document
	mediaType      string         // MediaType: the media type of a Bytes, File or Stream answer, for the API document
	sendsHeaders   []sentHeader   // SendsHeader: the headers a success answer carries, for the API document
	bad            string         // the first option whose value cannot work, as a sentence; empty when none
}

// answerOption names the first option given that states answers in the API document
// (ErrorStatuses, MediaType, SendsHeader), or "" when none is.
func (c *routeConfig) answerOption() string {
	switch {
	case len(c.errorStatuses) > 0:
		return "ErrorStatuses"
	case c.mediaType != "":
		return "MediaType"
	case len(c.sendsHeaders) > 0:
		return "SendsHeader"
	}
	return ""
}

// refuse records the first bad option; Registration turns it into a panic.
func (c *routeConfig) refuse(format string, args ...any) {
	if c.bad == "" {
		c.bad = fmt.Sprintf(format, args...)
	}
}

// MaxBody sets the most bytes of request body this route (or every route of this group) reads;
// over it is a 413. It must be positive.
func MaxBody(n int64) RouteOption {
	return func(c *routeConfig) {
		if n <= 0 {
			c.refuse("MaxBody(%d) is not a limit: it must be a positive number of bytes", n)
			return
		}
		c.maxBody, c.maxBodySet = n, true
	}
}

// BodyTimeout sets how long this route (or every route of this group) waits for its body; over
// it is a 408. It must be positive.
func BodyTimeout(d time.Duration) RouteOption {
	return func(c *routeConfig) {
		if d <= 0 {
			c.refuse("BodyTimeout(%v) is not a limit: it must be a positive duration", d)
			return
		}
		c.bodyTimeout, c.bodyTimeoutSet = d, true
	}
}

// MaxInFlight bounds the uploads of this route (or of each Upload route of this group) running
// at once (ADR 0024, ADR 0044). The counter is checked after the head and before 100 Continue,
// and a request over it is answered 503 with Retry-After and the connection closed, so the client
// never sends the body. n must be positive. There is no default, because one would turn away
// legitimate traffic; the start-up log says "unlimited" for an Upload route without it. A group's
// limit is counted per route, not across the group. It has no effect on a route whose Body is not
// an Upload.
func MaxInFlight(n int) RouteOption {
	return func(c *routeConfig) {
		if n <= 0 {
			c.refuse("MaxInFlight(%d) is not a limit: it must be a positive number of uploads", n)
			return
		}
		c.maxInFlight = n
	}
}

// MaxParts sets the most parts one upload on this route (or on each Upload route of this group)
// may have; more is a 413 (ADR 0044). The default is 128. n must be positive.
func MaxParts(n int) RouteOption {
	return func(c *routeConfig) {
		if n <= 0 {
			c.refuse("MaxParts(%d) is not a limit: it must be a positive number of parts", n)
			return
		}
		c.maxParts = n
	}
}

// AcceptUnknownMembers lets a JSON body carry members the Body type does not have; by default
// each is a 400 naming it (ADR 0015). On a group it applies to every route of the group.
func AcceptUnknownMembers() RouteOption { return func(c *routeConfig) { c.unknown = true } }

// The three states of routeConfig.unknownQuery: the app's WithRejectUnknownQueryKeys decides
// unless a group or the route said otherwise.
const (
	queryInherit uint8 = iota
	queryReject
	queryAccept
)

// RejectUnknownQueryKeys makes a request to this route (or to every route of this group) a 400
// when its query holds a key the Query section does not name, located query.<key> (ADR 0015).
// By default such a key is ignored. A route without a Query section is held to it too: it takes
// no query at all. The innermost option wins, so a route can undo its group's.
func RejectUnknownQueryKeys() RouteOption {
	return func(c *routeConfig) { c.unknownQuery = queryReject }
}

// AcceptUnknownQueryKeys lets this route (or every route of this group) ignore query keys its
// Query section does not name, as a route does by default. It is the way out of
// WithRejectUnknownQueryKeys for the route that must tolerate tracking parameters it did not
// ask for.
func AcceptUnknownQueryKeys() RouteOption {
	return func(c *routeConfig) { c.unknownQuery = queryAccept }
}

// Use adds middleware to a route, or to every route of a group when it is given to Group
// (ADR 0048). It is the second spelling of Group.Use, for a module that receives a group already
// holding routes: g.Group("/orders", Use(audit)). Options apply from the outermost group inward,
// so the group's middleware wraps the route's. A nil middleware is refused.
func Use(mw ...Middleware) RouteOption {
	return func(c *routeConfig) {
		for i, m := range mw {
			if m == nil {
				c.refuse("Use: middleware %d is nil", i+1)
				return
			}
			if sameCode(m, (*cors)(nil).serve) {
				c.refuse("Use: middleware %d is CORS, which answers a preflight, an OPTIONS request no route has, so on a group or a route it is never reached and every preflight is a 405: give it to App.Use, with CORSOptions.Paths to limit it to a path such as /api", i+1)
				return
			}
		}
		c.mw = append(c.mw, mw...)
	}
}

// RouteInfo is what the function given to UseEach is told about one route at Registration
// (ADR 0048): the route's identity, copied, with nothing of the framework's inside it.
type RouteInfo struct {
	// Method is the route's method: "GET" for a WebSocket route and for a Static one.
	Method string
	// Pattern is the route's pattern with its groups' prefixes, as it was registered:
	// "/orders/{id}". A Static route's is its prefix and "/{path...}".
	Pattern string
	// OperationID is the operationId the API document gives the route: the one given with
	// OperationID, or the one made from the method and path (ADR 0020). It is empty for a route
	// the document leaves out: a Static route, an Undocumented one (every stdhttp.Mount route),
	// and one whose method the document has no name for. A WebSocket route has the id of its
	// handshake's get.
	OperationID string
	// Tags are the tags of the route, the outermost group's first, as Tag gave them.
	Tags []string
	// Deprecated is true for a route given Deprecated, itself or through a group (ADR 0020). It is
	// true for a route the API document leaves out too, so a function that adds the Deprecation
	// and Sunset headers sees every deprecated route. False is the zero value: not deprecated.
	Deprecated bool
	// WebSocket is true for a route made with WebSocket.
	WebSocket bool
	// Static is true for a route made with Static.
	Static bool
	// Builtin names the route when the app registered it for itself: BuiltinOpenAPI for the API
	// document OpenAPI serves and BuiltinMetrics for the page ServeMetrics serves; it is "" for every
	// route the application wrote. Method is "GET" and Pattern the path given, and there is no
	// OperationID. A function that refuses what it does not know refuses these too, so a guard
	// exempts them by name or the app does not start.
	Builtin string
}

// The values of RouteInfo.Builtin. A guard compares with these rather than with the literal, so a
// misspelling does not compile.
const (
	BuiltinOpenAPI = "openapi" // the API document OpenAPI serves
	BuiltinMetrics = "metrics" // the page ServeMetrics serves
)

// eachHook is one UseEach and the line that gave it.
type eachHook struct {
	build func(RouteInfo) (Middleware, error)
	where string
}

func useEachOption(build func(RouteInfo) (Middleware, error), where string) RouteOption {
	return func(c *routeConfig) {
		if build == nil {
			c.refuse("UseEach: the function is nil")
			return
		}
		c.each = append(c.each, eachHook{build, where})
	}
}

// UseEach is the option spelling of App.UseEach and Group.UseEach (ADR 0048): build is called
// once for each route it reaches, at Registration, with the route's RouteInfo. The Middleware it
// returns runs inside every Use middleware of the route, next to the handler; a nil Middleware
// means none for this route; an error refuses the route, and the app panics at start. A nil
// function is refused. build runs with the Registration lock held, so it must not register
// anything.
func UseEach(build func(r RouteInfo) (Middleware, error)) RouteOption {
	return useEachOption(build, callerWhere())
}

// addEach calls the UseEach functions of cfg for a route, in order, and appends what they return
// to the route's middleware. The Registration lock is held.
func (c *routeConfig) addEach(info RouteInfo, label, where string) {
	for _, h := range c.each {
		m, err := h.build(info)
		if err != nil {
			panic(&RegistrationError{msg: label + ": the function given to UseEach (at " + h.where + ") refused the route: " + err.Error() + "\n\tregistered at " + where})
		}
		if sameCode(m, (*cors)(nil).serve) {
			panic(&RegistrationError{msg: label + ": the function given to UseEach (at " + h.where + ") returned CORS, which answers a preflight, an OPTIONS request no route has, so on a route it is never reached and every preflight is a 405: give it to App.Use, with CORSOptions.Paths to limit it to a path such as /api\n\tregistered at " + where})
		}
		if m != nil {
			c.mw = append(c.mw, m)
		}
	}
	c.each = nil
}

// noteRoute keeps the RouteInfo of a route for App.Routes. It is the one RouteInfo a route has:
// the same value is then given to the UseEach functions, so the list and UseEach cannot disagree.
// The Registration lock is held. The list is published with its length fixed: an element below
// that length is never written again, and a later append writes past it or into a new array, so
// Routes reads it without the lock.
func (a *App) noteRoute(info RouteInfo) {
	info.Tags = slices.Clone(info.Tags)
	a.routes = append(a.routes, info)
	list := a.routes[:len(a.routes):len(a.routes)]
	a.routeList.Store(&list)
}

// Routes returns every route registered so far, in the order it was registered, each as UseEach
// is told about it (ADR 0048): the typed routes (those mounted with stdhttp.Mount among them),
// WebSocket and Static routes, and the routes OpenAPI and ServeMetrics register, with Builtin set.
// The paths of WithHealth are answered before routing and are not routes, so they are not listed.
// An app with no route returns an empty slice. The slice is new on every call and each Tags is a
// copy, so changing what it returns changes nothing in the app. It may be called at any time and
// from any goroutine, before or while serving, and takes no lock. Called from a function given to
// UseEach it returns the routes registered so far, the one being registered last, so a check of
// every route belongs after the last route is registered, not in UseEach.
func (a *App) Routes() []RouteInfo {
	var list []RouteInfo
	if p := a.routeList.Load(); p != nil {
		list = *p
	}
	out := make([]RouteInfo, len(list))
	for i, r := range list {
		r.Tags = slices.Clone(r.Tags)
		out[i] = r
	}
	return out
}

// eachBuiltin applies the app-level UseEach functions to a route the app registered for itself
// (OpenAPI, ServeMetrics), so a default-deny function sees it (ADR 0048). The Registration lock is
// held.
func (a *App) eachBuiltin(rt *route, builtin, path, label, where string) {
	var cfg routeConfig
	if err := applyOptions(&cfg, scoped(a.provOpts, nil)); err != nil {
		panic(registrationFailure(label, err, where))
	}
	info := RouteInfo{Method: "GET", Pattern: path, Builtin: builtin}
	a.noteRoute(info)
	if len(cfg.each) > 0 {
		cfg.addEach(info, label, where)
	}
	if err := a.bindMiddleware(cfg.mw); err != nil {
		panic(registrationFailure(label, err, where))
	}
	a.useOn(rt, cfg.mw)
	rt.chain = len(cfg.mw) > 0
	if a.builtin == "" {
		a.builtin = label + " (at " + where + ")"
	}
}

// routeOperationID is the operationId of a route as describeRoute makes it (ADR 0020), or "" for
// a route the document leaves out: Undocumented, or a method it has no name for.
func routeOperationID(cfg *routeConfig, method, full string) string {
	if cfg.undocumented || !docMethods[method] {
		return ""
	}
	if cfg.operationID != "" {
		return cfg.operationID
	}
	template, _ := docPath(full)
	return operationID(method, template)
}

// Options bundles several options into one, applied in the order given. An application writes
// func Op(id, summary string) bites.RouteOption { return bites.Options(bites.OperationID(id),
// bites.Summary(summary)) } because RouteOption's parameter is not nameable outside this
// package, and the result is an option like any other: it takes its place among the others of a
// call or of a Group, and every refusal of a member (Summary on a group, MaxBody(0), a nil
// middleware) is that member's own, naming the same route. A nil member is refused, naming its
// position. Options() with none is an option that changes nothing.
func Options(opts ...RouteOption) RouteOption {
	opts = slices.Clone(opts)
	return func(c *routeConfig) {
		for i, o := range opts {
			if o == nil {
				c.refuse("Options: option %d is nil", i+1)
				return
			}
			o(c)
			if c.bad != "" {
				return
			}
		}
	}
}

// applyOptions applies opts to c in order and reports the first that cannot work.
func applyOptions(c *routeConfig, opts []RouteOption) error {
	for _, o := range opts {
		if o == nil {
			return refusalf("an option is nil")
		}
		o(c)
		if c.bad != "" {
			return refusalf("%s", c.bad)
		}
	}
	return nil
}

// Get registers a typed handler for GET pattern (and HEAD, ADR 0018). The handler is a plain
// function or a method value, and its Input and Output types are checked against the pattern
// now: a mistake panics with a *RegistrationError (ADR 0007).
func (a *App) Get[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	registerApp(a, "GET", pattern, h, opts, callerWhere())
}

// Post registers a typed handler for POST pattern.
func (a *App) Post[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	registerApp(a, "POST", pattern, h, opts, callerWhere())
}

// Put registers a typed handler for PUT pattern.
func (a *App) Put[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	registerApp(a, "PUT", pattern, h, opts, callerWhere())
}

// Patch registers a typed handler for PATCH pattern.
func (a *App) Patch[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	registerApp(a, "PATCH", pattern, h, opts, callerWhere())
}

// Delete registers a typed handler for DELETE pattern.
func (a *App) Delete[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	registerApp(a, "DELETE", pattern, h, opts, callerWhere())
}

// Head registers a typed handler for HEAD pattern, which then wins over the GET route's implicit
// HEAD (ADR 0018). The body of its answer is not sent.
func (a *App) Head[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	registerApp(a, "HEAD", pattern, h, opts, callerWhere())
}

// Options registers a typed handler for OPTIONS pattern.
func (a *App) Options[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	registerApp(a, "OPTIONS", pattern, h, opts, callerWhere())
}

// Handle registers a typed handler for any method token. A typed route always has a method
// (ADR 0018), so an empty method panics.
func (a *App) Handle[In, Out any](method, pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	registerApp(a, method, pattern, h, opts, callerWhere())
}

// Group is a path prefix on an App and the options its routes share (ADR 0048): limits,
// middleware and Providers. A Group carries the prefix, and nests.
type Group struct {
	app    *App
	prefix string
	opts   []RouteOption // the options of every enclosing group, outermost first, then this one's
	sealed string        // what was registered first in this group, and where; "" until then
}

// seal records the first route or child group of g, which Use refuses to come after: options are
// copied into a child when it is made and into a route when it is registered, so a later Use would
// reach neither (ADR 0048).
func (g *Group) seal(what, where string) {
	g.app.mu.Lock()
	defer g.app.mu.Unlock()
	if g.sealed == "" {
		g.sealed = what + " (at " + where + ")"
	}
}

// Use adds middleware to every route of this group and of the groups made in it afterwards, inside
// the middleware of the groups around it (ADR 0048). It must come before the group's first route
// and first child group: after one it panics, naming the line that sealed the group, because the
// middleware would silently skip what exists already. A nil middleware panics.
func (g *Group) Use(mw ...Middleware) {
	g.addScoped("Use", Use(mw...), callerWhere())
}

// UseEach adds a function that builds a middleware from each route's RouteInfo to every route of
// this group and of the groups made in it afterwards (see the function UseEach). Like Use it must
// come before the group's first route and first child group.
func (g *Group) UseEach(build func(r RouteInfo) (Middleware, error)) {
	where := callerWhere()
	g.addScoped("UseEach", useEachOption(build, where), where)
}

// addScoped adds opt to the options of every route to come, refusing it after the group is sealed.
func (g *Group) addScoped(verb string, opt RouteOption, where string) {
	label := `group "` + g.prefix + `"`
	g.app.mu.Lock()
	sealed := g.sealed
	g.app.mu.Unlock()
	if sealed != "" {
		panic(&RegistrationError{msg: label + ": " + verb + " is called after " + sealed + " was registered in this group, so it would not reach it: move " + verb + " above it\n\tregistered at " + where})
	}
	var scratch routeConfig
	if err := applyOptions(&scratch, []RouteOption{opt}); err != nil {
		panic(registrationFailure(label, err, where))
	}
	g.opts = append(g.opts[:len(g.opts):len(g.opts)], opt)
}

// Group returns a Group whose routes all start with prefix. The prefix starts with a slash and
// does not end with one; anything else panics. Its options apply to every route in it and in the
// groups nested in it; a nested group's beat this group's, and a route's own beat both. An
// option with a value that cannot work, such as MaxBody(0), panics here.
func (a *App) Group(prefix string, opts ...RouteOption) *Group {
	where := callerWhere()
	a.seal(`the group "`+prefix+`"`, where)
	a.mu.Lock()
	inherited := a.provOpts
	a.mu.Unlock()
	return newGroup(a, "", inherited, prefix, opts, where)
}

// seal records the app's first route or group, which an app Provide may not come after
// (ADR 0048): a route's Providers are checked at the route's own line.
func (a *App) seal(what, where string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sealed == "" {
		a.sealed = what + " (at " + where + ")"
	}
}

// scoped is the options of a route or a group: those of the enclosing scopes, outermost first,
// then its own, with the mark between them that tells a Provider which scope it is in.
func scoped(enclosing, own []RouteOption) []RouteOption {
	all := make([]RouteOption, 0, len(enclosing)+1+len(own))
	return append(append(append(all, enclosing...), enterScope), own...)
}

// Group returns a Group nested in g, which inherits g's options and may override them.
func (g *Group) Group(prefix string, opts ...RouteOption) *Group {
	where := callerWhere()
	g.seal(`the group "`+g.prefix+prefix+`"`, where)
	return newGroup(g.app, g.prefix, g.opts, prefix, opts, where)
}

func newGroup(a *App, outer string, inherited []RouteOption, prefix string, opts []RouteOption, where string) *Group {
	if !strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") || strings.ContainsAny(prefix, "{}") {
		panic(&RegistrationError{msg: "group " + `"` + prefix + `"` + ": a group prefix starts with a slash, does not end with one, and has no wildcard; to give options to routes that share no prefix, bundle them once with bites.Options(...) and pass that to each route\n\tregistered at " + where})
	}
	// Checked now rather than when a route is added, so a group with no routes yet is refused too.
	// The scratch config is thrown away: the options are applied again, in order, per route. The
	// enclosing scopes' options are applied too, so a second Provider of a type is found here.
	all := scoped(inherited, opts)
	scratch := routeConfig{inGroup: true}
	if err := applyOptions(&scratch, all); err != nil {
		panic(registrationFailure(`group "`+outer+prefix+`"`, err, where))
	}
	return &Group{app: a, prefix: outer + prefix, opts: all}
}

// Get registers a typed handler for GET prefix+pattern.
func (g *Group) Get[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	where := callerWhere()
	g.seal("GET"+" "+g.prefix+pattern, where)
	register(g.app, g.prefix, "GET", pattern, h, scoped(g.opts, opts), where)
}

// Post registers a typed handler for POST prefix+pattern.
func (g *Group) Post[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	where := callerWhere()
	g.seal("POST"+" "+g.prefix+pattern, where)
	register(g.app, g.prefix, "POST", pattern, h, scoped(g.opts, opts), where)
}

// Put registers a typed handler for PUT prefix+pattern.
func (g *Group) Put[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	where := callerWhere()
	g.seal("PUT"+" "+g.prefix+pattern, where)
	register(g.app, g.prefix, "PUT", pattern, h, scoped(g.opts, opts), where)
}

// Patch registers a typed handler for PATCH prefix+pattern.
func (g *Group) Patch[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	where := callerWhere()
	g.seal("PATCH"+" "+g.prefix+pattern, where)
	register(g.app, g.prefix, "PATCH", pattern, h, scoped(g.opts, opts), where)
}

// Delete registers a typed handler for DELETE prefix+pattern.
func (g *Group) Delete[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	where := callerWhere()
	g.seal("DELETE"+" "+g.prefix+pattern, where)
	register(g.app, g.prefix, "DELETE", pattern, h, scoped(g.opts, opts), where)
}

// Head registers a typed handler for HEAD prefix+pattern.
func (g *Group) Head[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	where := callerWhere()
	g.seal("HEAD"+" "+g.prefix+pattern, where)
	register(g.app, g.prefix, "HEAD", pattern, h, scoped(g.opts, opts), where)
}

// Options registers a typed handler for OPTIONS prefix+pattern.
func (g *Group) Options[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	where := callerWhere()
	g.seal("OPTIONS"+" "+g.prefix+pattern, where)
	register(g.app, g.prefix, "OPTIONS", pattern, h, scoped(g.opts, opts), where)
}

// Handle registers a typed handler for method and prefix+pattern.
func (g *Group) Handle[In, Out any](method, pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption) {
	where := callerWhere()
	g.seal(method+" "+g.prefix+pattern, where)
	register(g.app, g.prefix, method, pattern, h, scoped(g.opts, opts), where)
}

// registerApp registers a route made on the app itself: the app's Providers are its enclosing
// scope.
func registerApp[In, Out any](a *App, method, pattern string, h func(context.Context, In) (Out, error), opts []RouteOption, where string) {
	a.seal(method+" "+pattern, where)
	a.mu.Lock()
	enclosing := a.provOpts
	a.mu.Unlock()
	register(a, "", method, pattern, h, scoped(enclosing, opts), where)
}

// typedRoute is one route after Registration: the handler and the plans of its Input and Output.
type typedRoute[In, Out any] struct {
	h   func(context.Context, In) (Out, error)
	in  *inputPlan
	out *outPlan
	cfg routeConfig

	validate func(In) error // the Input's Validate as a method expression; nil when it has none
}

// register is the whole of Registration (ADR 0007): add the pattern to the table (which refuses a
// malformed or conflicting one), read the Input and Output types, and keep a closure that holds
// the plans.
func register[In, Out any](a *App, prefix, method, pattern string, h func(context.Context, In) (Out, error), opts []RouteOption, where string) {
	full := prefix + pattern
	label := method + " " + full
	fail := func(err error) { panic(registrationFailure(label, err, where)) }
	if method == "" {
		fail(refusalf("a typed route needs a method; a method-less pattern is for mounting a plain http.Handler"))
	}
	if h == nil {
		fail(refusalf("the handler is nil"))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.serving {
		panic(&RegistrationError{msg: "route " + label + " registered at " + where + " after the app started serving: routes can be added only before it does"})
	}
	rt := a.addRoute(method, full, label, where)
	cfg := routeConfig{maxBody: a.limits.MaxBody, bodyTimeout: a.limits.BodyTimeout}
	if err := applyOptions(&cfg, opts); err != nil {
		fail(err)
	}
	if cfg.undocumented && (cfg.summary != "" || cfg.description != "" || cfg.operationID != "") {
		fail(refusalf("Summary, Description and OperationID describe an operation of the API document, which Undocumented leaves this route out of: give one or the other"))
	}
	if cfg.undocumented {
		if name := cfg.answerOption(); name != "" {
			fail(refusalf("%s states answers in the API document, which Undocumented leaves this route out of: give one or the other", name))
		}
	}
	info := RouteInfo{Method: method, Pattern: full, OperationID: routeOperationID(&cfg, method, full), Tags: slices.Clone(cfg.tags), Deprecated: cfg.deprecated}
	a.noteRoute(info)
	if len(cfg.each) > 0 {
		cfg.addEach(info, label, where)
	}
	if !cfg.undocumented {
		if err := a.checkShape(method, full, where); err != nil {
			fail(err)
		}
	}
	c := &compiler{a: a, method: method, params: patternParams(full), cfg: cfg, browserOpened: eventStream(reflect.TypeFor[Out]())}
	in, err := c.compileInput(reflect.TypeFor[In]())
	if err != nil {
		fail(err)
	}
	if in.body != nil && (in.body.kind == bodyUpload || in.body.kind == bodyMultipart) {
		a.planUpload(label, &cfg, in.body.kind == bodyMultipart)
	}
	outT := reflect.TypeFor[Out]()
	var out *outPlan
	if isOneOf(outT) {
		out, err = a.compileOneOf(outT, method)
	} else {
		out, err = a.compileOutput(outT)
	}
	if err != nil {
		fail(err)
	}
	if err := a.compileReturns(out, outT, cfg.returns, method); err != nil {
		fail(err)
	}
	if in.body != nil && in.body.sub && !out.answersPage() {
		fail(refusalf("the Body is a Submitted form, whose mistakes the handler shows itself, but the handler returns %s and no Page to show the form in: return a Page (or a OneOf with one, such as OneOf2[SeeOther, Page]), or read the body as Form[T] and let the framework answer a bad form with the 400", outT))
	}
	if cfg.setsLocation && !out.markLocation() {
		fail(refusalf("SetsLocation is given, but the handler returns %s, which cannot be a Created: only a Created has a Location the handler may leave empty", outT))
	}
	if out.wk >= wrapStream && method == "HEAD" {
		fail(refusalf("a HEAD route cannot return a stream: its answer has no body to stream"))
	}
	a.wires = append(a.wires, &wireRoute{method: method, pattern: full, where: where, inT: reflect.TypeFor[In](), outT: outT, in: in, out: out, unknown: cfg.unknown})
	if !cfg.undocumented {
		op, err := a.describeRoute(method, full, where, reflect.TypeFor[In](), in, outT, out, &cfg, c.provDefs, c.provSecs)
		if err != nil {
			fail(err)
		}
		if op != nil {
			a.docs = append(a.docs, op)
		}
	}
	a.methods[method] = true
	tr := &typedRoute[In, Out]{h: h, in: in, out: out, cfg: cfg}
	if in.validates {
		// The method expression, found once here: calling it per request is a plain call that
		// takes the Input by value, with no interface to box it in (ADR 0021, ADR 0003).
		m, _ := reflect.TypeFor[In]().MethodByName("Validate")
		tr.validate = m.Func.Interface().(func(In) error)
	}
	rt.serve = tr.serve
	if err := a.bindMiddleware(cfg.mw); err != nil {
		fail(err)
	}
	a.useOn(rt, cfg.mw)
	// A Provider receives the request view, which the chain builds; a route with Providers takes
	// the chain's path even with no middleware.
	rt.chain = len(cfg.mw) > 0 || len(in.provs) > 0
}

// serve is the hot path of a typed route: run its Providers, decode the Input in place, call the
// handler, write the answer. The Input and the Output stay on this frame; nothing in it is pooled
// (ADR 0008). req is the chain's request view, nil on a route with neither middleware nor
// Providers.
func (rt *typedRoute[In, Out]) serve(a *App, ex *engine.Exchange, ps *router.Params, ctx context.Context, req *Request) {
	var in In
	if steps := rt.in.provs; steps != nil {
		f := provFrame{a: a, ex: ex, ps: ps, ctx: ctx, req: req, in: noescapeP(unsafe.Pointer(&in)), steps: steps, tail: rt}
		steps[0].node.run(noescape(&f), 0)
		return
	}
	st := getState()
	ok := rt.in.decode(a, ex, ps, noescapeP(unsafe.Pointer(&in)), st, &rt.cfg)
	putState(st)
	if !ok {
		return
	}
	rt.call(a, ex, ctx, in)
}

// afterProviders is the rest of serve once the last Provider has filled its fields.
func (rt *typedRoute[In, Out]) afterProviders(f *provFrame) {
	rt.body(f.a, f.ex, f.ps, f.ctx, (*In)(f.in))
}

func (rt *typedRoute[In, Out]) body(a *App, ex *engine.Exchange, ps *router.Params, ctx context.Context, in *In) {
	st := getState()
	ok := rt.in.decode(a, ex, ps, unsafe.Pointer(in), st, &rt.cfg)
	putState(st)
	if !ok {
		return
	}
	rt.call(a, ex, ctx, *in)
}

// call runs Validate and the handler on a decoded Input, and writes the answer.
func (rt *typedRoute[In, Out]) call(a *App, ex *engine.Exchange, ctx context.Context, in In) {
	if rt.validate != nil {
		// A Submitted form with mistakes goes to the handler as it is; Validate runs on a form
		// decoded whole, and its mistakes at the body join the form's (ADR 0078).
		if b := rt.in.body; b == nil || !b.sub || b.formValid(unsafe.Pointer(&in)) {
			if err := rt.validate(in); err != nil {
				if b == nil || !b.sub || !b.keepValidate(unsafe.Pointer(&in), err) {
					a.answerInvalid(ex, err)
					return
				}
			}
		}
	}
	out, err := rt.h(ctx, in)
	if err != nil {
		a.answerError(ex, err)
		return
	}
	if rt.out.dynamic {
		rt.out.writeDynamic(a, ex, any(out), ctx)
		return
	}
	rt.out.write(a, ex, noescapeP(unsafe.Pointer(&out)), ctx)
}

// addRoute adds a route to the table and returns it with its pattern (ADR 0051), refusing a path
// WithHealth answers (ADR 0052) and whatever the router refuses. The Registration lock is held.
func (a *App) addRoute(method, full, label, where string) *route {
	a.healthConflict(label, full, where)
	rt := newRoute(full)
	if err := a.router.Add(method, full, rt, where); err != nil {
		panic(&RegistrationError{msg: err.Error()})
	}
	return rt
}
