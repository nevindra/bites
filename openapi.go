package bites

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"mime"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/openapi"
	"github.com/nevindra/bites/internal/router"
	"github.com/nevindra/bites/internal/wire"
)

// The OpenAPI 3.1 document (ADR 0020). Each typed route leaves a record at Registration: its
// operation, built from the plans and types the route was compiled from, so every mistake in
// them (a description that names a missing field, a type the document cannot describe) is a
// refusal at the line that registered the route. The whole document is put together, its
// references named, and written as bytes once, when the app starts serving; a request for it
// copies those bytes. Nothing here runs per request.

// Info is the identity of the API in its document. Title and Version are required by OpenAPI.
// Servers are where the API is served, the document's servers (ADR 0025): without them OpenAPI
// reads the document as served from "/" of the host it was fetched from, which is wrong behind a
// proxy that strips a prefix.
type Info struct {
	// Title is the name of the API; OpenAPI requires it, so an empty one is refused.
	Title string
	// Version is the version of the API, not of OpenAPI; OpenAPI requires it, so an empty one is refused.
	Version string
	// Description is optional prose for the document, and may use CommonMark.
	Description string
	// Servers are where the API is served, in the order given; see Server.
	Servers []Server

	// SchemaName, when set, names the document's component schemas. It is called once for each
	// Go type that becomes a component, with the type's package path and the name the framework
	// would give it (the Go name, a generic instance's type arguments appended, anything outside
	// A-Z a-z 0-9 . - _ written as "_"), and returns the component name, or "" to keep the
	// default rules. A name it returns is used whether or not the default would collide, and the
	// `.Request`, `.RequestOpen` and `.Response` suffixes of a type whose schemas differ by mode
	// are added after it. Two types given one name, a name with a character outside that set, and
	// a name that another type has by default are refused when the document is built.
	SchemaName func(pkgPath, name string) string
}

// Server is one entry of the API document's servers: a URL the API is served at, absolute
// ("https://api.example.com/chat") or a path on the host the document was fetched from ("/chat"),
// and an optional description. The paths of the document follow the URL, so it has no trailing
// slash. It is fixed at Registration: the document is the same bytes for every request.
type Server struct {
	// URL is an absolute http or https URL, or a path on the host the document was fetched from.
	URL string
	// Description is optional prose about the server.
	Description string
}

// Summary sets the one-line summary of the route's operation in the API document. It is for a
// route, not a group, because the same line on every operation of a group would describe none of
// them.
func Summary(s string) RouteOption {
	return func(c *routeConfig) {
		switch {
		case c.inGroup:
			c.refuse("Summary is given to a group, but one summary cannot describe every operation in it: give it to each route")
		case strings.TrimSpace(s) == "":
			c.refuse("Summary is empty")
		default:
			c.summary = s
		}
	}
}

// Description sets the longer description of the route's operation in the API document (CommonMark
// is allowed). Like Summary it is for a route, not a group.
func Description(s string) RouteOption {
	return func(c *routeConfig) {
		switch {
		case c.inGroup:
			c.refuse("Description is given to a group, but one description cannot describe every operation in it: give it to each route")
		case strings.TrimSpace(s) == "":
			c.refuse("Description is empty")
		default:
			c.description = s
		}
	}
}

// Tag adds tags to the route's operation in the API document, or to every operation of a group
// when it is given to Group. A tag a group and its route both give is listed once, the group's
// first.
func Tag(names ...string) RouteOption {
	return func(c *routeConfig) {
		if len(names) == 0 {
			c.refuse("Tag is given no name")
			return
		}
		for _, n := range names {
			if strings.TrimSpace(n) == "" {
				c.refuse("Tag is given an empty name")
				return
			}
			if !slices.Contains(c.tags, n) {
				c.tags = append(c.tags, n)
			}
		}
	}
}

// Deprecated marks the route's operation deprecated in the API document: the operation gets
// "deprecated": true, which a client generator and a documentation page show as a warning. Given
// to Group (or inside Options) it deprecates every route of the group and its nested groups, as
// Tag does. Giving it to a route and to its group as well is harmless. It is for the API document
// and for RouteInfo.Deprecated, which a UseEach function reads to add the Deprecation (RFC 9745)
// and Sunset (RFC 8594) headers itself: the framework sends no header. A route the document
// leaves out (WebSocket, Static, Undocumented) accepts it and still reports it in RouteInfo.
// There is no way to take it back inside a group, and no argument: a route that is deprecated is
// not deprecated "unless".
func Deprecated() RouteOption { return func(c *routeConfig) { c.deprecated = true } }

// Returns declares a type that a handler returning an interface may return (ADR 0013). A route
// whose output is an interface needs one Returns for each type it may hold: the answer of each is
// compiled at Registration, and the API document gets its status and schema. T is the type
// itself (OK[User], Created[User], NoContent, a plain value); a handler may return it by value or
// by pointer. T must implement the interface, may not be named twice, and Returns is refused on a
// route whose output is not an interface, on a WebSocket route and on a group.
func Returns[T any]() RouteOption {
	t := reflect.TypeFor[T]()
	return func(c *routeConfig) {
		switch {
		case c.inGroup:
			c.refuse("Returns[%s] is given to a group, but each route's handler returns its own types: give it to the route", t)
		case slices.Contains(c.returns, t):
			c.refuse("Returns[%s] is given twice", t)
		default:
			c.returns = append(c.returns, t)
		}
	}
}

// Undocumented leaves the route, or every route of a group, out of the API document (ADR 0069):
// a mounted net/http handler, the documentation page itself, an internal or debugging route. The
// route is served as any other; it is not in paths, its types are not in components, and the
// check that the document can hold its path is not made. A Summary or Description on the same
// route is refused, since the document would drop it; a Tag, usually a group's, is ignored.
func Undocumented() RouteOption { return func(c *routeConfig) { c.undocumented = true } }

// SetsLocation says that every Created this route answers carries a Location (ADR 0013). The API
// document then states the header on the 201 and marks it required, where without the option it
// states none, because a Created sends Location only when the handler fills the field and most
// handlers do not. A handler that returns a Created with an empty Location on such a route is a
// logged 500, since the document promised the header; a handler that sets one on a route without
// the option still sends it, as before. It is for a route, not a group, whose routes differ in
// what they return, and a route whose handler cannot return a Created is refused.
func SetsLocation() RouteOption {
	return func(c *routeConfig) {
		if c.inGroup {
			c.refuse("SetsLocation is given to a group, but only some of its routes may return a Created: give it to each route that does")
			return
		}
		c.setsLocation = true
	}
}

// ErrorStatuses states the error statuses a handler on this route, or on every route of a group,
// may return, so the API document lists them beside its own (ADR 0069): ErrorStatuses(404, 409)
// on a route whose handler returns NotFound or Conflict. Each is documented as a problem+json
// answer, the shape every error answer has. The document already states what the framework itself
// answers (400 for the Input, 401 for a credential, 413 and 415 for a body, 429 and 403 for the
// first-party middleware), and its default answer covers any other error, so the option is for
// what the handler and its Providers decide; a 429 is documented as the rate limit's answer, with
// Retry-After and RateLimit, which is how a Provider that calls RateLimiter.Allow states its
// limit: ErrorStatuses(429) on the group it is given to. A status outside 400 to 599, or named twice in one
// call, is refused; a group's and a route's statuses add up. It does not limit what the handler
// may return: an error status not named is still sent, and documented only by the default.
func ErrorStatuses(statuses ...int) RouteOption {
	return func(c *routeConfig) {
		if len(statuses) == 0 {
			c.refuse("ErrorStatuses is given no status: name the error statuses the handler returns, as ErrorStatuses(404, 409)")
			return
		}
		for i, s := range statuses {
			switch {
			case s < 400 || s > 599:
				c.refuse("ErrorStatuses is given %d, which is not an error status: give the statuses from 400 to 599 the handler returns", s)
				return
			case slices.Contains(statuses[:i], s):
				c.refuse("ErrorStatuses is given %d twice", s)
				return
			case !slices.Contains(c.errorStatuses, s):
				c.errorStatuses = append(c.errorStatuses, s)
			}
		}
	}
}

