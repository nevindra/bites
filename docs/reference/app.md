# App, groups, options and limits

**The App that holds the routes and serves them, the Group that shares a prefix and options, the options of New, and the Limits every app has.**

**Guide:** [routing](../guide/routing.md) · **Design:** [routing](../design/routing.md)

An `App` is a route table and the engine that serves it. Routes are registered with the generic methods `Get`, `Post` and the others, which read the handler's Input and Output types once and refuse any mistake in them with a panic before the first request ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)). A `Group` is a path prefix and the options its routes share. Everything is set on the instance and nothing is global ([ADR 0026](../adr/0026-no-global-state-and-one-behaviour.md)).

The generic methods are Go 1.27 syntax: the type arguments `In` and `Out` are inferred from the handler, so a call reads `app.Get("/users/{id}", users.Get)`. A handler is a plain function or a method value of the shape `func(context.Context, In) (Out, error)`. What `In` may be is in [input](./input.md), what `Out` may be is in [responses](./responses.md), and how an error becomes an answer is in [errors](./errors.md).

<!-- compiles: decls -->

```go
type GetUser struct {
	Path struct{ ID int64 }
}

type User struct {
	ID   int64
	Name string
}

func getUser(ctx context.Context, in GetUser) (User, error) {
	if in.Path.ID == 0 {
		return User{}, bites.NotFound("no such user")
	}
	return User{ID: in.Path.ID, Name: "Ada"}, nil
}
```

<!-- compiles: body -->

```go
app = bites.New(bites.WithLimits(bites.Limits{MaxBody: 64 << 10}))
admin := app.Group("/admin", bites.MaxBody(1<<20))
admin.Get("/users/{id}", getUser)
app.Get("/users/{id}", getUser, bites.Summary("One user"))
```

## What Registration refuses