// ErrorStatus states an error status a handler may return together with the type of the members
// its answer carries, so the API document, and the client types generated from it, say what the
// problem body holds beyond the framework's own members (ADR 0088):
//
//	bites.ErrorStatus[StaleBlocks](409)
//
// The document's response for that status is application/problem+json with the problem's members
// and the fields of T. The handler sends the data with WithMembers(bites.Conflict("..."), StaleBlocks{...}),
// which writes T with the response codec. T is a struct whose wire names do not clash with the
// problem's own (type, title, status, detail, instance, errors, truncated, request_id). It
// replaces ErrorStatuses for its status: do not name the status in both on one route. It is
// refused for 400, 401 and 429, which the framework answers in a shape of its own, for a status the
// framework already answers on the route (for example 413 on a route with a body), for a T that
// is not a struct or cannot be written as JSON, for a status named twice on a route and its groups,
// and on an Undocumented or static route. It states and does not check: an error returned with
// other members is still sent, and logged as one the document does not describe.
func ErrorStatus[T any](status int) RouteOption {
	t := reflect.TypeFor[T]()
	return func(c *routeConfig) {
		switch {
		case status < 400 || status > 599:
			c.refuse("ErrorStatus[%s] is given %d, which is not an error status: give a status from 400 to 599 the handler returns", typeLabel(t), status)
		case status == 400 || status == 401 || status == 429:
			c.refuse("ErrorStatus[%s] is given %d, which the framework answers in a shape of its own (400 lists the Input's mistakes, 401 and 429 carry headers): use a status the handler decides, such as 409 or 422", typeLabel(t), status)
		case t.Kind() != reflect.Struct:
			c.refuse("ErrorStatus[%s](%d): the members of an error are the fields of a struct, so T must be a struct type, as ErrorStatus[StaleBlocks](%d)", typeLabel(t), status, status)
		case slices.ContainsFunc(c.typedErrors, func(o typedError) bool { return o.status == status }):
			c.refuse("ErrorStatus is given %d twice: state the members of a status once, on the route or on its group", status)
		default:
			c.typedErrors = append(c.typedErrors, typedError{status: status, typ: t})
		}
	}
}

// typedError is one ErrorStatus: an error status and the type of the members of its answer.
type typedError struct {
	status int
	typ    reflect.Type
}

// statedError is a typedError as the app keeps it, to tell at run time whether a route states the
// members an error sends.
type statedError struct {
	status int
	typ    reflect.Type
}

// problemMembers are the names the problem body writes itself, so a type given to ErrorStatus
// cannot use them for a field of its own.
var problemMembers = []string{"type", "title", "status", "detail", "instance", "errors", "truncated", "request_id"}

// typedErrorResponse is the response the document states for an ErrorStatus: the problem body
// and the fields of T. A field of T that is a member of the problem is refused here, at
// Registration, with the wire name that clashes.
func (a *App) typedErrorResponse(reg *openapi.Registry, te typedError) (map[string]any, error) {
	mp, err := a.memberPlanOf(te.typ)
	if err != nil {
		return nil, refusalf("ErrorStatus[%s](%d): the type cannot be written as JSON: %v", typeLabel(te.typ), te.status, err)
	}
	for _, n := range problemMembers {
		if _, ok := mp.names[n]; ok {
			return nil, refusalf("ErrorStatus[%s](%d): a field of %s is written as %q, a member the problem body already has (%s): give the field another wire name with a json tag", typeLabel(te.typ), te.status, typeLabel(te.typ), n, strings.Join(problemMembers, ", "))
		}
	}
	schema, err := reg.Schema(te.typ, openapi.Response)
	if err != nil {
		return nil, err
	}
	if a.statedErrors == nil {
		a.statedErrors = map[statedError]bool{}
	}
	a.statedErrors[statedError{te.status, te.typ}] = true
	return map[string]any{
		"description": "An error answer (problem+json) that also carries the members of " + typeLabel(te.typ) + ".",
		"content": map[string]any{problemType: map[string]any{"schema": map[string]any{
			"allOf": []any{problemSchema(), schema},
		}}},
	}, nil
}

// MediaType states the media type of what a Bytes, File or Stream answer of this route sends, so
// the API document says "application/x-ndjson" where it would say any type ("*/*"): the
// handler sets the type at run time, where Registration cannot see it. It is for a route, not a
// group, and is refused on a route whose answers all have a media type the document already
// states (JSON, text, an event stream), and on a media type that is not type/subtype or has a
// wildcard. It states and does not check: an answer of another type is still sent as it is.
func MediaType(media string) RouteOption {
	return func(c *routeConfig) {
		mt, _, err := mime.ParseMediaType(media)
		switch {
		case c.inGroup:
			c.refuse("MediaType is given to a group, but the routes of a group send different types: give it to each route")
		case err != nil || !strings.Contains(mt, "/") || strings.Contains(mt, "*"):
			c.refuse("MediaType %q is not a media type: give type/subtype, with no wildcard, as \"application/x-ndjson\" or \"text/csv; charset=utf-8\"", media)
		default:
			c.mediaType = media
		}
	}
}

// SendsHeader states one response header this route's handler sets on a success answer, or every
// route of a group, so the API document lists it on every success status beside the ones the
// framework states itself (a Created's Location, a File's ETag): T is the Go type of the value
// and gives the header's schema, as it does for a Query field (a string, a number, a bool, a
// named type with Length, Range, Pattern, Format or an Example method), and description says
// what the header means. The header is optional: a handler that always sets it says so in the
// description. It is stated, not checked: a header not named is still sent, and a handler sends
// one only through the Header of the value it returns, as before (ADR 0009).
//
//	bites.SendsHeader[string]("ETag", "The version of the record, for If-Match on the next write.")
//	bites.SendsHeader[int]("X-Total-Count", "How many records match, before paging.")
//
// A group's and a route's headers add up. A name that is not a header name, an empty description,
// Content-Type (MediaType states it), Location (SetsLocation states it), a name given twice
// (case aside, also by a group and its route) and a T that is not a text value (a struct, a
// slice, a map) are refused. When the framework already states the header on an answer (a File's
// ETag), this one replaces it, so its description is the one the document shows.
func SendsHeader[T any](name, description string) RouteOption {
	t := reflect.TypeFor[T]()
	return func(c *routeConfig) {
		switch {
		case !isToken(name):
			c.refuse("SendsHeader is given %q, which is not a header name", name)
		case strings.EqualFold(name, "Content-Type"):
			c.refuse("SendsHeader is given Content-Type, which the media type of the answer states: use MediaType")
		case strings.EqualFold(name, "Location"):
			c.refuse("SendsHeader is given Location, which SetsLocation states for a Created and a redirect states by itself: use SetsLocation")
		case strings.TrimSpace(description) == "":
			c.refuse("SendsHeader %s has no description: say what the header means to a client, as SendsHeader[%s](%q, \"...\")", name, t, name)
		case slices.ContainsFunc(c.sendsHeaders, func(o sentHeader) bool { return strings.EqualFold(o.name, name) }):
			c.refuse("SendsHeader is given %s twice: state a header once, on the route or on its group", name)
		default:
			c.sendsHeaders = append(c.sendsHeaders, sentHeader{name: name, desc: description, typ: t})
		}
	}
}

// sentHeader is one SendsHeader: the header's name, what it means and the type of its value.
type sentHeader struct {
	name, desc string
	typ        reflect.Type
}

// statedAnswers applies MediaType and SendsHeader to the success responses of a route.
func (a *App) statedAnswers(reg *openapi.Registry, responses map[string]any, cfg *routeConfig) error {
	anyType := false
	headers := map[string]map[string]any{}
	for _, h := range cfg.sendsHeaders {
		if _, err := wire.CompileTextWith(h.typ, a.formats); err != nil {
			return refusalf("SendsHeader[%s] is given for %s, but a header value is text and %s cannot be read as text (%v): use a string, a number, a bool or a named type of one", typeLabel(h.typ), h.name, typeLabel(h.typ), err)
		}
		schema, err := a.textSchema(reg, h.typ)
		if err != nil {
			return err
		}
		headers[h.name] = map[string]any{"description": h.desc, "schema": schema}
	}
	for status, r := range responses {
		rm, ok := r.(map[string]any)
		if !ok || status == "default" || status >= "400" {
			continue
		}
		if content, ok := rm["content"].(map[string]any); ok && cfg.mediaType != "" {
			if v, ok := content["*/*"]; ok {
				delete(content, "*/*")
				content[cfg.mediaType] = v
				anyType = true
			}
		}
		if len(cfg.sendsHeaders) == 0 {
			continue
		}
		hs, _ := rm["headers"].(map[string]any)
		if hs == nil {
			hs = map[string]any{}
			rm["headers"] = hs
		}
		for _, h := range cfg.sendsHeaders {
			for k := range hs {
				if strings.EqualFold(k, h.name) {
					delete(hs, k)
				}
			}
			hs[h.name] = maps.Clone(headers[h.name])
		}
	}
	if cfg.mediaType != "" && !anyType {
		return refusalf("MediaType is given, but every answer of the handler has a media type the API document already states (JSON, text or an event stream): MediaType is for a Bytes, File or Stream answer")
	}
	return nil
}

// routeDoc is the record of one typed route: where it sits in the document and its operation,
// with references to schemas not yet named.
type routeDoc struct {
	method  string
	path    string // the OpenAPI path template
	op      map[string]any
	tags    []string
	limited bool // a RateLimiter's middleware is on the route: it can answer 429 (ADR 0069)
	csrf    bool // CSRF is on the route: it can answer 403 to a state-changing method
}

// OperationID sets the operationId of the route's operation in the API document, which a client
// generator turns into the name of the method that calls it (ADR 0020). Without it the id is
// made from the method and the path: GET /users/{id} is getUsersById. It is for a route, not a
// group, and must start with a letter and hold only letters, digits, '_', '.' and '-'. Two
// operations whose ids differ only in case or in those three marks are refused, since a generator
// makes the same method name of both.
func OperationID(id string) RouteOption {
	return func(c *routeConfig) {
		switch {
		case c.inGroup:
			c.refuse("OperationID is given to a group, but an operationId names one operation: give it to each route")
		case !validOperationID(id):
			c.refuse("OperationID %q is not an operationId a client generator can name a method by: start with a letter and use letters, digits, '_', '.' and '-'", id)
		default:
			c.operationID = id
		}
	}
}

func validOperationID(id string) bool {
	if id == "" || !('a' <= id[0] && id[0] <= 'z' || 'A' <= id[0] && id[0] <= 'Z') {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

// operationID is the operationId made from a method and an OpenAPI path template: the method in
// lower case, then each literal segment as words with a capital each, and each wildcard as By and
// its name, so GET /users/{id}/orders is getUsersByIdOrders and GET / is getRoot.
func operationID(method, template string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	n := b.Len()
	words := func(s string) {
		up := true
		for i := 0; i < len(s); i++ {
			c := s[i]
			switch {
			case 'a' <= c && c <= 'z':
				if up {
					c -= 'a' - 'A'
				}
			case 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
			default:
				up = true
				continue
			}
			b.WriteByte(c)
			up = false
		}
	}
	for _, seg := range strings.Split(template, "/") {
		if name, ok := strings.CutPrefix(seg, "{"); ok {
			b.WriteString("By")
			words(strings.TrimSuffix(name, "}"))
			continue
		}
		words(seg)
	}
	if b.Len() == n {
		b.WriteString("Root")
	}
	return b.String()
}

// operationKey is what a client generator keeps of an operationId when it makes a method name:
// the letters in one case and the digits.
func operationKey(id string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '.' || r == '-' {
			return -1
		}
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, id)
}

// checkOperationID refuses an operationId a client generator could not tell from one an earlier
// route has. The Registration lock is held.
func (a *App) checkOperationID(id string, given bool, where string) error {
	key := operationKey(id)
	if a.docOps == nil {
		a.docOps = map[string]docOp{}
	}
	if prev, ok := a.docOps[key]; ok {
		how := "given with OperationID"
		if !prev.given {
			how = "made from its method and path"
		}
		if given {
			return refusalf("OperationID %q names the same method of a generated client as the operationId %q of the route registered at %s (%s): give each operation its own", id, prev.id, prev.where, how)
		}
		return refusalf("the operationId made from the method and path, %q, names the same method of a generated client as the operationId %q of the route registered at %s (%s): give this route its own with OperationID", id, prev.id, prev.where, how)
	}
	a.docOps[key] = docOp{id: id, given: given, where: where}
	return nil
}

// docOp is an operationId of the document and the route that has it.
type docOp struct {
	id, where string
	given     bool
}

// docScheme is one security scheme of the document: its name under components.securitySchemes
// and its entry there.
type docScheme struct {
	name string
	obj  map[string]any
}

// schemeName is the name of the security scheme of c in the document, added the first time a
// route uses it. Two Providers of one scheme share it (the realm is not in OpenAPI); an API key is
// a scheme per header or cookie. The Registration lock is held.
func (a *App) schemeName(c *credential) string {
	obj := c.object()
	for _, s := range a.docAuth {
		if reflect.DeepEqual(s.obj, obj) {
			return s.name
		}
	}
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '-' || r == '_' || r == '.' {
				return r
			}
			return '_'
		}, s)
	}
	var name string
	switch c.typ {
	case "http":
		name = clean(c.scheme) + "Auth"
	case "apiKey":
		switch {
		case c.in == "cookie":
			name = "cookieAuth." + clean(c.name)
		case strings.EqualFold(c.name, defaultAPIKeyHeader):
			name = "apiKeyAuth"
		default:
			name = "apiKeyAuth." + clean(c.name)
		}
	default:
		name = c.typ
	}
	base := name
	for i := 2; slices.ContainsFunc(a.docAuth, func(s docScheme) bool { return s.name == name }); i++ {
		name = base + "." + strconv.Itoa(i)
	}
	a.docAuth = append(a.docAuth, docScheme{name: name, obj: obj})
	return name
}

// security is the operation's security: one Security Requirement for each way through the auth
// Providers the route runs. Every Provider must succeed, so their credentials are required
// together; an AnyAuth offers each of its own as an alternative, and an optional one also offers
// none, the empty requirement. It is nil for a route that runs no auth Provider.
func (a *App) security(provs []*providerDef) []any {
	reqs := [][]string{{}}
	secured := false
	for _, d := range provs {
		if d.auth == nil {
			continue
		}
		secured = true
		var next [][]string
		for _, r := range reqs {
			for _, alt := range d.auth.alts {
				n := slices.Clone(r)
				if alt.cred != nil {
					if name := a.schemeName(alt.cred); !slices.Contains(n, name) {
						n = append(n, name)
					}
				}
				slices.Sort(n)
				if !slices.ContainsFunc(next, func(o []string) bool { return slices.Equal(o, n) }) {
					next = append(next, n)
				}
			}
		}
		reqs = next
	}
	if !secured {
		return nil
	}
	out := make([]any, len(reqs))
	for i, r := range reqs {
		m := map[string]any{}
		for _, name := range r {
			m[name] = []any{}
		}
		out[i] = m
	}
	return out
}

// sameCode reports whether m and ref are method values of one method, whatever their receivers:
// the code of a method value is the method's wrapper.
func sameCode(m Middleware, ref Middleware) bool {
	return reflect.ValueOf(m).Pointer() == reflect.ValueOf(ref).Pointer()
}