A route that cannot work panics with a `*RegistrationError` whose text is the route, the reason and where it was registered ([`RegistrationError`](#registrationerror)). Every row is asserted whole in the refusal table, `refusal_test.go`, named `TestEveryMistakeIsRefusedAtRegistrationWithTheMessageWrittenForIt`. The rows that concern the route methods and groups are:

| Mistake | Message (after the route label, then the line `registered at file.go:N`) |
|---|---|
| a nil handler | `the handler is nil` |
| a typed route with no method (`Handle("", ...)`) | `a typed route needs a method; a method-less pattern is for mounting a plain http.Handler` |
| a method that is not a token | `invalid method "GE T" registered at file.go:N: a method is a token` |
| a pattern without a leading slash | `invalid pattern "x" registered at file.go:N: must start with '/'` |
| two patterns that match the same requests | `pattern "GET /dup" (registered at file.go:N) conflicts with pattern "GET /dup" (registered at file.go:M): they match the same requests` |
| a group prefix without a slash, or with a trailing one or a wildcard | `group "admin": a group prefix starts with a slash, does not end with one, and has no wildcard; to give options to routes that share no prefix, bundle them once with bites.Options(...) and pass that to each route` |
| a limit option that cannot work | `MaxBody(0) is not a limit: it must be a positive number of bytes` (and the same for `BodyTimeout`, `MaxInFlight`, `MaxParts`, `MaxMessage`) |
| a nil option | `an option is nil` |
| a `Service` with no name, with no function, registered twice, or added after the app started serving; `WithServiceTimeout` that is not positive or given twice | see [`Service`](#service) and [`WithServiceTimeout`](#withservicetimeout) |
| a route or group added after the app started serving | `GET /x registered at file.go:N after the app started serving: routes can be added only before it does` |

The mistakes in the Input and Output types are in [input](./input.md) and [responses](./responses.md), those of Providers in [providers](./providers.md), those of middleware in [middleware](./middleware.md), those of WebSocket routes in [websocket](./websocket.md) and those of the API document in [openapi](./openapi.md).

## Creating an app

### `New`

```go
func New(opts ...Option) *App
```

New returns an App with no routes. The options are applied in order. A negative field in `WithLimits`, a `MinSize` below 1 KiB in `WithGzip`, `WithGzip` given twice, and a bad `WithAllowOrigins` entry panic here, naming the option. The defaults are the logger `slog.Default()`, the real clock, the naming policy `SnakeCase`, no compression, parking on, and the limits of [`Limits`](#limits).

Guide: [getting started](../guide/getting-started.md). Decision: [ADR 0026](../adr/0026-no-global-state-and-one-behaviour.md).

### `Option`

```go
type Option func(*App)
```

An Option configures an App: everything is set on the instance. The options are `WithClock`, `WithErrorMapper` ([errors](./errors.md#witherrormapper)), `WithGzip`, `WithLimits`, `WithLogger`, `WithNaming`, `WithoutParking`, `WithRejectUnknownQueryKeys`, `WithTextFormat`, `WithJSONSchema`, and for WebSockets and `CSRF` `WithAllowOrigins` (the app's list of origins) and `WithAllowAnyOrigin`, and for WebSockets alone `WithGoAwaySpread` ([websocket](./websocket.md)).

### `App`

```go
type App struct {
	// Has unexported fields.
}
```

An App is a set of routes and the server that serves them. The zero value is not usable; call `New`. Routes, groups, middleware and Providers can be added only before the app starts serving (the first `Serve`, `Listen`, `ServeTLS` or `ListenTLS`, or `bitetest.Start`); adding one afterwards panics ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)). An App is safe to serve from more than one listener at once.

## Options

### `WithClock`

```go
func WithClock(c Clock) Option
```

WithClock sets the clock the app measures its timeouts on, so a test moves time instead of waiting for it. The default is the real clock. `bitetest.NewClock` returns one that moves only when told to ([bitetest](./bitetest.md#bitetestnewclock)).

Guide: [testing](../guide/testing.md). Decision: [ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md).

### `WithCompression`

```go
func WithCompression(c Compression) Option
```

WithCompression turns response compression on with the encoders `c.Encoders` lists, in the server's order of preference: a body of at least `c.MinSize` is compressed with the registered coding the request's `Accept-Encoding` weighs highest, and on a tie with the one listed first. An empty list is gzip alone. It is off without this option or `WithGzip`. The engine compresses the finished body, so a middleware sees the uncompressed answer, and adds `Vary: Accept-Encoding` to every answer. A compressed typed answer is 2 allocations with gzip, zstd or brotli ([ADR 0003](../adr/0003-the-trade-budget.md)). The start-up log has a `compression` line listing the codings in order.

It panics in `New` when given twice or beside `WithGzip` (`WithCompression: given after WithGzip (first at file.go:N, again at file.go:M): give one Compression that lists every Encoder, in the order the server prefers`), when `MinSize` is from 1 to 1,023 or negative, for a nil `Encoder` (`WithCompression: Encoders[1] is nil`), for a `Coding` that is not a lowercase token or is `identity` or `*` (`WithCompression: Encoders[0] has the coding "Br", which is not a lowercase content coding token such as "br", "zstd" or "gzip"`), and for a coding listed twice (`WithCompression: Encoders[2] has the coding "gzip", as Encoders[1] has: list each coding once, in the order the server prefers`).

<!-- compiles: body -->

```go
app = bites.New(bites.WithCompression(bites.Compression{
	Encoders: []bites.Encoder{bites.GzipEncoder()}, // brotli.Encoder(), zstd.Encoder() first, from the modules
}))
```

Guide: [deploying](../guide/deploying.md#compressing-responses). Decisions: [ADR 0031](../adr/0031-gzip-only-and-off-by-default.md), [ADR 0061](../adr/0061-compression-chooses-among-the-apps-encoders-by-the-clients-weights-then-the-apps-order.md).

### `WithGzip`

```go
func WithGzip(g Gzip) Option
```

WithGzip turns response compression on with gzip alone: it is `WithCompression(Compression{MinSize: g.MinSize})`. A body of at least `Gzip.MinSize` is compressed with gzip when the request's `Accept-Encoding` accepts it. It is off without this option, because the reference workload usually sits behind a load balancer that compresses. It costs no allocation of its own (pooled in the engine, [ADR 0003](../adr/0003-the-trade-budget.md): 2 allocations, the same as plain).

It panics in `New` when given twice (`WithGzip: given twice (first at file.go:N, again at file.go:M): give one Gzip, because two could say different things`) or after `WithCompression`, and when `MinSize` is from 1 to 1,023 or negative (`WithGzip: Gzip.MinSize is 512, which is below the floor of 1024 bytes: ...`).

Decision: [ADR 0031](../adr/0031-gzip-only-and-off-by-default.md).

### `WithLimits`

```go
func WithLimits(l Limits) Option
```

WithLimits sets the limits of [`Limits`](#limits). A zero field keeps its default. A negative field makes `New` panic, because no limit can be switched off: `WithLimits: Limits.MaxHead is -1, which is not a limit: a limit cannot be switched off, so give a positive number of bytes, or zero for the default of 8 KiB`. Every field has such a row in the refusal table.

Guide: [limits and timeouts](../guide/limits.md). Decision: [ADR 0024](../adr/0024-limits-are-on-by-default.md).

### `WithLogger`

```go
func WithLogger(l *slog.Logger) Option
```

WithLogger sets the logger for the start-up settings (each limit, marked `default` or `custom`), handler errors that are logged, and recovered panics. The default is `slog.Default()`. `AccessLog` uses it when its own `Logger` is nil.

### `WithNaming`

```go
func WithNaming(p NamingPolicy) Option
```

WithNaming sets the one naming policy of the app: the function that turns a Go field name into the name a client sends, in paths, queries, cookies, bodies, error locations and the API document. The default is `SnakeCase`. Headers are not under the policy: a header field's name becomes its canonical kebab form (`RequestID` is `Request-Id`).

Decision: [ADR 0011](../adr/0011-one-naming-policy-for-wire-names.md).

### `App.WriteWireTable`

```go
func (a *App) WriteWireTable(w io.Writer) error
```

WriteWireTable writes, route by route in registration order, every name a client sees. Each row is the section (`path`, `query`, `header`, `cookie`, `body`, or `response 201` for a success answer), the Go field path, the wire name and the Go type. A body or response is walked through nested structs, lists and maps, so a row reads `lines[].skuCode` for `Body.Lines[].SKUCode`, and a type that contains itself is listed once and then named `(recursive: T)`. An `Opt` in a text section is marked `(optional)`. Undocumented routes are included, since they are decoded the same way.

The names are read from the plans Registration compiled, with the app's own naming policy, `WireNames` methods, `json` tags and text formats, so the table cannot disagree with the decoder, the encoder or the API document, and it works for a policy that only the application knows. The error is the writer's. It allocates freely and is for a test, a small `main` or a review, never a request. It lists typed routes only; a WebSocket route's sections and a response wrapper's headers are not in it.

```text
POST /units/{orgUnitId}/orders  (registered at orders.go:31)
  SECTION       GO FIELD                       WIRE NAME        TYPE
  path          PlaceOrder.Path.OrgUnitID      orgUnitId        string
  query         PlaceOrder.Query.PageSize      pageSize         int (optional)
  header        PlaceOrder.Header.RequestID    Request-Id       string (optional)
  body          PlaceOrder.Body.ShipTo.City    shipTo.city      string
  body          PlaceOrder.Body.Lines[].SKU    lines[].sku      string
  response 201  Created.Body.OrderID           orderId          string
```

Guide: [seeing the names a client sends](../guide/requests.md#seeing-the-names-a-client-sends). Decision: [ADR 0011](../adr/0011-one-naming-policy-for-wire-names.md).

### `WithTextFormat`

```go
func WithTextFormat[T any](format string) Option
```

WithTextFormat registers a type from another module as text with an OpenAPI format, for the type the core cannot import, such as `github.com/google/uuid`'s `UUID`. `*T` must implement `encoding.TextUnmarshaler` and `T` or `*T` must implement `encoding.TextMarshaler`. The type is then read from a Path, Query, Header or Cookie, from a `Form` and from a JSON body with its own `UnmarshalText`, written with its own `MarshalText`, and described as `type: string` with the format given, as a parameter, a request field and a response field. A value that does not decode is a 400 with the sentence of the format and the rule `type`: `expected a UUID` for `"uuid"`, `expected a date as YYYY-MM-DD` for `"date"`, `expected a valid <format>` for any other. The type's own error text never reaches the client. Without the registration the same type decodes but says `expected a valid value of type uuid.UUID` (its Go type) and is described without a format.

A type registered here costs what a `TextUnmarshaler` costs: two allocations per value in a text section. A type named `UUID` in a package whose path holds `uuid` (other than the standard library's), read by a text field with no registration, is named once in the start-up log at Info, with its first field and this option written out, since it would otherwise answer `expected a valid value of type <package>.UUID` with nothing saying why.

It is refused when `New` runs, naming the line that wrote the option, for an empty format, an interface or pointer type, a type the core already knows (`time.Time`, the standard library's `uuid.UUID`, `Date`), a type whose pointer has no `UnmarshalText`, a type with no `MarshalText`, and a type registered twice with two formats.

<!-- compiles: decls -->

```go
type exampleID [16]byte // stands for another module's uuid type

func (id exampleID) MarshalText() ([]byte, error) { return []byte(hex.EncodeToString(id[:])), nil }

func (id *exampleID) UnmarshalText(b []byte) error {
	_, err := hex.Decode(id[:], b)
	return err
}

func newApp() *bites.App {
	return bites.New(bites.WithTextFormat[exampleID]("uuid"))
}
```

Decision: [ADR 0042](../adr/0042-text-values-are-decoded-by-type.md).

### `WithJSONSchema`

```go
func WithJSONSchema[T any](s JSONSchema) Option
```

WithJSONSchema registers the schema of a type that encodes or decodes itself through its own JSON methods, such as pgx's `pgtype.Timestamptz`, which the codec delegates to and the API document cannot look inside. `T` or `*T` must have `MarshalJSON`, `UnmarshalJSON` or the `encoding/json/v2` equivalents; a type with text methods alone is [`WithTextFormat`](#withtextformat)'s. The schema is then stated wherever `T` appears: a `Body` field, an Output field, a slice or array element, a map value, inside `Opt` and `Null`, and in an `Events` or `Socket` message. The type's own methods still read and write the value; the registration only states it.

Without a registration such a field is refused when the route is registered, because the document would describe it as `{}` and a client generated from it would say `unknown`. The refusal names the route, the field, the type, the direction and this option: `POST /x: Body.At: pgtype.Timestamptz decodes itself through its own UnmarshalJSON, so the API document cannot see its shape and would describe it as {}: register it with WithJSONSchema[pgtype.Timestamptz](JSONSchema{Type: ..., Format: ...}), or with JSONSchema{Type: "any"} if it really is free-form JSON`. In an Output the words are `encodes itself through its own MarshalJSON`. `json.RawMessage` and `jsontext.Value` are free-form by name and need no registration; an `any` field is explicit and unchanged.

It is refused when `New` runs, naming the line that wrote the option, for an interface or pointer type, a type the core already knows (`time.Time`, the standard library's `uuid.UUID`, `Date`, `json.RawMessage`, `jsontext.Value`), a type with no JSON method, a `Type` that is not a JSON type (including an empty one), a `Format` on an object, array, boolean or free-form type, `Nullable` on a free-form type, a type registered twice with two schemas, and a type also registered with `WithTextFormat`.

It costs nothing per request: the check is made at Registration and the schema is read when the document is built.

<!-- compiles: decls -->

```go
type stamp struct{ at time.Time } // stands for another module's timestamp, which writes null when unset

func (s stamp) MarshalJSON() ([]byte, error) { return s.at.MarshalJSON() }

func (s *stamp) UnmarshalJSON(b []byte) error { return s.at.UnmarshalJSON(b) }

func newJSONApp() *bites.App {
	return bites.New(bites.WithJSONSchema[stamp](bites.JSONSchema{Type: "string", Format: "date-time", Nullable: true}))
}
```

Decision: [ADR 0076](../adr/0076-a-type-that-writes-its-own-json-is-described-by-a-registration-or-refused.md).

### `JSONSchema`

```go
type JSONSchema struct {
	Type     string
	Format   string
	Nullable bool
}
```

JSONSchema is the shape [`WithJSONSchema`](#withjsonschema) declares for a type that writes its own JSON. `Type` is `string`, `number`, `integer`, `boolean`, `object` or `array`, or `any` for a value that is free-form JSON, described as `{}`; leaving it empty is refused rather than read as free-form. `Format` is the OpenAPI format of a string, number or integer. `Nullable` says the type's own method can write `null`, so a response is described as `type: [<Type>, "null"]`; a request is not, because the decoder refuses `null` for a field that is not a [`Null`](./input.md#null) before the type is asked.

### `WithRejectUnknownQueryKeys`

```go
func WithRejectUnknownQueryKeys() Option
```

WithRejectUnknownQueryKeys makes every route answer a 400 for a query key its `Query` section does not name, located `query.<key>`. A route without a `Query` section is held to it too. [`RejectUnknownQueryKeys`](./input.md#rejectunknownquerykeys) does the same for one route or group, and [`AcceptUnknownQueryKeys`](./input.md#acceptunknownquerykeys) undoes it for one. The start-up log says `unknown query keys` is `rejected` and marks it `custom`. Decision: [ADR 0042](../adr/0042-text-values-are-decoded-by-type.md).

### `WithServiceTimeout`

```go
func WithServiceTimeout(d time.Duration) Option
```

WithServiceTimeout sets how long the app waits for one [`Service`](#service)'s `Start` to return, and for one `BeforeShutdown` or `Stop` that is called with a context already done (every one under `Close`, the rest after Shutdown's deadline has passed). The default is 30 seconds. A function that has not returned by then is abandoned: logged, and the next one goes on. The start-up log says `service timeout` is `default` or `custom` when the app has services. It panics in `New` for zero or a negative `d` (`app: WithServiceTimeout(0) is not a time to wait: give a positive duration, or leave it out for 30s`) and when given twice (`app: WithServiceTimeout is given twice`).

Guide: [deploying](../guide/deploying.md#services-that-live-as-long-as-the-app). Decision: [ADR 0079](../adr/0079-services-start-after-the-app-binds-and-stop-in-reverse-around-shutdown.md).

### `WithoutParking`

```go
func WithoutParking() Option
```

WithoutParking keeps every idle connection on a goroutine of its own. By default a connection idle for 100 ms is parked on the engine's epoll and its goroutine ends, which takes about 2 KB off every idle connection ([ADR 0005](../adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md)); one that is woken pays a goroutine start and an epoll registration. Parking is used only where it can be (Linux, TCP). This option is the way to compare against it, or to go back should it misbehave.

### `WithTrustedProxies`

```go
func WithTrustedProxies(p TrustedProxies) Option
```

WithTrustedProxies names the proxies in front of the app. [`Request.ClientAddr`](./middleware.md#requestclientaddr) then reads the client's address from `p.Header` when, and only when, the connection comes from one of `p.Prefixes` (or from a Unix socket's peer, with `p.UnixSockets`), and [`Request.Scheme`](./middleware.md#requestscheme) reads `X-Forwarded-Proto` on the same condition; with `p.ForwardedHost` and `p.ForwardedPrefix`, [`Request.PublicHost`](./middleware.md#requestpublichost) and [`Request.PublicPrefix`](./middleware.md#requestpublicprefix) read `X-Forwarded-Host` and `X-Forwarded-Prefix` on it too. The same peers may send a PROXY protocol header to a listener given [`ProxyProtocol`](#proxyprotocol). Without it no proxy is trusted: the client address is the peer's and no forwarding header is read. The start-up log has a `trusted proxies` line.

It panics in `New` when given twice (`WithTrustedProxies: given twice (first at file.go:N, again at file.go:M): ...`), with an empty `Prefixes` and `UnixSockets` false, with a zero `netip.Prefix`, with a prefix of length 0 (`0.0.0.0/0`, `::/0`: `... trusts every peer, so any client could name any address in X-Forwarded-For: ...`), with a `Header` that is not a token, and with a `Header` that does not carry one address: `Forwarded` (`WithTrustedProxies: Header "Forwarded" is not read: its elements are "for=...;proto=...;host=..." pairs, not one address, ...`), `X-Forwarded-Host`, `X-Forwarded-Proto` or `X-Forwarded-Prefix` (`... does not carry the client's address: ...`).

Guide: [deploying](../guide/deploying.md#behind-a-load-balancer). Decisions: [ADR 0025](../adr/0025-no-proxy-is-trusted-by-default.md), [ADR 0058](../adr/0058-the-proxy-protocol-is-turned-on-per-listener-and-accepted-only-from-a-trusted-proxy.md).

### `TrustedProxies`

```go
type TrustedProxies struct {
	Prefixes        []netip.Prefix
	UnixSockets     bool
	Header          string
	ForwardedHost   bool
	ForwardedPrefix bool
}
```

TrustedProxies is what `WithTrustedProxies` takes. `Prefixes` are the networks the load balancers and proxies connect from; a prefix with host bits set is masked, and one in the IPv4-mapped form (`::ffff:10.0.0.0/104`) is turned into its IPv4 one. `UnixSockets` trusts the peer of every Unix socket the app listens on: a Unix peer has no address to list, so the socket file's permission bits are the list; it is false by default, and with it `Prefixes` may be empty. `Header` is the header they write the client's address in: `X-Forwarded-For` when empty, a list each proxy appends to and that is read from the right; any other name (`CF-Connecting-IP`, `X-Real-IP`, `Fly-Client-IP`) is read as one address, from its last line. `ForwardedHost` says the proxies write `X-Forwarded-Host`, replacing any the client sent, so [`Request.PublicHost`](./middleware.md#requestpublichost) reads it from a trusted peer and a WebSocket's Origin check accepts it as the request's own host ([`AllowOrigins`](./websocket.md#alloworigins)); `ForwardedPrefix` says the same of `X-Forwarded-Prefix`, the path prefix they strip, for [`Request.PublicPrefix`](./middleware.md#requestpublicprefix). Both are false by default, because a balancer trusted for `X-Forwarded-For` may pass a client's `X-Forwarded-Host` through untouched.

## Registering routes

All the route methods of `App` take the same arguments: the pattern, the handler and the route options (`opts ...RouteOption`). A pattern follows `net/http.ServeMux` rules ([ADR 0018](../adr/0018-the-router-follows-servemux-rules.md)): `{name}` is a wildcard, `{name...}` matches the rest, `{$}` anchors the end of a subtree. Every wildcard must have a field of that name in the Input's `Path` section and every `Path` field a wildcard. A route registered at a line that conflicts with an earlier one is refused naming both lines.

Allocations: a typed JSON request through any of these routes is 2 allocations, 3 when the handler uses the done channel of its context ([ADR 0003](../adr/0003-the-trade-budget.md)).

Guide: [routing](../guide/routing.md) and [handlers](../guide/handlers.md). Decision: [ADR 0010](../adr/0010-one-input-struct-with-sections-by-field-name.md).

### `App.Get`

```go
func (a *App) Get[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Get registers a typed handler for GET and, unless a `Head` route says otherwise, for HEAD ([ADR 0018](../adr/0018-the-router-follows-servemux-rules.md)). A HEAD request to a GET route gets the head of the answer, with the same `Content-Length`, and no body. The Input of a GET route may not have a `Body` section: `GET /x: BodyOnGet.Body is a Body section, but a GET route has no request body`.

### `App.Head`

```go
func (a *App) Head[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Head registers a typed handler for HEAD, which then wins over the GET route's implicit HEAD. The body of its answer is not sent. A HEAD route may not have a `Body` section and may not return a `Stream`, `SSE` or `Events`: `HEAD /x: a HEAD route cannot return a stream: its answer has no body to stream`.

### `App.Post`

```go
func (a *App) Post[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Post registers a typed handler for POST. A POST route may have a `Body` section, read as the type decides ([input](./input.md#body-types)).

### `App.Put`

```go
func (a *App) Put[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Put registers a typed handler for PUT.

### `App.Patch`

```go
func (a *App) Patch[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Patch registers a typed handler for PATCH. The field that a PATCH body may leave alone, set or clear is an `Opt[Null[T]]` ([`Null`](./input.md#null)).

### `App.Delete`

```go
func (a *App) Delete[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Delete registers a typed handler for DELETE. Like GET and HEAD, a DELETE route may not have a `Body` section.

### `App.Options`

```go
func (a *App) Options[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Options registers a typed handler for OPTIONS. A CORS preflight is answered by the `CORS` middleware before routing and never reaches a route ([middleware](./middleware.md#cors)); an OPTIONS request to a path with no OPTIONS route is answered by the engine with the `Allow` header.

### `App.Handle`

```go
func (a *App) Handle[In, Out any](method, pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Handle registers a typed handler for any method token. A typed route always has a method, so an empty method panics, and so does one that is not an HTTP token (`invalid method "GE T" registered at file.go:N: a method is a token`). A method OpenAPI 3.1 has no name for (`PURGE`) is served but left out of the API document.

### `App.Group`

```go
func (a *App) Group(prefix string, opts ...RouteOption) *Group
```

Group returns a Group whose routes all start with prefix. The prefix starts with a slash and does not end with one, and has no wildcard; anything else panics. Its options apply to every route in it and in the groups nested in it. A nested group's options beat this group's, and a route's own beat both. An option with a value that cannot work, such as `MaxBody(0)`, panics here, even when the group has no route yet.

The options a group takes are `MaxBody`, `BodyTimeout`, `AcceptUnknownMembers`, `RejectUnknownQueryKeys`, `AcceptUnknownQueryKeys`, `MaxInFlight`, `MaxParts`, `MaxMessage` ([input](./input.md#body-and-upload-options)), `Use` and `UseEach` ([middleware](./middleware.md#use)), `Provide` and `Override` ([providers](./providers.md)), `AllowOrigins` and `AllowAnyOrigin` ([websocket](./websocket.md)), `Tag` and `Deprecated` ([openapi](./openapi.md#tag)), and `Options` of any of these. `Summary`, `Description` and `Returns` are refused on a group, because one line on every operation of a group would describe none of them.

Decision: [ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md).

### `App.UseEach`

```go
func (a *App) UseEach(build func(r RouteInfo) (Middleware, error))
```

UseEach gives every route the app registers after it (the API document and the metrics page included, marked `RouteInfo.Builtin`) a middleware built from the route's identity, so an app can refuse a route that has no permission, or audit by operation ID (see [`UseEach`](./middleware.md#useeach) for what `build` returns and the order it runs in). It must come before the app's first route and first group, because it cannot reach a route that exists already: after one it panics, ``app: UseEach is called after GET /x (at file.go:12) was registered, so it would not reach it: move UseEach above it``, with the line of the `UseEach` after it. A nil function panics (`app: UseEach: the function is nil`), and so does a call after the app started serving. The same holds after `OpenAPI` or `ServeMetrics`, which it also reaches (``app: UseEach is called after OpenAPI /openapi.json (at file.go:12) was registered, ...``). Unlike `Use` it is not read at request time, so it cannot come late.

Guide: [middleware](../guide/middleware.md#default-deny-a-middleware-built-for-each-route). Decision: [ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md).

### `App.Routes`

```go
func (a *App) Routes() []RouteInfo
```

Routes returns every route registered so far, in the order it was registered, each as the function given to [`UseEach`](./app.md#appuseeach) is told about it: one [`RouteInfo`](./middleware.md#routeinfo) per typed route (a [`stdhttp.Mount`](./stdhttp.md) route among them, one per method), WebSocket route and Static route, and one for the API document and the metrics page when [`OpenAPI`](./openapi.md) and [`ServeMetrics`](./observability.md) registered them, with `Builtin` set. The paths of `WithHealth` are answered before routing and are not routes, so they are not listed. An app with no route returns an empty slice. The slice is new on every call and each `Tags` is a copy, so a caller that changes what it got changes nothing in the app. It may be called at any time and from any goroutine, before or while serving, and takes no lock. Called from the function given to `UseEach`, it returns the routes registered so far with the one being registered last, so a check of every route belongs after the last route, not in `UseEach`. It costs nothing a request pays: the list is made at Registration, one `RouteInfo` per route, and the request path is unchanged. Nothing can be misused, so it has no refusal.

Guide: [routing](../guide/routing.md#listing-the-routes). Decision: [ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md).

### `App.Use`

```go
func (a *App) Use(mw ...Middleware)
```

Use adds app middleware: it runs before the route is matched, so it sees every request including a 404, a 405 and a CORS preflight. Calls add to the end of the chain, so the first one given is outermost. Unlike a group's `Use` it may come after routes, because it is read when a request arrives. A nil middleware panics (`app: Use: middleware 2 is nil`), and so does a call after the app started serving.

Allocations: a typed POST behind any number of middleware layers is still 2 ([ADR 0003](../adr/0003-the-trade-budget.md)). See also [`Middleware`](./middleware.md#middleware).

Guide: [middleware](../guide/middleware.md). Decision: [ADR 0022](../adr/0022-middleware-sees-a-request-view-and-returns-a-response-value.md).

## Serving

The engine speaks HTTP/1.1 only; TLS is served with `ServeTLS` and `ListenTLS` and negotiates only `http/1.1` ([ADR 0017](../adr/0017-tls-now-http2-later.md)). The framework installs no signal handler: catching SIGTERM and calling `Shutdown` is two lines of the application's own Go ([ADR 0029](../adr/0029-shutdown-drains-and-then-closes.md)). A handler that panics is recovered by the engine, logged with its stack, and answered with a 500 ([ADR 0028](../adr/0028-panics-are-recovered-by-the-engine.md)).

Guide: [deploying](../guide/deploying.md).

### `App.Listen`

```go
func (a *App) Listen(addr string, opts ...ListenOption) error
```

Listen serves on a TCP address until `Shutdown` or `Close`, and returns [`ErrServerClosed`](#errserverclosed) then. An error from listening is returned as it is. The first call starts serving: the route table is frozen and the API document, if asked for, is built. `opts` are the settings of this listener, [`ProxyProtocol`](#proxyprotocol) the one there is.

### `App.ListenTLS`

```go
func (a *App) ListenTLS(addr string, cfg *tls.Config, opts ...ListenOption) error
```

ListenTLS is `Listen` for TLS: it serves HTTPS on a TCP address until `Shutdown` or `Close`, and returns `ErrServerClosed` then. `cfg` is checked first and a mistake in it panics (see [`App.ServeTLS`](#appservetls)); an error from listening is returned.

### `App.Serve`

```go
func (a *App) Serve(ln net.Listener, opts ...ListenOption) error
```

Serve serves on `ln` until `Shutdown` or `Close`, and returns `ErrServerClosed` then. The listener may be any `net.Listener`, which is how a test serves the app over memory. More than one Serve may run on one App. Any other error from the listener is returned. `opts` are the settings of this listener.

### `App.ServeTLS`

```go
func (a *App) ServeTLS(ln net.Listener, cfg *tls.Config, opts ...ListenOption) error
```

ServeTLS is `Serve` for TLS: each connection handshakes on its own goroutine within `Limits.HandshakeTimeout` before its first request, and only HTTP/1.1 is negotiated. Loading certificate files is the caller's: `tls.LoadX509KeyPair` returns its error there.

The config is refused with a panic, before anything is served, when:

- it is nil: `ServeTLS: the TLS config is nil; give a *tls.Config with a certificate`;
- it has no certificate source: `ServeTLS: the TLS config has no certificate: set Certificates, GetCertificate or GetConfigForClient`;
- `NextProtos` offers `h2` (the engine would speak HTTP/1 to a client that agreed to h2) or any protocol other than `http/1.1` and `acme-tls/1`: ``ServeTLS: the TLS config offers "h2" in NextProtos, but bites speaks HTTP/1.1 only until its HTTP/2 engine exists (ADR 0017); ...``;
- `NextProtos` offers `acme-tls/1` and the config has only static `Certificates`, which cannot answer a tls-alpn-01 challenge: ``ServeTLS: the TLS config offers "acme-tls/1" in NextProtos but has only static Certificates, ...``;
- `ClientAuth` verifies client certificates and there are no `ClientCAs` (nor a `GetConfigForClient`), so any public CA's certificate would pass: `ServeTLS: the TLS config verifies client certificates (ClientAuth RequireAndVerifyClientCert) but has no ClientCAs, ...`.

`ListenTLS` panics with the same texts under its own name. A config that lists `acme-tls/1` is served with it: a CA's tls-alpn-01 validation is answered by the certificate `GetCertificate` returns and its connection closed after the handshake, and every other client gets HTTP/1.1 ([ADR 0017](../adr/0017-tls-now-http2-later.md)). Client certificates are verified by `ClientAuth` and `ClientCAs` and read with [`Request.VerifiedChains`](./middleware.md#requestverifiedchains) ([ADR 0066](../adr/0066-client-certificates-are-verified-by-the-tls-config-and-read-as-verified-chains.md)); a certificate reloaded without a restart is a [`Certs`](#certs).

### `App.ListenUnix`

```go
func (a *App) ListenUnix(path string, perm fs.FileMode, opts ...ListenOption) error
```

ListenUnix serves on a Unix socket at `path` until `Shutdown` or `Close`, and returns `ErrServerClosed` then. The socket file is made with the permission bits `perm` (`0o660` lets the process's group connect, since connecting needs write permission) and removed when the listener closes. A socket left at `path` by a process that is gone (nothing answers on it) is removed first; a socket another process listens on is an error (`bites: ListenUnix: another process is listening on ...`), and a file that is not a socket is left alone and is an error from `net.Listen`. A Unix connection is parked when idle and watched for a hangup like a TCP one, and has no peer address: [`Request.RemoteAddr`](./middleware.md#requestremoteaddr) is the zero `netip.AddrPort`, and its peer is a trusted proxy only with `TrustedProxies.UnixSockets`. For TLS on a Unix socket, give `ServeTLS` a `net.Listen("unix", path)` listener.

It panics, before anything is made, for a `perm` of zero or with bits beyond `0o777`: `ListenUnix: the permission Srw-rw---- is not a set of permission bits: give the bits of the socket file, such as 0o660 for the owner and its group, which is who may connect`.

Several listeners: call `Listen`, `ListenTLS`, `ListenUnix`, `Serve` and `ServeTLS` as many times as needed, each on its own goroutine. They share the route table and `MaxConns`, each logs `bites: listening` with its network, address, whether it is TLS and whether it reads the PROXY protocol, and `Shutdown` and `Close` reach all of them. Decision: [ADR 0068](../adr/0068-one-app-serves-several-listeners-and-a-unix-socket-is-a-socket-like-tcp.md).

<!-- compiles: body -->

```go
errs := make(chan error, 2)
go func() { errs <- app.Listen(":8080") }()
go func() { errs <- app.ListenUnix("/run/app/app.sock", 0o660) }()
```

### `ListenOption`

```go
type ListenOption func(*listenConfig)
```

A ListenOption is a setting of one listener, given to `Listen`, `ListenTLS`, `ListenUnix`, `Serve` or `ServeTLS` after their other arguments. A nil one panics at the serving call (`Listen: ListenOption 2 is nil`).

### `ProxyProtocol`

```go
func ProxyProtocol() ListenOption
```

ProxyProtocol makes a listener read the PROXY protocol, version 1 or 2, at the start of every connection: the header a TCP balancer (AWS NLB, HAProxy, nginx's `proxy_protocol`) sends to say who its client is. The client it names becomes the connection's peer, so `Request.RemoteAddr`, `Request.ClientAddr` and the forwarding walk start from it. A version 2 `LOCAL` header (the balancer's health check) and a version 1 `UNKNOWN` keep the balancer as the peer.

Only a trusted proxy may send it: a connection from a peer outside `TrustedProxies.Prefixes` (on a Unix socket, any peer when `UnixSockets` is false) is closed as it is accepted, before a byte is read. A trusted peer must send it: a connection that starts with anything else, or with a header that does not parse, is closed with nothing written, and a peer with no header at all is logged at Warn once a minute. The wait for the header's first byte is the connection's idle wait, parked like any other; once begun, the whole header must arrive within `Limits.HeadTimeout`, or on a TLS listener, where it comes before the handshake, within `Limits.HandshakeTimeout` from accept. It costs no allocation a request, and a connection of such a listener 80 bytes more while idle.

The serving call panics when the app has no `WithTrustedProxies` (`Listen: ProxyProtocol is given but the app trusts no proxy, so no peer could send the header: name the balancers with WithTrustedProxies (ADR 0058)`), when the listener is a Unix socket and `UnixSockets` is false, and when it is a TCP listener and `Prefixes` is empty.

<!-- compiles: body -->

```go
app = bites.New(bites.WithTrustedProxies(bites.TrustedProxies{
	Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/16")},
}))
go app.Listen(":8443", bites.ProxyProtocol()) // the balancer's port
go app.Listen(":8080")                         // probes, no header
```

Guide: [deploying](../guide/deploying.md#the-proxy-protocol). Decision: [ADR 0058](../adr/0058-the-proxy-protocol-is-turned-on-per-listener-and-accepted-only-from-a-trusted-proxy.md).

### `Service`

```go
type Service struct {
	Name           string
	Start          func(ctx context.Context) error
	BeforeShutdown func(ctx context.Context) error
	Stop           func(ctx context.Context) error
}
```

Service is something that lives exactly as long as the app: a database pool, a queue consumer, a scheduler, a registration in a service registry. A nil function means there is nothing to do at that moment, and at least one must be set. `Name` is required, unique in the app, and has no space, quote or control character.

- `Start` runs after the listener is bound and before the first connection is accepted, one service at a time in registration order. It returns when the service is running and does not block. Its `ctx` is the service's life, not the start's: it has no deadline and is cancelled just before `Stop` is called (or when the app closes) with the cause `ErrServerClosed`, so `go run(ctx)` is the correct body of a worker. An error or panic refuses the start: the services already started are stopped in reverse, the listener is closed, and `Listen`, `Serve`, `ListenTLS` and `ListenUnix` return `bites: service "queue": Start: <error>`, which wraps the cause for `errors.Is` and `errors.As`.
- `BeforeShutdown` runs when `Shutdown` starts, in reverse order, after readiness has failed and before the shutdown delay and the listener closing, within the first half of what is left of Shutdown's context. `Close` does not run it.
- `Stop` runs once the drain is over, in reverse order, with Shutdown's context. It is called for a service whose `Start` returned nil or that has none, never for one whose `Start` failed, and every `Stop` runs even when an earlier one failed. It must work when `BeforeShutdown` never ran.

A function that has not returned when its context ends is abandoned, logged by name, and the next goes on; Go cannot kill its goroutine, so it leaks until the process ends. A panic in a function is recovered, logged with its stack and treated as an error. Each function logs one line when it ends: `bites: service started`, `bites: service left` or `bites: service stopped`, with `service` and `took`, at Error with `err` when it failed.

A module returns a `Service` for the application, and [`Worker`](#worker) and [`Every`](#every) build one for a background goroutine and for periodic work. `bites.Now(ctx)` and `bites.Logger(ctx)` work on the `ctx` of `Start`: the app's clock and logger to pass to [`App.Service`](#appservice). A service started with `Start` must not call `Shutdown` or `Close` on its app and wait for it; call it from its own goroutine.

Guide: [deploying](../guide/deploying.md#services-that-live-as-long-as-the-app), [testing](../guide/testing.md#testing-a-service). Decision: [ADR 0079](../adr/0079-services-start-after-the-app-binds-and-stop-in-reverse-around-shutdown.md).

### `App.Service`

```go
func (a *App) Service(s Service)
```

Service registers `s` to start with the app and stop with it. Services start in the order they are registered and stop in the reverse order, so a service registered after another stops before it: register the pool before the worker that uses it. It panics for a `Service` with no usable name (`app: Service has no name: set Service.Name, the word the log and the errors call this service by`), with no function (`app: Service "queue" has no Start, BeforeShutdown or Stop: a service with nothing to run is a mistake; set at least one of them`), for a name used twice (`app: Service "queue" is registered twice, first at file.go:N: a name is one service; give the second its own name`), and for a call after the app started serving (`app: Service "queue" registered at file.go:N after the app started serving: services can be added only before it does`). Each message that is not the last ends with the line `registered at file.go:N`.

Allocations: none on the request path; a typed POST beside a Service is still 2 ([ADR 0003](../adr/0003-the-trade-budget.md)).

<!-- compiles: decls -->

```go
func register(app *bites.App, pool *sql.DB) {
	app.Service(bites.Service{
		Name:  "orders-db",
		Start: func(ctx context.Context) error { return pool.PingContext(ctx) },
		Stop:  func(context.Context) error { return pool.Close() },
	})
}
```

Guide: [deploying](../guide/deploying.md#services-that-live-as-long-as-the-app). Decision: [ADR 0079](../adr/0079-services-start-after-the-app-binds-and-stop-in-reverse-around-shutdown.md).

### `Worker`

```go
func Worker(name string, run func(ctx context.Context) error) Service
```

Worker returns a [`Service`](#service) that runs `run` on its own goroutine for the life of the app. Its `Start` calls `go run(ctx)` with the Service's life as `ctx` and returns at once, so a slow `run` never delays the app's start. Its `Stop` waits for `run` to return: the app cancels `ctx` (cause `ErrServerClosed`) just before `Stop`, so `run` must watch `ctx.Done()` and return, and one that does not within Shutdown's context (or `WithServiceTimeout` under `Close`) is abandoned and logged. [`Logger(ctx)`](observability.md#logger) is the app's logger and [`Now(ctx)`](#now) its clock.

A `run` that returns before `Stop`, with an error or nil, or that panics, is logged at Error as `bites: worker returned before Stop and is not restarted` with `worker` and `err`, and is not restarted: a worker that must survive its own failure loops inside `run`. An error returned after `ctx` was cancelled is logged at Warn, except `context.Canceled`, which is the expected way to stop.

It panics at the call, naming the line, for a name that is empty or has a space, a quote or a control character (`bites.Worker: the name is empty or has a space, a quote or a control character: give the word the log and the errors call this worker by, such as "mail-sender"`) and for a nil `run` (`bites.Worker("mail"): run is nil: give the function the worker runs until its ctx is cancelled`).

Allocations: none on the request path ([ADR 0003](../adr/0003-the-trade-budget.md)).

<!-- compiles: decls -->

```go
func registerMailer(app *bites.App, outbox *Outbox) {
	app.Service(bites.Worker("mailer", func(ctx context.Context) error {
		for {
			select {
			case <-ctx.Done():
				return nil
			case m := <-outbox.C:
				if err := m.Send(ctx); err != nil {
					bites.Logger(ctx).Warn("mail not sent", "err", err)
				}
			}
		}
	}))
}

type Outbox struct{ C chan Mail }

type Mail struct{}

func (Mail) Send(context.Context) error { return nil }
```

Guide: [deploying](../guide/deploying.md#periodic-work-and-a-background-worker). Decision: [ADR 0079](../adr/0079-services-start-after-the-app-binds-and-stop-in-reverse-around-shutdown.md).

### `Every`

```go
func Every(name string, interval time.Duration, fn func(ctx context.Context) error, opts ...EveryOptions) Service
```

Every returns a [`Service`](#service) that calls `fn` once every `interval` for the life of the app, on the app's clock (`WithClock`, `bitetest.Clock`), so a test moves time instead of waiting. By default the first call is one interval after the app started serving, not at start: a start never waits for the work. For a first call right after the start, give [`EveryOptions{AtStart: true}`](#everyoptions).

Calls are serial: `fn` never runs twice at once. A call that takes longer than `interval` delays the next until it returns, and the ticks it missed are dropped, not run in a burst: the next call is at the next multiple of `interval` after the first due time that is still ahead, and the log says at Warn (`bites: job took longer than its interval, the missed ticks are dropped`, with `job`, `interval` and `skipped`) how many were skipped. `fn`'s `ctx` is the Service's life, cancelled before `Stop`, which waits for a call in progress to return.

An error from `fn` is logged at Warn (`bites: job failed, trying again at the next tick`, with `job` and `err`) and the loop goes on. A panic in `fn` is recovered, logged at Error with its stack, and the loop goes on. There is no jitter option and no cron expression.

It panics at the call, naming the line, for a name that is empty or has a space, a quote or a control character, for a nil `fn` (`bites.Every("purge"): fn is nil: give the function called once every interval`), for an interval of zero or less (`bites.Every("purge"): the interval 0 is not a time between calls: give a positive duration, such as time.Minute`), and when more than one options value is given (`bites.Every("purge"): 2 options values were given; give one EveryOptions`).

Allocations: none on the request path ([ADR 0003](../adr/0003-the-trade-budget.md)).

<!-- compiles: decls -->

```go
func registerSweeps(app *bites.App, db *Store) {
	app.Service(bites.Every("purge-deleted", time.Hour, func(ctx context.Context) error {
		return db.PurgeDeletedBefore(ctx, bites.Now(ctx).AddDate(0, 0, -30))
	}))
}

type Store struct{}

func (*Store) PurgeDeletedBefore(context.Context, time.Time) error { return nil }
```

### `EveryOptions`

```go
type EveryOptions struct {
	AtStart bool
}
```

EveryOptions configures [`Every`](#every). The zero value is valid: the first call is one interval after the app started serving.

`AtStart`, when true, makes the first call of `fn` right after the Service started, on the Service's own goroutine and before the first interval has passed: warm a cache, or reconcile what a crash left behind. The call does not delay the start, because `Start` returns first and the app accepts connections while `fn` runs. It is the same call as every other one: the app's clock, logger and `ctx`, serial with the interval calls, an error logged at Warn and a panic recovered, and `Stop` cancels its `ctx` and waits for it. The intervals count from the start, so when the call at start takes longer than `interval` the ticks it missed are dropped and logged as for any call. False is no call at start.

<!-- compiles: decls -->

```go
func registerWarmup(app *bites.App, cache *Cache) {
	app.Service(bites.Every("warm-cache", 10*time.Minute, cache.Refresh, bites.EveryOptions{AtStart: true}))
}

type Cache struct{}

func (*Cache) Refresh(context.Context) error { return nil }
```

Guide: [deploying](../guide/deploying.md#periodic-work-and-a-background-worker), [testing](../guide/testing.md#testing-a-service). Decision: [ADR 0079](../adr/0079-services-start-after-the-app-binds-and-stop-in-reverse-around-shutdown.md).

### `App.Shutdown`

```go
func (a *App) Shutdown(ctx context.Context) error
```

Shutdown stops accepting, closes idle connections, cancels the context of every live stream (`SSE`, and a `Stream` with `Live` set) with the cause `ErrServerClosed`, sends every WebSocket a close with status 1001, and waits for requests in flight, which finish with `Connection: close`. When `ctx` ends first the rest are closed and their contexts cancelled, and Shutdown returns `ctx.Err()`; it returns nil when everything drained. Called on an app that never started serving it returns nil. With [Services](#service) it first runs their `BeforeShutdown` functions and after the drain their `Stop` functions, and returns the errors of all of them joined with the drain's (`context.DeadlineExceeded` for a function that was abandoned); a single error is returned as it is. `Listen` and `Serve` return only after the `Stop` functions have returned, so `go app.Shutdown(ctx)` followed by `app.Listen(addr)` is safe.

Decision: [ADR 0029](../adr/0029-shutdown-drains-and-then-closes.md).

<!-- compiles: body -->

```go
go func() {
	if err := app.Listen(":8080"); err != nil && !errors.Is(err, bites.ErrServerClosed) {
		log.Fatal(err)
	}
}()
stop, cancel := signal.NotifyContext(ctx, syscall.SIGTERM)
defer cancel()
<-stop.Done()
drain, cancelDrain := context.WithTimeout(context.Background(), 10*time.Second)
defer cancelDrain()
_ = app.Shutdown(drain)
```

### `App.Close`

```go
func (a *App) Close() error
```

Close stops accepting and closes every connection at once, cancelling the context of each request in flight with `ErrServerClosed`. It returns nil when the app has no [Services](#service); with services it runs their `Stop` functions with a context that is already done (no `BeforeShutdown`), each waited for at most `WithServiceTimeout`, and returns their errors joined. Serve and Listen then return `ErrServerClosed`, after the stops.

### `App.Clock`

```go
func (a *App) Clock() Clock
```

Clock is the clock the app was given with `WithClock`, or the real one. A test reads the time through it; code that runs for a request reads it with [`Now`](#now).

### `Now`

```go
func Now(ctx context.Context) time.Time
```

Now is the time on the clock of the app that `ctx`'s request belongs to, so a Provider, a middleware or a handler that checks a time against the request (a session's absolute lifetime, a token's expiry) moves with the clock a test moves with `WithClock`. It works on a context derived from the request's and on one kept after the request ended; on a Service's life (the `ctx` its `Start` receives) it is the app's clock too; on any other context, or a nil one, it is `time.Now()`. It costs no allocation.

### `ErrServerClosed`

```go
var ErrServerClosed = engine.ErrServerClosed
```

ErrServerClosed is what `Serve`, `ServeTLS`, `Listen`, `ListenTLS` and `ListenUnix` return after `Shutdown` or `Close`, and the cancel cause of the context of a live stream or a request cut by them. Test for it with `errors.Is(err, bites.ErrServerClosed)`; a handler reads the cause with [`Cause`](./errors.md#cause), not `context.Cause` ([ADR 0016](../adr/0016-the-handler-context-is-cancelled-on-disconnect.md)). Its text is `bites: server closed`.

## TLS certificates

A certificate that is renewed while the app runs (cert-manager, certbot) is put in force without a restart through a `Certs`, whose `GetCertificate` the `*tls.Config` is given ([ADR 0067](../adr/0067-a-certificate-is-reloaded-through-get-certificate-and-a-failed-reload-changes-nothing.md)). bites installs no signal handler and watches no file: the application calls `Reload`.

<!-- compiles: body -->

```go
certs, err := bites.LoadCerts("tls.crt", "tls.key")
if err != nil {
	log.Fatal(err)
}
go app.ListenTLS(":8443", &tls.Config{GetCertificate: certs.GetCertificate})
hup := make(chan os.Signal, 1)
signal.Notify(hup, syscall.SIGHUP)
go func() {
	for range hup {
		if err := certs.Reload(); err != nil {
			log.Printf("certificate not reloaded: %v", err)
		}
	}
}()
```

### `Certs`

```go
type Certs struct {
	// unexported fields
}
```

Certs is a server certificate that can be replaced while the app serves. Every handshake reads the certificate in force with one atomic load, so handshakes during a reload finish with the old one, the next ones get the new one, and open connections keep theirs. A reload or a set that fails returns its error and changes nothing. The zero `Certs` holds no certificate: `Set` gives it one, and `GetCertificate` fails every handshake until then. It is safe for concurrent use. Leave `Certificates` of the config empty: `crypto/tls` uses `Certificates[0]` for a client that sends no server name.

### `LoadCerts`

```go
func LoadCerts(certFile, keyFile string) (*Certs, error)
```

LoadCerts reads a PEM certificate chain and its key, as `tls.LoadX509KeyPair` does, and returns a `Certs` holding it, or the error that stopped it (the same checks as `Reload`).

### `Certs.Reload`

```go
func (c *Certs) Reload() error
```

Reload reads the two files `LoadCerts` was given again and puts the certificate in force. It returns an error, and the certificate in force is unchanged, when a file is missing or half written, the PEM does not parse, the key is not the certificate's, or the certificate has expired; the error's text ends with `(the certificate in force is unchanged)`. Reloads and sets are serialised, so an older read is never put in force after a newer one. On a `Certs` not made by `LoadCerts` it returns `ErrNoCertFiles`.

### `Certs.Set`

```go
func (c *Certs) Set(cert tls.Certificate) error
```

Set puts `cert` in force, for a certificate from somewhere other than two files (a secret store). It is refused, with the certificate in force unchanged, when `cert` has no certificate or no private key, when the key is not the certificate's, or when the certificate has expired.

### `Certs.GetCertificate`

```go
func (c *Certs) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error)
```

GetCertificate is the function a `*tls.Config` calls for each handshake: it returns the certificate in force, or an error while there is none.

### `ErrNoCertFiles`

```go
var ErrNoCertFiles = errors.New("bites: Certs has no files to reload; it was not made by LoadCerts")
```

ErrNoCertFiles is what `Reload` returns on a `Certs` that was not made by `LoadCerts`.

## Groups

A Group is a path prefix on an App and the options its routes share. It carries the prefix, and nests. A group is sealed by the first route or child group registered through it: after that `Use`, `Provide` and `Override` on the group panic, naming the line that sealed it, because the call would not reach what exists already ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)). The option spellings (`Use`, `Provide`, `Override` given to `Group`) cannot be late.

The route methods of a group are those of `App` with the group's prefix put in front of the pattern; their panics name the full route (`GET /g/x`).

### `Group`

```go
type Group struct {
	// Has unexported fields.
}
```

A Group is made with `App.Group` or `Group.Group`. Do not copy it.

### `Group.Get`

```go
func (g *Group) Get[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Get registers a typed handler for GET (and HEAD) `prefix+pattern`. See [`App.Get`](#appget).

### `Group.Head`

```go
func (g *Group) Head[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Head registers a typed handler for HEAD `prefix+pattern`. See [`App.Head`](#apphead).

### `Group.Post`

```go
func (g *Group) Post[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Post registers a typed handler for POST `prefix+pattern`. See [`App.Post`](#apppost).

### `Group.Put`

```go
func (g *Group) Put[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Put registers a typed handler for PUT `prefix+pattern`. See [`App.Put`](#appput).

### `Group.Patch`

```go
func (g *Group) Patch[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Patch registers a typed handler for PATCH `prefix+pattern`. See [`App.Patch`](#apppatch).

### `Group.Delete`

```go
func (g *Group) Delete[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Delete registers a typed handler for DELETE `prefix+pattern`. See [`App.Delete`](#appdelete).

### `Group.Options`

```go
func (g *Group) Options[In, Out any](pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Options registers a typed handler for OPTIONS `prefix+pattern`. See [`App.Options`](#appoptions).

### `Group.Handle`

```go
func (g *Group) Handle[In, Out any](method, pattern string, h func(context.Context, In) (Out, error), opts ...RouteOption)
```

Handle registers a typed handler for `method` and `prefix+pattern`. See [`App.Handle`](#apphandle).

### `Group.Group`

```go
func (g *Group) Group(prefix string, opts ...RouteOption) *Group
```

Group returns a Group nested in `g`, which inherits `g`'s options and may override them. Its prefix is appended to `g`'s and follows the same rules as [`App.Group`](#appgroup).

### `Group.Use`

```go
func (g *Group) Use(mw ...Middleware)
```

Use adds middleware to every route of this group and of the groups made in it afterwards, inside the middleware of the groups around it. It must come before the group's first route and first child group: after one it panics, ``group "/g": Use is called after GET /g/x (at file.go:12) was registered in this group, so it would not reach it: move Use above it``, with the line of the `Use` after it. A nil middleware panics (`group "/g": Use: middleware 1 is nil`). The same thing as an option is [`Use`](./middleware.md#use).

### `Group.UseEach`

```go
func (g *Group) UseEach(build func(r RouteInfo) (Middleware, error))
```

UseEach builds a middleware from the identity of each route of this group and of the groups made in it afterwards (see [`UseEach`](./middleware.md#useeach)). Like `Use` it must come before the group's first route and first child group: after one it panics, ``group "/g": UseEach is called after GET /g/x (at file.go:12) was registered in this group, so it would not reach it: move UseEach above it``. A nil function panics (`group "/g": UseEach: the function is nil`).

## Route options

### `RouteOption`

```go
type RouteOption func(*routeConfig)
```

A RouteOption changes one route, or every route of a group when it is given to `Group`. So are middleware (`Use`) and Providers (`Provide`, `Override`). Options apply in order from the app's limits inward: the group's, a nested group's, then the route's own, so the innermost one wins. A nil option panics (`an option is nil`). An application bundles several into one with [`Options`](#options-1).

The options by page:

| Option | What it does | Page |
|---|---|---|
| `Options` | several options as one, applied in order | [below](#options-1) |
| `MaxBody`, `BodyTimeout`, `MaxInFlight`, `MaxParts`, `AcceptUnknownMembers`, `RejectUnknownQueryKeys`, `AcceptUnknownQueryKeys` | the limits and mode of the body, and whether an unknown query key is refused | [input](./input.md#body-and-upload-options) |
| `Use`, `UseEach` | middleware, and middleware built from the route's identity | [middleware](./middleware.md#use) |
| `Provide`, `Override` | Providers | [providers](./providers.md) |
| `Returns` | the types an interface return may hold | [responses](./responses.md#returns) |
| `Summary`, `Description`, `Tag` | prose for the API document | [openapi](./openapi.md) |
| `MaxMessage`, `AllowOrigins`, `AllowAnyOrigin` | WebSocket limits and origins | [websocket](./websocket.md) |
| `OperationID`, `Undocumented`, `Deprecated`, `SetsLocation` | the operation's id, whether it is documented, whether it is deprecated, and whether its `Created` states a `Location` | [openapi](./openapi.md#describing-an-operation) |

### `Options`

```go
func Options(opts ...RouteOption) RouteOption
```

Options bundles several options into one, applied in the order given, so an application can name a combination it repeats: `func Op(id, summary string) bites.RouteOption { return bites.Options(bites.OperationID(id), bites.Summary(summary)) }` is an option like `Summary` and may be followed by others in the same call. The bundle works on a route, on a `Group` and inside another `Options`, and it adds no rule of its own: a member that cannot work is refused as it would be alone (`Summary` in a bundle given to a group is `group "/g": Summary is given to a group, but one summary cannot describe every operation in it: give it to each route`), and a nil member panics, naming its position (`GET /x: Options: option 2 is nil`). `Options()` with no member changes nothing. Decision: [ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md).

## Limits and compression

### `Limits`

```go
type Limits struct {
	MaxHead      int           // request head, 8 KiB; over it is a 431
	MaxFields    int           // header or Form fields, 100; over it is a 431 (413 for a Form)
	MaxBody      int64         // request body per route, 1 MiB; over it is a 413
	HeadTimeout  time.Duration // time to read a head, 10 s
	BodyTimeout  time.Duration // time to read a body per route, 30 s; over it is a 408
	IdleTimeout  time.Duration // idle keep-alive, 60 s
	WriteTimeout time.Duration // write, 30 s
	MaxConns     int           // connections, 90% of the file descriptor limit
	MaxDepth     int           // JSON nesting, 64; over it is a 400

	HandshakeTimeout time.Duration

	MaxMessage        int
	MessageTimeout    time.Duration
	ReassemblyBudget  int64
	SendQueue         int64
	SendBudget        int64
	HeartbeatInterval time.Duration
	MissedPongs       int

	MaxFieldBytes int
	MaxFormBytes  int
	UploadMemory  int64
	UploadDir     string
}
```

Limits are the limits of ADR 0024 that the app has. A zero field takes its default, a negative field makes `New` panic, and no limit can be switched off. The start-up log lists every limit, marked `default` or `custom`. The per-route ones (`MaxBody`, `BodyTimeout`) are also options of a route or a group.

| Field | Default | When exceeded |
|---|---|---|
| `MaxHead` | 8 KiB | 431 |
| `MaxFields` | 100 | 431 for header fields, 413 for the fields of a `Form[T]` |
| `MaxBody` | 1 MiB | 413 |
| `HeadTimeout` | 10 s | connection closed |
| `BodyTimeout` | 30 s | 408, connection closed |
| `IdleTimeout` | 60 s | connection closed |
| `WriteTimeout` | 30 s | connection closed (applies to every chunk of at most 64 KiB of a stream) |
| `MaxConns` | 90% of the process's file descriptor limit | not accepted: the engine stops calling accept |
| `MaxDepth` | 64 levels of JSON nesting | 400 |
| `HandshakeTimeout` | 10 s, from accept to the end of a TLS handshake | connection closed |
| `MaxMessage` | 64 KiB, one WebSocket message reassembled | socket closed with 1009 |
| `MessageTimeout` | 30 s, to finish a message that did not arrive whole | socket closed with 1008 |
| `ReassemblyBudget` | 64 MiB, all WebSockets together | the socket that would pass it is closed with 1013 |
| `SendQueue` | 1 MiB queued on one WebSocket | socket closed with 1008 |
| `SendBudget` | 64 MiB queued on all WebSockets | the socket that would pass it is closed with 1013 |
| `HeartbeatInterval` | 30 s between pings | |
| `MissedPongs` | 2 pings in a row unanswered | socket closed with 1008 |
| `MaxFieldBytes` | 64 KiB, one text field of a `Multipart[T]` | 413 at the field |
| `MaxFormBytes` | 1 MiB, all the text of one `Multipart[T]` request | 413 at the field that passed it |
| `UploadMemory` | 32 MiB of `Multipart[T]` file bytes in memory, all requests together (one request keeps at most 256 KiB, fixed in code) | the file goes to a temporary file, with no answer |
| `UploadDir` | `os.TempDir()`, where those files go, unlinked | `New` panics when it does not exist or is not writable |

At most 20 errors are listed in a 400 and a message echoes at most 64 bytes of the client's value; these are fixed ([ADR 0012](../adr/0012-client-errors-are-400-problem-json.md)). An `Upload` or `Multipart[T]` route has no default `MaxBody` ([input](./input.md#upload)); the 256 KiB and 32 MiB of `UploadMemory` are judgements, not measurements of a workload, and a spill directory on tmpfs is memory, which the start-up log warns about. The `MaxMessage` option sets the WebSocket limit for a route or a group, and may not exceed `ReassemblyBudget`.

Guide: [limits and timeouts](../guide/limits.md). Decisions: [ADR 0024](../adr/0024-limits-are-on-by-default.md), [ADR 0037](../adr/0037-a-slow-reader-is-closed-not-buffered-forever.md), [ADR 0039](../adr/0039-heartbeats-run-on-one-timer-wheel.md), [ADR 0046](../adr/0046-websocket-origins-and-inbound-limits.md).

### `Gzip`

```go
type Gzip struct {
	// MinSize is the smallest body that is compressed, in bytes. Zero is the default of 1 KiB;
	// a value below 1 KiB makes New panic, because under about 1.2 KiB an answer fits one TCP
	// segment and compressing it saves bytes and no packets.
	MinSize int
}
```

Gzip configures response compression for `WithGzip`, gzip alone. Compression is gzip at `BestSpeed` with no level to choose. `MinSize` is the only field.

### `Compression`

```go
type Compression struct {
	MinSize  int
	Encoders []Encoder
}
```

Compression is what `WithCompression` takes. `MinSize` is the smallest body that is compressed, in bytes, 1 KiB when zero and never less. `Encoders` are the codings offered, in the server's order of preference, which decides between codings a client weighs alike (a browser sends `gzip, deflate, br, zstd`, all of weight 1): list the one that makes the smallest answer first, `brotli.Encoder(), zstd.Encoder(), bites.GzipEncoder()`. Empty is gzip alone.

### `Encoder`

```go
type Encoder interface {
	Coding() string
	Encode(dst, src []byte) []byte
}
```

An Encoder is one content coding the engine can compress an answer with: [`GzipEncoder`](#gzipencoder) from the core, `zstd.Encoder` and `brotli.Encoder` from the modules ([zstd](./zstd.md), [brotli](./brotli.md)). `Coding` is the token `Accept-Encoding` and `Content-Encoding` name it by, a lowercase token read once when `New` runs. `Encode` appends the compressed form of `src` to `dst` and returns it; it is called from many connections at once, and must keep neither slice past its return, because both are the engine's pooled buffers: the contract `io.Writer` states for its argument. An output that is not smaller than `src` is not sent; the body goes as it is. A panic in `Encode` is recovered, logged at Error with its stack, and the answer sent uncompressed. Decision: [ADR 0061](../adr/0061-compression-chooses-among-the-apps-encoders-by-the-clients-weights-then-the-apps-order.md).

### `GzipEncoder`

```go
func GzipEncoder() Encoder
```

GzipEncoder is the core's gzip: `compress/gzip` at `BestSpeed`, its writers pooled and never reaching user code. Its coding is `gzip`, and a client's `x-gzip` is read as gzip.

### `MinCompressSize`

```go
const MinCompressSize = engine.CompressFloor
```

MinCompressSize is the floor of `Compression.MinSize` and `Gzip.MinSize`, and their default: 1024 bytes. It was `MinGzipSize` before the encoders of ADR 0061.

## Clocks and naming

### `Clock`

```go
type Clock = clock.Clock
```

Clock reads the time and schedules a function (`Now` and `AfterFunc`); the app takes one with `WithClock` so a timeout is tested by moving time forward instead of waiting for it. The real clock is the default. `bitetest.Clock` is one that moves only when told to ([bitetest](./bitetest.md#bitetestclock)).

### `NamingPolicy`

```go
type NamingPolicy = wire.Policy
```

NamingPolicy turns a Go field name into a wire name: `func(goName string) string`. One is chosen per app with `WithNaming`. A name that no rule produces is given by the optional method `WireNames() map[string]string` on a section's struct, keyed by Go field name and checked at Registration (`GET /x: StaleNamesIn.Query.WireNames names Gone, which is not a field`). Two fields of one struct with the same wire name are refused. A body type that carries `json` tags keeps them, and a tag wins over the policy.

### `CamelCase`

```go
func CamelCase(goName string) string
```

CamelCase is the lowerCamel naming policy: `ItemID` is `itemId`, `HTTPStatus` is `httpStatus`.

### `SnakeCase`

```go
func SnakeCase(goName string) string
```

SnakeCase is the default naming policy: `ItemID` is `item_id`, `HTTPStatus` is `http_status`, `UserIDs` is `user_ids`.

## Registration errors

### `RegistrationError`

```go
type RegistrationError struct {
	// Has unexported fields.
}
```

RegistrationError is the panic value of a route (or group, option or middleware) that cannot work. Its text is the label, the reason, and where it was registered:

```text
GET /users/{id}: the pattern has {id}, but GetUser.Path has no field for it
	registered at main.go:14
```

It is an error so that a test can recover it and read the text with `errors.As`; it is not recovered by the framework, because a program with a refused route is meant to stop. Every message is a row of the refusal table.

<!-- compiles: decls -->

```go
func refusedWith(register func(*bites.App)) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			var re *bites.RegistrationError
			if err, ok := r.(error); ok && errors.As(err, &re) {
				msg = re.Error()
			}
		}
	}()
	register(bites.New())
	return ""
}
```

Decision: [ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md).

### `RegistrationError.Error`

```go
func (e *RegistrationError) Error() string
```

Error returns the text: the label, the reason, a newline, a tab, and `registered at file.go:N`.