// middlewareAnswers reports whether a RateLimiter's middleware and CSRF are among mw.
func middlewareAnswers(mw []Middleware) (limited, csrfOn bool) {
	limit, forgery := (*RateLimiter)(nil).serve, (*csrf)(nil).serve
	for _, m := range mw {
		limited = limited || sameCode(m, limit)
		csrfOn = csrfOn || sameCode(m, forgery) || sameCode(m, (*csrf)(nil).serveAppOrigins)
	}
	return limited, csrfOn
}

// safeMethod is a method CSRF lets through.
func safeMethod(m string) bool { return m == "GET" || m == "HEAD" || m == "OPTIONS" || m == "TRACE" }

// OpenAPI serves the API document of the app's typed routes as application/json at path, for GET
// and HEAD. The document is built once, when the app starts serving, and served as the same
// bytes every time; a route added after that is refused, as always. A route given Undocumented and
// routes on a method OpenAPI 3.1 has no name for are left out of it; a WebSocket route is the get of
// its handshake, with its messages under x-websocket. Calling OpenAPI twice, with a path
// that is not a plain absolute path, or with no Title or Version panics.
func (a *App) OpenAPI(path string, info Info) {
	where := callerWhere()
	label := "OpenAPI " + path
	fail := func(format string, args ...any) {
		panic(&RegistrationError{msg: label + ": " + fmt.Sprintf(format, args...) + "\n\tregistered at " + where})
	}
	switch {
	case !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "{}?#"):
		fail("the path must start with a slash and have no wildcard, query or fragment")
	case strings.TrimSpace(info.Title) == "":
		fail("Info.Title is empty: OpenAPI requires a title")
	case strings.TrimSpace(info.Version) == "":
		fail("Info.Version is empty: OpenAPI requires the version of the API (not of OpenAPI)")
	}
	for i, sv := range info.Servers {
		if msg := checkServerURL(sv.URL); msg != "" {
			fail("Info.Servers[%d].URL %q %s", i, sv.URL, msg)
		}
		for j := range i {
			if info.Servers[j].URL == sv.URL {
				fail("Info.Servers[%d].URL %q is given twice (also Servers[%d])", i, sv.URL, j)
			}
		}
	}
	info.Servers = slices.Clone(info.Servers)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.serving {
		panic(&RegistrationError{msg: label + " registered at " + where + " after the app started serving: routes can be added only before it does"})
	}
	if a.docInfo != nil {
		fail("the document is already served at %s (registered at %s)", a.docPath, a.docWhere)
	}
	rt := a.addRoute("GET", path, label, where)
	a.methods["GET"] = true
	a.docInfo, a.docPath, a.docWhere = &info, path, where
	a.eachBuiltin(rt, BuiltinOpenAPI, path, label, where)
	rt.serve = func(a *App, ex *engine.Exchange, _ *router.Params, _ context.Context, _ *Request) {
		r := ex.Respond(200)
		r.Header("Content-Type", "application/json")
		b := r.Body()
		*b = append(*b, a.doc...)
	}
}

// OpenAPIDocument returns the bytes of the API document, the JSON that OpenAPI serves, without
// serving it: a test that compares the document with a file checked into the repository, or a
// program that writes it, calls this once the routes are registered. It is the document of the
// routes registered so far, so call it after the last one; a route registered afterwards is not
// in the bytes already returned. The returned slice is the caller's. It is an error when OpenAPI
// was never called, and a mistake that only the whole set of types shows (two types given one
// component name) is returned as an error here, where serving panics. It allocates, and is for a
// build step and a test, never a request.
func (a *App) OpenAPIDocument() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.docInfo == nil {
		return nil, fmt.Errorf("bites: OpenAPIDocument is called, but OpenAPI was never called: there is no document to return")
	}
	if a.doc != nil {
		return slices.Clone(a.doc), nil
	}
	doc, err := a.marshalDocument()
	if err != nil {
		return nil, fmt.Errorf("OpenAPI %s: the API document cannot be built: %w\n\tregistered at %s", a.docPath, err, a.docWhere)
	}
	return doc, nil
}

// checkServerURL says why u cannot be the URL of a Server in the document, or "" (ADR 0025).
func checkServerURL(u string) string {
	switch {
	case u == "":
		return "is empty: give the URL the API is served at, such as https://api.example.com or /api"
	case u == "/":
		return `is "/", which OpenAPI reads when servers is absent: leave Servers out`
	case strings.HasSuffix(u, "/"):
		return "ends with a slash, and every path of the document begins with one, so a client would ask for //...: drop the trailing slash"
	case strings.ContainsAny(u, "{}"):
		return "has a server variable, which is not supported: write the URL in full"
	case strings.ContainsAny(u, "?#"):
		return "has a query or a fragment: a server URL is scheme://host[:port][/path] or /path"
	}
	path := u
	if !strings.HasPrefix(u, "/") {
		scheme, rest, ok := strings.Cut(u, "://")
		if !ok || (!strings.EqualFold(scheme, "http") && !strings.EqualFold(scheme, "https")) {
			return "is neither an http or https URL nor a path that starts with a slash"
		}
		host := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			host, path = rest[:i], rest[i:]
		} else {
			path = ""
		}
		if !validHost(host) {
			return "has a host that is not a host name, an IPv4 address or a bracketed IPv6 address with an optional port (a userinfo or a space is refused)"
		}
	}
	if _, ok := cleanPrefix(path); !ok {
		return "has a path that is not an absolute path of RFC 3986 path characters with no empty, \".\" or \"..\" segment"
	}
	return ""
}

// registry is the schema generator of this app, made on first use. The Registration lock is held.
func (a *App) registry() *openapi.Registry {
	if a.api == nil {
		a.api = openapi.New(openapi.Options{
			Names:       a.naming,
			Formats:     a.formats,
			JSONSchemas: a.jsonSchemas,
			Files:       isFormFileType,
			Body: func(t reflect.Type) (reflect.Type, bool) {
				if isOwn(t, "Raw[") {
					return t.Field(0).Type, true
				}
				return nil, false
			},
			Form: func(t reflect.Type) (reflect.Type, bool) {
				if isOwn(t, "Form[") || isMultipart(t) || isSubmitted(t) {
					return t.Field(0).Type, true
				}
				return nil, false
			},
			Descriptions: fileDescriptions,
			DeprecatedFields: func(t reflect.Type) []string {
				return slices.Sorted(maps.Keys(structFieldMap(t, fieldDeprecated)))
			},
			Namer: func(pkgPath, name string) string {
				if a.docInfo == nil || a.docInfo.SchemaName == nil {
					return ""
				}
				return a.docInfo.SchemaName(pkgPath, name)
			},
		})
	}
	return a.api
}

// fieldDescriptions is the Descriptions method of a struct type, if it has one (ADR 0020): the
// descriptions of its fields by Go field name.
func fieldDescriptions(t reflect.Type) map[string]string {
	if t.Kind() != reflect.Struct {
		return nil
	}
	if d, ok := reflect.New(t).Interface().(interface{ Descriptions() map[string]string }); ok {
		return d.Descriptions()
	}
	return nil
}

// structDescriptions is the field descriptions of a struct the API document describes as an object:
// a JSON body, a response, or a struct inside one (ADR 0020). Each struct the codec inlines
// describes its own fields, found through nested embedding, and the outer struct's own method
// wins for a field both describe, as in a text section. Without this, two embedded structs that
// both had the method described nothing, because Go promotes neither.
func structDescriptions(t reflect.Type) map[string]string {
	return structFieldMap(t, fieldDescriptions)
}

// fieldDeprecated is the DeprecatedFields method of a struct type, if it has one (ADR 0020): the
// Go names of its deprecated fields, as a map so it is found through embedding as Descriptions is.
func fieldDeprecated(t reflect.Type) map[string]string {
	if t.Kind() != reflect.Struct {
		return nil
	}
	d, ok := reflect.New(t).Interface().(interface{ DeprecatedFields() []string })
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, n := range d.DeprecatedFields() {
		out[n] = ""
	}
	return out
}

// structFieldMap is structDescriptions for any per-field method: get reads the method of one type.
func structFieldMap(t reflect.Type, get func(reflect.Type) map[string]string) map[string]string {
	if t.Kind() != reflect.Struct {
		return nil
	}
	var merged map[string]string
	for i := range t.NumField() {
		f := t.Field(i)
		if !jsoncodec.Inlined(f) {
			continue
		}
		if sub := structFieldMap(f.Type, get); len(sub) > 0 {
			if merged == nil {
				merged = map[string]string{}
			}
			maps.Copy(merged, sub)
		}
	}
	own := declaredMap(t, get)
	if merged == nil {
		return own
	}
	maps.Copy(merged, own)
	return merged
}

// sectionDescriptions is sectionFieldMap for Descriptions.
func sectionDescriptions(s *textSection) (map[string]string, error) {
	return sectionFieldMap(s, fieldDescriptions, "Descriptions")
}

// sectionFieldMap is the field descriptions (or, with fieldDeprecated, the deprecated fields) of a
// Path, Query, Header or Cookie section, read from the per-field method named method (ADR 0020). Each embedded struct's own Descriptions describes its own fields, and the section
// struct's own Descriptions is its view of the whole and wins over an embedded entry for the same
// field. A name that is not a field of the struct whose method names it is refused.
func sectionFieldMap(s *textSection, get func(reflect.Type) map[string]string, method string) (map[string]string, error) {
	var collect func(t reflect.Type, prefix string, label string) (map[string]string, error)
	collect = func(t reflect.Type, prefix, label string) (map[string]string, error) {
		fields := map[string]bool{}
		var names []string
		for _, f := range s.fields {
			if rest, ok := strings.CutPrefix(f.goPath, prefix+"."); ok {
				n := lastName(rest)
				fields[n] = true
				names = append(names, n)
			}
		}
		merged := map[string]string{}
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
			// Two siblings cannot describe one field: a field of the same name in both is already
			// two parameters with one wire name, which the compiler refused.
			maps.Copy(merged, sub)
		}
		var own map[string]string
		if t.Kind() == reflect.Struct {
			own = declaredMap(t, get)
		}
		for _, k := range sortedKeys(own) {
			if !fields[k] {
				return nil, refusalf("%s.%s names %s, which is not a field of %s (its fields: %s)", label, method, k, typeLabel(t), strings.Join(names, ", "))
			}
			merged[k] = own[k]
		}
		return merged, nil
	}
	label := sectionLabel(s)
	return collect(s.typ, label, label)
}

// docPath turns a route pattern into an OpenAPI path template and its shape, the template with
// the wildcard names taken out: OpenAPI treats two templates of one shape as the same path.
func docPath(p string) (template, shape string) {
	var t, s strings.Builder
	for {
		i := strings.IndexByte(p, '{')
		if i < 0 {
			t.WriteString(p)
			s.WriteString(p)
			break
		}
		j := strings.IndexByte(p[i:], '}')
		if j < 0 {
			t.WriteString(p)
			s.WriteString(p)
			break
		}
		t.WriteString(p[:i])
		s.WriteString(p[:i])
		if name := p[i+1 : i+j]; name != "$" {
			t.WriteString("{" + strings.TrimSuffix(name, "...") + "}")
			s.WriteString("{}")
		}
		p = p[i+j+1:]
	}
	if t.Len() == 0 {
		return "/", "/"
	}
	return t.String(), s.String()
}

var docMethods = map[string]bool{"GET": true, "PUT": true, "POST": true, "DELETE": true, "OPTIONS": true, "HEAD": true, "PATCH": true, "TRACE": true}

// checkShape refuses a route the document cannot hold: one path template that differs from an
// earlier one only in its wildcard names, or one operation twice.
func (a *App) checkShape(method, full, where string) error {
	if !docMethods[method] {
		return nil
	}
	template, shape := docPath(full)
	if a.docShapes == nil {
		a.docShapes = map[string]string{}
	}
	if prev, ok := a.docShapes[shape]; ok {
		pt, pw, _ := strings.Cut(prev, "\x00")
		if pt != template {
			return refusalf("the API document would hold %s and %s as two paths, but OpenAPI treats paths that differ only in the names of their wildcards as one: use the same wildcard names in both (the other is registered at %s)", template, pt, pw)
		}
	} else {
		a.docShapes[shape] = template + "\x00" + where
	}
	key := method + " " + template
	if prev, ok := a.docShapes[key]; ok {
		return refusalf("the API document would hold %s twice: the route registered at %s already describes it", key, prev)
	}
	a.docShapes[key] = where
	return nil
}

// describeRoute builds the record of a route from the types and plans it was compiled from. It
// returns nil for a method the document has no name for.
func (a *App) describeRoute(method, full, where string, inT reflect.Type, in *inputPlan, outT reflect.Type, out *outPlan, cfg *routeConfig, provs []*providerDef, provSecs []*textSection) (*routeDoc, error) {
	if !docMethods[method] {
		return nil, nil
	}
	reg := a.registry()
	template, _ := docPath(full)
	id := cfg.operationID
	if id == "" {
		id = operationID(method, template)
	}
	if err := a.checkOperationID(id, cfg.operationID != "", where); err != nil {
		return nil, err
	}
	op := map[string]any{"operationId": id}
	if cfg.summary != "" {
		op["summary"] = cfg.summary
	}
	if cfg.description != "" {
		op["description"] = cfg.description
	}
	if len(cfg.tags) > 0 {
		op["tags"] = toAny(cfg.tags)
	}
	if cfg.deprecated {
		op["deprecated"] = true
	}
	var params []any
	for _, sec := range []struct {
		s  *textSection
		in string
	}{{in.path, "path"}, {in.query, "query"}, {in.header, "header"}, {in.cookie, "cookie"}} {
		ps, err := a.parameters(reg, sec.s, sec.in)
		if err != nil {
			return nil, err
		}
		params = append(params, ps...)
	}
	// The headers and cookies the route's Providers read are its parameters too; one the Input
	// also reads is stated once.
	for _, s := range provSecs {
		in := secNames[s.kind]
		ps, err := a.parameters(reg, s, in)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			if q := sameParameter(params, p); q != nil {
				if p.(map[string]any)["required"] == true {
					q["required"] = true // the Provider needs it, whatever the Input says
				}
				continue
			}
			params = append(params, p)
		}
	}
	if len(params) > 0 {
		op["parameters"] = params
	}
	if in.body != nil {
		bt, _ := inT.FieldByName("Body")
		rb, err := a.requestBody(reg, in.body, bodyValueType(bt.Type), inT, cfg.unknown)
		if err != nil {
			return nil, err
		}
		op["requestBody"] = rb
	}
	var responses map[string]any
	if out == nil {
		// A WebSocket route (ADR 0020): the upgrade, and the refusals of a handshake (ADR 0046).
		responses = map[string]any{
			"101": map[string]any{"description": "Switching Protocols: the connection is now a WebSocket, whose messages x-websocket describes."},
			"400": map[string]any{"$ref": "#/components/responses/Error"},
			"403": map[string]any{"$ref": "#/components/responses/Error"},
			"426": map[string]any{"$ref": "#/components/responses/Error"},
		}
	} else {
		var err error
		if responses, err = a.responses(reg, outT, out); err != nil {
			return nil, err
		}
	}
	// A Submitted form's mistakes are the handler's Page (ADR 0078), so only the other sections,
	// or a body that is not a form, answer the 400.
	formOnly := in.body != nil && in.body.sub
	hasInput := in.path != nil || in.query != nil || in.header != nil || in.cookie != nil || (in.body != nil && !formOnly) || (in.validates && !formOnly) || len(provSecs) > 0
	if hasInput {
		responses["400"] = map[string]any{"$ref": "#/components/responses/InvalidInput"}
	}
	if in.body != nil && in.body.kind != bodyBytes {
		responses["413"] = map[string]any{"$ref": "#/components/responses/Error"}
		if in.body.kind != bodyReader {
			responses["415"] = map[string]any{"$ref": "#/components/responses/Error"}
		}
	}
	// The credentials the route's Providers read (ADR 0069).
	if sec := a.security(provs); sec != nil {
		op["security"] = sec
		responses["401"] = map[string]any{"$ref": "#/components/responses/Unauthorized"}
		if _, ok := responses["400"]; !ok {
			responses["400"] = map[string]any{"$ref": "#/components/responses/Error"}
		}
	}
	if err := a.statedAnswers(reg, responses, cfg); err != nil {
		return nil, err
	}
	// The error statuses with members (ErrorStatus[T]) come first, so a status the framework
	// already answers on the route is refused rather than quietly replaced.
	for _, te := range cfg.typedErrors {
		key := strconv.Itoa(te.status)
		if _, ok := responses[key]; ok {
			return nil, refusalf("ErrorStatus[%s](%d): the framework already answers %d on this route with a shape of its own, so the document cannot state members for it: use a status the handler decides, such as 409 or 422", typeLabel(te.typ), te.status, te.status)
		}
		r, err := a.typedErrorResponse(reg, te)
		if err != nil {
			return nil, err
		}
		responses[key] = r
	}
	// The error statuses the route says its handler returns (ErrorStatuses). A 429 is the
	// rate limit's answer, with its headers, since RateLimiter.Allow in a Provider sends it.
	for _, st := range cfg.errorStatuses {
		if _, ok := responses[strconv.Itoa(st)]; !ok {
			ref := "#/components/responses/Error"
			if st == 429 {
				ref = "#/components/responses/TooManyRequests"
			}
			responses[strconv.Itoa(st)] = map[string]any{"$ref": ref}
		}
	}
	responses["default"] = map[string]any{"$ref": "#/components/responses/Error"}
	op["responses"] = responses
	rd := &routeDoc{method: method, path: template, op: op, tags: cfg.tags}
	rd.limited, rd.csrf = middlewareAnswers(cfg.mw)
	return rd, nil
}

// sameParameter returns the parameter of params with p's name in p's place, or nil.
func sameParameter(params []any, p any) map[string]any {
	pm, _ := p.(map[string]any)
	for _, q := range params {
		if qm, _ := q.(map[string]any); qm != nil && pm != nil && qm["in"] == pm["in"] && strings.EqualFold(fmt.Sprint(qm["name"]), fmt.Sprint(pm["name"])) {
			return qm
		}
	}
	return nil
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// parameters lists the fields of a text section as OpenAPI parameters. A field a Provider fills is
// not in a section, so it is not a parameter.
func (a *App) parameters(reg *openapi.Registry, s *textSection, in string) ([]any, error) {
	if s == nil {
		return nil, nil
	}
	descs, err := sectionDescriptions(s)
	if err != nil {
		return nil, err
	}
	deprecated, err := sectionFieldMap(s, fieldDeprecated, "DeprecatedFields")
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(s.fields))
	for _, f := range s.fields {
		var schema any
		if f.list >= 0 || f.csv {
			// A CSV is an array in the document whatever its wire form: the style says how it is sent.
			items, err := a.textSchema(reg, f.typ.Elem())
			if err != nil {
				return nil, err
			}
			ls := map[string]any{"type": "array", "items": items}
			if f.items != nil && f.items.Bounded {
				ls["minItems"], ls["maxItems"] = f.items.Min, f.items.Max
			}
			if f.items != nil && f.items.Dup != nil {
				ls["uniqueItems"] = true
			}
			schema = ls
		} else {
			ts, err := a.textSchema(reg, f.typ)
			if err != nil {
				return nil, err
			}
			schema = ts
		}
		if f.hasDef {
			schema = withDefault(schema, f)
		}
		p := map[string]any{"name": f.wire, "in": in, "required": f.required || in == "path", "schema": schema}
		if f.csv {
			// A header's array is always simple and comma-separated; the style names it for a query only.
			if in == "query" {
				p["style"], p["explode"] = "form", false
			} else {
				p["style"] = "simple"
			}
		}
		if d, ok := descs[lastName(f.goPath)]; ok {
			p["description"] = d
		}
		if _, ok := deprecated[lastName(f.goPath)]; ok {
			p["deprecated"] = true
		}
		out = append(out, p)
	}
	return out, nil
}

// withDefault is schema with the field's default beside its type (ADR 0041), in the JSON type the
// schema declares: 50 for an integer, "risk" for a string. The schema may be shared, so it is copied.
func withDefault(schema any, f textField) any {
	m, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	m = maps.Clone(m)
	v := f.defVal
	var val any = f.def
	switch m["type"] {
	case "integer":
		switch v.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			val = v.Int()
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			val = v.Uint()
		}
	case "number":
		if v.CanFloat() {
			val = v.Float()
		}
	case "boolean":
		if v.Kind() == reflect.Bool {
			val = v.Bool()
		}
	}
	m["default"] = val
	return m
}

// sectionLabel is "GetUser.Query", the section's name as a message gives it.
func sectionLabel(s *textSection) string {
	if s.goPath != "" {
		return s.goPath
	}
	return typeLabel(s.typ)
}

func lastName(goPath string) string {
	if i := strings.LastIndexByte(goPath, '.'); i >= 0 {
		return goPath[i+1:]
	}
	return goPath
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// textSchema is the schema of one text value. A type text decodes but the JSON schema generator
// cannot describe is documented as a string, which is what it is on the wire.
// An Example method its type's own rules refuse is the one error returned: it is a mistake of the
// application, and the document would state a value no client could send.
func (a *App) textSchema(reg *openapi.Registry, t reflect.Type) (map[string]any, error) {
	s, err := reg.Schema(t, openapi.Request)
	if err != nil {
		var ee *openapi.ExampleError
		if errors.As(err, &ee) {
			return nil, refusalf("the API description cannot describe %s: %v", typeLabel(t), err)
		}
		return map[string]any{"type": "string"}, nil
	}
	return s, nil
}

// requestBody describes the Body section by the way its type is read (ADR 0044).
func (a *App) requestBody(reg *openapi.Registry, b *bodyPlan, t, input reflect.Type, unknown bool) (map[string]any, error) {
	mode := openapi.Request
	if unknown {
		mode = openapi.RequestOpen
	}
	var content map[string]any
	switch b.kind {
	case bodyBytes:
		content = map[string]any{"*/*": map[string]any{}}
	case bodyReader:
		content = map[string]any{"*/*": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}
	case bodyUpload:
		content = map[string]any{"multipart/form-data": map[string]any{"schema": map[string]any{"type": "object"}}}
	case bodyMultipart:
		// A file is a string with format binary (OpenAPI 3.1), which the registry knows through
		// Options.Files; an Opt of one and a list of them are described as they are for text.
		s, _, err := reg.BodySchema(t, mode, input)
		if err != nil {
			return nil, refusalf("the API description cannot describe the Body: %v", err)
		}
		entry := map[string]any{"schema": s}
		enc := map[string]any{}
		for i := range b.form.fields {
			if f := &b.form.fields[i]; f.file != nil && f.file.accept != nil {
				enc[f.wire] = map[string]any{"contentType": strings.Join(f.file.accept, ", ")}
			}
		}
		if len(enc) > 0 {
			entry["encoding"] = enc
		}
		content = map[string]any{"multipart/form-data": entry}
	default:
		s, media, err := reg.BodySchema(t, mode, input)
		if err != nil {
			var de *openapi.DescriptionError
			if b.kind == bodyForm && !asDescription(err, &de) {
				s, media = map[string]any{"type": "object"}, openapi.MediaForm
			} else {
				return nil, refusalf("the API description cannot describe the Body: %v", err)
			}
		}
		content = map[string]any{media: map[string]any{"schema": s}}
	}
	return map[string]any{"required": !b.opt, "content": content}, nil
}

// bodyValueType is the type a Body section's value is read as: T for an Opt[T] Body, which may be
// absent, and the field's type otherwise.
func bodyValueType(t reflect.Type) reflect.Type {
	if k, elem, _, _ := wire.PresenceOf(t); k == wire.Optional {
		return elem
	}
	return t
}

func asDescription(err error, target **openapi.DescriptionError) bool {
	for err != nil {
		if de, ok := err.(*openapi.DescriptionError); ok {
			*target = de
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// answer is one success response of a route.
// pageErrors is the status of a Page's client-error answer, stated as the range 4XX.
const pageErrors = 4

type answer struct {
	status  int
	media   string // empty when the response has no body
	schema  map[string]any
	headers []string
	item    map[string]any // Events[T]: one event, as the x-itemSchema of its media type
}

// responses describes what the route answers on success: one status for a plain value or a
// wrapper, one for each type a route that returns an interface declared, one for each variant of
// a OneOf.
func (a *App) responses(reg *openapi.Registry, t reflect.Type, p *outPlan) (map[string]any, error) {
	var answers []answer
	if p.dynamic {
		for _, v := range p.variants {
			as, err := a.answersOf(reg, v.t, v.plan)
			if err != nil {
				return nil, err
			}
			answers = append(answers, as...)
		}
	} else if p.alts != nil {
		for _, al := range p.alts {
			as, err := a.answersOf(reg, al.t, al.plan)
			if err != nil {
				return nil, err
			}
			answers = append(answers, as...)
		}
	} else {
		as, err := a.answersOf(reg, t, p)
		if err != nil {
			return nil, err
		}
		answers = as
	}
	type bucket struct {
		media   []string
		schemas map[string][]any
		headers []string
		items   map[string][]any // the x-itemSchema of each media type, from Events[T]
		bare    map[string]bool  // a media type an answer gave no item for: then none is stated
	}
	byStatus := map[int]*bucket{}
	for _, an := range answers {
		b := byStatus[an.status]
		if b == nil {
			b = &bucket{schemas: map[string][]any{}, items: map[string][]any{}, bare: map[string]bool{}}
			byStatus[an.status] = b
		}
		for _, h := range an.headers {
			if !slices.Contains(b.headers, h) {
				b.headers = append(b.headers, h)
			}
		}
		if an.media == "" {
			continue
		}
		if _, seen := b.schemas[an.media]; !seen {
			b.media = append(b.media, an.media)
		}
		var sv any
		if an.schema != nil {
			sv = an.schema
		}
		dup := false
		for _, s := range b.schemas[an.media] {
			dup = dup || reflect.DeepEqual(s, sv)
		}
		if !dup {
			b.schemas[an.media] = append(b.schemas[an.media], sv)
		}
		if an.item == nil {
			b.bare[an.media] = true
		} else if !slices.ContainsFunc(b.items[an.media], func(it any) bool { return reflect.DeepEqual(it, an.item) }) {
			b.items[an.media] = append(b.items[an.media], an.item)
		}
	}
	out := map[string]any{}
	for status, b := range byStatus {
		r := map[string]any{"description": engine.StatusText(status)}
		key := strconv.Itoa(status)
		if status == pageErrors {
			r["description"] = "A page shown again for a client error, such as a form with what was wrong in it."
			key = "4XX"
		}
		if len(b.media) > 0 {
			content := map[string]any{}
			for _, m := range b.media {
				ss := b.schemas[m]
				var mt map[string]any
				switch {
				case ss[0] == nil && len(ss) == 1:
					mt = map[string]any{}
				case len(ss) == 1:
					mt = map[string]any{"schema": ss[0]}
				default:
					mt = map[string]any{"schema": map[string]any{"oneOf": ss}}
				}
				switch its := b.items[m]; {
				case b.bare[m] || len(its) == 0:
				case len(its) == 1:
					mt["x-itemSchema"] = its[0]
				default:
					mt["x-itemSchema"] = map[string]any{"oneOf": its}
				}
				content[m] = mt
			}
			r["content"] = content
		}
		if len(b.headers) > 0 {
			sort.Strings(b.headers)
			hs := map[string]any{}
			for _, h := range b.headers {
				hd := map[string]any{"schema": map[string]any{"type": "string"}}
				if status == 201 && h == "Location" {
					// Only a route registered with SetsLocation states the header at all (ADR 0013):
					// a Created sends Location when its field is not empty, and most handlers
					// leave it empty. Such a route sends it always, so the header is required.
					hd["description"] = "The URL of the created resource."
					hd["required"] = true
				}
				hs[h] = hd
			}
			r["headers"] = hs
		}
		out[key] = r
	}
	if _, ok := byStatus[206]; ok {
		// Only a File answers 206; it also refuses a failed precondition and a range it cannot serve.
		out["412"] = map[string]any{"$ref": "#/components/responses/Error"}
		out["416"] = map[string]any{"$ref": "#/components/responses/Error"}
	}
	return out, nil
}

// answersOf is the success responses of one output type with its plan (ADR 0013, ADR 0043,
// ADR 0030). A header or cookie a handler sets at run time is not in the document: the types do
// not say which.
func (a *App) answersOf(reg *openapi.Registry, t reflect.Type, p *outPlan) ([]answer, error) {
	body := func(bt reflect.Type) (map[string]any, error) {
		s, err := reg.Schema(bt, openapi.Response)
		if err != nil {
			return nil, refusalf("the API description cannot describe the response %s: %v", typeLabel(bt), err)
		}
		return s, nil
	}
	wrapped := func(status int, headers ...string) ([]answer, error) {
		f, _ := t.FieldByName("Body")
		s, err := body(f.Type)
		if err != nil {
			return nil, err
		}
		return []answer{{status: status, media: openapi.MediaJSON, schema: s, headers: headers}}, nil
	}
	switch p.wk {
	case wrapNone:
		s, err := body(t)
		if err != nil {
			return nil, err
		}
		return []answer{{status: 200, media: openapi.MediaJSON, schema: s}}, nil
	case wrapOK:
		return wrapped(200)
	case wrapCreated:
		if p.mustLoc {
			return wrapped(201, "Location")
		}
		return wrapped(201)
	case wrapAccepted:
		return wrapped(202)
	case wrapNoContent:
		return []answer{{status: 204}}, nil
	case wrapNotModified:
		return []answer{{status: 304}}, nil
	case wrapSeeOther:
		return []answer{{status: 303, headers: []string{"Location"}}}, nil
	case wrapRedirect:
		var as []answer
		for _, s := range []int{301, 302, 307, 308} {
			as = append(as, answer{status: s, headers: []string{"Location"}})
		}
		return as, nil
	case wrapText:
		return []answer{{status: 200, media: "text/plain", schema: map[string]any{"type": "string"}}}, nil
	case wrapPage:
		// A Page is the page itself on success and the form shown again on a client error
		// (ADR 0078); which 4xx is the handler's, so the range is stated.
		html := map[string]any{"type": "string"}
		return []answer{{status: 200, media: "text/html", schema: html}, {status: pageErrors, media: "text/html", schema: html}}, nil
	case wrapSSE:
		return []answer{{status: 200, media: "text/event-stream", schema: map[string]any{"type": "string"}}}, nil
	case wrapEvents:
		// OpenAPI 3.1 has no keyword for the items of a stream; 3.2's itemSchema is stated under
		// the extension x-itemSchema, in the shape 3.2 gives an event, with T's JSON as its data.
		s, err := body(p.events.eventType())
		if err != nil {
			return nil, err
		}
		item := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":    map[string]any{"type": "string"},
				"event": map[string]any{"type": "string"},
				"retry": map[string]any{"type": "integer", "minimum": 0},
				"data":  map[string]any{"type": "string", "contentMediaType": openapi.MediaJSON, "contentSchema": s},
			},
			"required": []any{"data"},
		}
		return []answer{{status: 200, media: "text/event-stream", schema: map[string]any{"type": "string"}, item: item}}, nil
	case wrapFile:
		// What File itself sends (ADR 0043): its validators, ranges, and the name of a download.
		// The content type is inferred from the file's name at run time, so any is allowed unless
		// the route states it with MediaType.
		hs := []string{"Accept-Ranges", "Content-Disposition", "ETag", "Last-Modified"}
		return []answer{
			{status: 200, media: "*/*", headers: hs},
			{status: 206, media: "*/*", headers: append(slices.Clone(hs), "Content-Range")},
			{status: 304, headers: []string{"ETag", "Last-Modified"}},
		}, nil
	}
	// Bytes and Stream: the content type is the handler's, so any is allowed unless the route
	// states it with MediaType.
	return []answer{{status: 200, media: "*/*"}}, nil
}

// buildDocument puts the document together and keeps its bytes, once, when the app starts
// serving. The Registration lock is held. A failure here is a Registration mistake that only the
// whole set of types can show (two types that cannot be told apart by name), so it panics with
// the line that asked for the document.
func (a *App) buildDocument() {
	if a.docInfo == nil {
		return
	}
	doc, err := a.marshalDocument()
	if err != nil {
		panic(&RegistrationError{msg: "OpenAPI " + a.docPath + ": the API document cannot be built: " + err.Error() + "\n\tregistered at " + a.docWhere})
	}
	a.doc = doc
}

func (a *App) marshalDocument() ([]byte, error) {
	reg := a.registry()
	paths := map[string]any{}
	var tags []string
	// App middleware runs for every request, so a RateLimiter or CSRF given to App.Use, which may
	// come after the routes, is read here rather than at each route's Registration.
	appLimited, appCSRF := middlewareAnswers(a.mw)
	used := map[string]bool{}
	for _, d := range a.docs {
		rs := d.op["responses"].(map[string]any)
		if d.limited || appLimited {
			rs["429"] = map[string]any{"$ref": "#/components/responses/TooManyRequests"}
		}
		if (d.csrf || appCSRF) && !safeMethod(d.method) {
			if _, ok := rs["403"]; !ok {
				rs["403"] = map[string]any{"$ref": "#/components/responses/Error"}
			}
		}
		for _, r := range rs {
			if ref, ok := r.(map[string]any)["$ref"].(string); ok {
				used[strings.TrimPrefix(ref, "#/components/responses/")] = true
			}
		}

		item, _ := paths[d.path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[d.path] = item
		}
		item[strings.ToLower(d.method)] = d.op
		for _, t := range d.tags {
			if !slices.Contains(tags, t) {
				tags = append(tags, t)
			}
		}
	}
	info := map[string]any{"title": a.docInfo.Title, "version": a.docInfo.Version}
	if a.docInfo.Description != "" {
		info["description"] = a.docInfo.Description
	}
	doc := map[string]any{"openapi": "3.1.0", "info": info, "paths": paths}
	if len(a.docInfo.Servers) > 0 {
		ss := make([]any, len(a.docInfo.Servers))
		for i, sv := range a.docInfo.Servers {
			m := map[string]any{"url": sv.URL}
			if sv.Description != "" {
				m["description"] = sv.Description
			}
			ss[i] = m
		}
		doc["servers"] = ss
	}
	if len(tags) > 0 {
		sort.Strings(tags)
		ts := make([]any, len(tags))
		for i, t := range tags {
			ts[i] = map[string]any{"name": t}
		}
		doc["tags"] = ts
	}
	resolved, err := reg.Resolve(doc)
	if err != nil {
		return nil, err
	}
	schemas, err := reg.Components()
	if err != nil {
		return nil, err
	}
	responses := errorResponses()
	for _, name := range []string{"Unauthorized", "TooManyRequests"} {
		if !used[name] {
			delete(responses, name)
		}
	}
	comps := map[string]any{"responses": responses}
	if len(schemas) > 0 {
		comps["schemas"] = schemas
	}
	if len(a.docAuth) > 0 {
		ss := map[string]any{}
		for _, s := range a.docAuth {
			ss[s.name] = s.obj
		}
		comps["securitySchemes"] = ss
	}
	resolved["components"] = comps
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(resolved); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// problemProps are the members every problem body can have, with extra added.
func problemProps(extra map[string]any) map[string]any {
	str := map[string]any{"type": "string"}
	p := map[string]any{
		"title":      str,
		"status":     map[string]any{"type": "integer"},
		"detail":     str,
		"request_id": str,
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// problemSchema is the schema of the answer of ADR 0012, without a list of errors.
func problemSchema() map[string]any {
	return map[string]any{"type": "object", "required": []any{"title", "status"}, "properties": problemProps(nil)}
}

// errorResponses are the problem+json answers of ADR 0012, shared by every operation. They are
// components of their own kind, so a type of the application named Error or InvalidInput cannot
// collide with them.
func errorResponses() map[string]any {
	str := map[string]any{"type": "string"}
	props := problemProps
	problem := problemSchema()
	invalid := map[string]any{"type": "object", "required": []any{"title", "status", "errors"}, "properties": props(map[string]any{
		"errors": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "required": []any{"location", "message"},
			"properties": map[string]any{
				"location": str, "message": str,
				"rule": map[string]any{"type": "string", "description": "The machine-readable code of the message: type, required, length, range, items, nonempty, positive, values or check. Absent when the message has none."},
				"min":  map[string]any{"type": "integer", "description": "The lower bound of a length, range or items rule. Absent when the rule has no lower bound."},
				"max":  map[string]any{"type": "integer", "description": "The upper bound of a length, range or items rule. Absent when the rule has no upper bound."},
			},
		}},
		"truncated": map[string]any{"type": "boolean"},
	})}
	content := func(s map[string]any) map[string]any {
		return map[string]any{problemType: map[string]any{"schema": s}}
	}
	header := func(s map[string]any, description string) map[string]any {
		return map[string]any{"description": description, "schema": s}
	}
	return map[string]any{
		"Error":        map[string]any{"description": "An error answer (problem+json).", "content": content(problem)},
		"InvalidInput": map[string]any{"description": "The request does not fit the route's input: every error found is listed with its location.", "content": content(invalid)},
		"Unauthorized": map[string]any{
			"description": "The request has no credential of the route's scheme, or one that is not valid.",
			"headers":     map[string]any{"WWW-Authenticate": header(str, "The challenge: the scheme and realm to authenticate with, and error=\"invalid_token\" for a Bearer token that is not valid.")},
			"content":     content(problem),
		},
		"TooManyRequests": map[string]any{
			"description": "The client has made more requests than its rate limit allows.",
			"headers": map[string]any{
				"Retry-After":      header(map[string]any{"type": "integer"}, "Seconds until a request will be allowed."),
				"RateLimit-Policy": header(str, "The limit's policy: its name, quota and window."),
				"RateLimit":        header(str, "What is left of the limit, and when it resets."),
			},
			"content": content(problem),
		},
	}
}

// fileDescriptions is structDescriptions with one more sentence for a file field whose type
// states MaxSize: OpenAPI 3.1 has no keyword for the bytes of a binary string, so the field's
// description says it (ADR 0044).
func fileDescriptions(t reflect.Type) map[string]string {
	d := structDescriptions(t)
	if t.Kind() != reflect.Struct {
		return d
	}
	for i := range t.NumField() {
		f := t.Field(i)
		ft := f.Type
		if k, elem, _, _ := wire.PresenceOf(ft); k == wire.Optional {
			ft = elem
		} else if ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if !isFormFileType(ft) || !f.IsExported() {
			continue
		}
		ms, ok := reflect.New(ft).Interface().(interface{ MaxSize() int64 })
		if !ok {
			continue
		}
		note := "At most " + strconv.FormatInt(ms.MaxSize(), 10) + " bytes."
		if d == nil {
			d = map[string]string{}
		} else {
			d = maps.Clone(d)
		}
		if prev := d[f.Name]; prev != "" {
			note = prev + " " + note
		}
		d[f.Name] = note
	}
	return d
}
