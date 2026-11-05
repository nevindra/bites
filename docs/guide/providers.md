# Providers

**A Provider builds a value your Input asks for, such as the authenticated user, so a handler receives it ready and a request that cannot have it is answered before the handler runs.**

**Reference:** [Providers](../reference/providers.md) · **Design:** [Middleware and Providers](../design/middleware-and-providers.md)

A Provider is a function registered for a type. An Input field of that type, which is not one of the sections (`Path`, `Query`, `Header`, `Cookie`, `Body`), is filled by it. The whole graph of Providers is checked at Registration, so a missing one or a cycle is a panic at start-up and never a nil user in production ([ADR 0023](../adr/0023-provided-sections-are-checked-at-registration.md)). The examples build the typical case: a credential from a header, and the user behind it. For sessions kept in a cookie, see [Session](./security.md#keeping-a-session).

<!-- compiles: decls -->
```go
type Credential struct{ Token string }

type User struct{ Name string }

type Admin struct{ Name string }

// Directory stands in for a database pool.
type Directory interface {
	Find(ctx context.Context, token string) (name string, admin bool, ok bool)
}

// Users is the service that owns the lookup; its methods are the Providers.
type Users struct {
	dir Directory
}

type Greeting struct {
	Name string
}
```

## Reading the authenticated user from a header

**Register a Provider with [`app.Provide`](../reference/providers.md#appprovide), then put a field of the provided type in the Input.** The Provider receives the context, the [`Request`](../reference/middleware.md#request) view and its dependencies, and returns the value and an error. Any function of that shape is a Provider: a plain function for one that needs nothing, and a method on the service that owns the lookup for one that does, registered as a method value exactly as a handler is ([ADR 0014](../adr/0014-services-are-plain-go-values.md)).

<!-- compiles: decls -->
```go
func credentialFromHeader(ctx context.Context, req bites.Request, _ struct{}) (Credential, error) {
	token := strings.TrimPrefix(req.Header("Authorization"), "Bearer ")
	if token == "" {
		return Credential{}, bites.Unauthorized("no credential")
	}
	return Credential{Token: token}, nil
}

func (u *Users) FromCredential(ctx context.Context, req bites.Request, deps struct{ Credential Credential }) (User, error) {
	name, _, ok := u.dir.Find(ctx, deps.Credential.Token)
	if !ok {
		return User{}, bites.Unauthorized("unknown credential")
	}
	req.AddLogAttrs(slog.String("user", name))
	return User{Name: name}, nil
}

type WhoAmI struct {
	User  User
	Query struct{ Shout bites.Opt[bool] }
}

func whoAmI(ctx context.Context, in WhoAmI) (Greeting, error) {
	if in.Query.Shout.Or(false) {
		return Greeting{Name: strings.ToUpper(in.User.Name)}, nil
	}
	return Greeting{Name: in.User.Name}, nil
}
```

<!-- compiles: body -->
```go
var dir Directory // the application's own lookup
users := &Users{dir: dir}
app.Provide(credentialFromHeader)
app.Provide(users.FromCredential)
app.Get("/me", whoAmI)
```

The `User` field is not read from the request: the framework runs the Provider and stores the value in it, beside the sections the Input also has. A caller with a credential gets its answer, and one without is refused before the handler:

```http
GET /me HTTP/1.1
Host: api.example.com
Authorization: Bearer abc123

HTTP/1.1 200 OK
Content-Type: application/json

{"name":"ada"}
```

```http
GET /me HTTP/1.1
Host: api.example.com

HTTP/1.1 401 Unauthorized
Content-Type: application/problem+json

{"title":"Unauthorized","status":401,"detail":"no credential"}
```

**A Provider can add to the request's log record.** `req.AddLogAttrs(slog.String("user", name))` above puts the user on the line the [access log](./first-party-middleware.md#logging-each-request) writes: the access log is an [Observer](./observability.md) that writes when the request has ended, after every Provider has run.

## Depending on another Provider

**A Provider's dependencies are the fields of a struct, matched by type.** `FromCredential` declares `struct{ Credential Credential }`, so the framework runs the `Credential` Provider first and copies its value into that field. A Provider that needs nothing takes `struct{}`. The fields must be exported, and their names are free: only the types matter.

**A Provider that refines a value provides a distinct type.** A Provider of `User` cannot need a `User`, which is a cycle the constructor refuses. To ask "is this user an administrator", provide `Admin` from `User`:

<!-- compiles: decls -->
```go
func (u *Users) Admin(ctx context.Context, req bites.Request, deps struct{ User User }) (Admin, error) {
	_, admin, ok := u.dir.Find(ctx, deps.User.Name)
	if !ok || !admin {
		return Admin{}, bites.Forbidden("administrators only")
	}
	return Admin{Name: deps.User.Name}, nil
}

type Locale struct{ Tag string }

func localeFromHeader(ctx context.Context, req bites.Request, _ struct{}) (Locale, error) {
	return Locale{Tag: req.Header("Accept-Language")}, nil
}

type Hello struct {
	Locale Locale
}

func greet(ctx context.Context, in Hello) (Greeting, error) {
	return Greeting{Name: "hello in " + in.Locale.Tag}, nil
}

type Purge struct {
	Admin Admin
}

func purge(ctx context.Context, in Purge) (Greeting, error) {
	return Greeting{Name: in.Admin.Name}, nil
}
```

A route that asks for `Admin` runs the `Credential`, `User` and `Admin` Providers in that order. A route that asks for both `User` and `Admin` runs the `Credential` Provider once.

## Reading a header the Provider needs

**A Provider reads a header or a cookie through a `Header` or `Cookie` section in its dependencies, as an Input does.** The field named `Header` (or `Cookie`) of the dependency struct is a section, not a provided type: it is decoded and checked before the Provider runs, a missing or malformed value is the 400 that names it, and the [OpenAPI document](./openapi.md) states it as a parameter of every route that runs the Provider. `req.Header(...)` still works, and is what a Provider made by `BearerAuth` uses inside; the section is for a value the Provider needs, so the client is told when it is missing.

<!-- compiles: decls -->
```go
type Tenant struct{ Slug string }

type TenantSlug string

func (TenantSlug) Length() (int, int) { return 2, 20 }

func tenantFromHeader(ctx context.Context, req bites.Request, deps struct {
	Header struct{ XTenant TenantSlug }
}) (Tenant, error) {
	return Tenant{Slug: string(deps.Header.XTenant)}, nil
}
```

A route whose Input asks for `Tenant`, with no `X-Tenant` sent, is answered before the Provider and the handler run:

```http
HTTP/1.1 400 Bad Request
Content-Type: application/problem+json

{"title":"Bad Request","status":400,"errors":[{"location":"header.X-Tenant","message":"required","rule":"required"}]}
```

The path and the body are the Input's: a `Path` or `Body` field in a Provider's dependencies is refused, since a Provider runs on routes whose paths and bodies differ. Ask for the value in the Input, or send it in a header. A `Query` section is the one exception, on the routes a browser opens without a header of its own (a WebSocket route, and a route that answers `SSE` or `Events`, which `EventSource` opens): there a Provider may read a ticket from the query, as [WebSockets](./websockets.md#a-browser-and-its-credential) shows; on any other route a Provider's `Query` is refused, naming the route. A `Header` the Input also has is decoded twice, once for each; the document lists the parameter once.

## Providing for a group or a single route

**A Provider covers the scope it is registered in: the whole app, a group and the groups in it, or one route.** [`Group.Provide`](../reference/providers.md#groupprovide) registers for a group, and the option [`bites.Provide`](../reference/providers.md#provide) for a route or, given to `Group`, a group.

<!-- compiles: body -->
```go
users := &Users{}
app.Provide(credentialFromHeader)
app.Provide(users.FromCredential)

admin := app.Group("/admin")
admin.Provide(users.Admin)
admin.Post("/purge", purge)

app.Get("/me", whoAmI)
app.Get("/hello", greet, bites.Provide(localeFromHeader))
```

`/admin/purge` is covered by `users.Admin`, `/hello` has a Provider of its own that no other route sees, and `/me` has neither: a route whose Input asks for `Admin` outside the group is refused. Providers of an outer scope are visible inside it, so the admin routes read the app's `User` Provider without registering it again.

**A type has one Provider in a scope, and an inner scope replaces an outer one only by saying so.** A second `Provide` of a type that the same scope or an enclosing one already provides is refused. [`Override`](../reference/providers.md#override) is the explicit replacement, for this scope and everything inside it. The Providers that need the type, wherever they were registered, receive the replacement on the routes it covers.

<!-- compiles: body -->
```go
users := &Users{}
app.Provide(credentialFromHeader)
app.Provide(users.FromCredential)

internal := app.Group("/internal")
internal.Override(func(ctx context.Context, req bites.Request, _ struct{}) (User, error) {
	return User{Name: "service:" + req.Header("X-Service")}, nil
})
internal.Get("/me", whoAmI)
```

Under `/internal`, `User` no longer comes from a credential, and the `Credential` Provider does not run there at all. [`Group.Override`](../reference/providers.md#groupoverride) is the method; the function `Override` is the option for a route or `Group(prefix, ...)`. An `Override` with nothing to replace is refused, and a test uses it to put a fake user in place of the real one.

## A session on every route, and the user in the handlers that ask

**To authenticate every route and still hand the user to a handler, split the two jobs: one [`UseEach`](../reference/app.md#appuseeach) guard proves who is calling and passes the proof on in the context, and a Provider reads it for the Inputs that ask.** A Provider registered alone is the other way round, authentication only where an Input has a field for it, and a route whose Input has no such field is open. The guard is the shape that leaves no route out ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)); the Provider is what keeps the handler's signature honest ([ADR 0023](../adr/0023-provided-sections-are-checked-at-registration.md)). The context the guard gives to `next.Call` is the one the Providers and the handler receive, because the guard runs before them.

<!-- compiles: decls -->
```go
// Account is who the guard proved.
type Account struct{ ID, Name string }

type accountKey struct{}

// authenticate checks the credential; the application's own lookup goes here.
func authenticate(ctx context.Context, req bites.Request) (Account, error) {
	id := req.Header("X-Session")
	if id == "" {
		return Account{}, bites.Unauthorized("not signed in")
	}
	return Account{ID: id, Name: "ada"}, nil
}

// requireSession is the function given to UseEach. It is called for every route, so a route
// is guarded unless this function says otherwise, by name.
func requireSession(r bites.RouteInfo) (bites.Middleware, error) {
	if r.OperationID == "login" || r.Builtin == bites.BuiltinOpenAPI {
		return nil, nil // the few public routes, and the API document by its built-in name
	}
	return func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		acct, err := authenticate(ctx, req)
		if err != nil {
			return bites.Response{}, err // a 401 here, before any Provider or the handler
		}
		return next.Call(context.WithValue(ctx, accountKey{}, acct)), nil
	}, nil
}

// accountFromContext is the Provider: it does not authenticate, it reads what the guard proved.
func accountFromContext(ctx context.Context, _ bites.Request, _ struct{}) (Account, error) {
	acct, ok := ctx.Value(accountKey{}).(Account)
	if !ok {
		return Account{}, bites.Unauthorized("not signed in") // an exempt route that asked for a user
	}
	return acct, nil
}

type MeInput struct{ Account Account }

func me(ctx context.Context, in MeInput) (Greeting, error) { return Greeting{Name: in.Account.Name}, nil }

func login(ctx context.Context, in struct{}) (bites.NoContent, error) { return bites.NoContent{}, nil }
```

<!-- compiles: body -->
```go
app.UseEach(requireSession) // before the first route, and before OpenAPI
app.Provide(accountFromContext)
app.OpenAPI("/openapi.json", bites.Info{Title: "Users", Version: "1"})
app.Post("/login", login, bites.OperationID("login"))
app.Get("/me", me)
```

Three things make this the whole answer. **A route nobody remembered is guarded**: `requireSession` returns a middleware for everything but the names it exempts, so a route added next year is behind the session without anyone editing the guard, and one that should be public is a line in the exempt test that a reviewer reads. **The routes the app registers for itself are in the list**: `OpenAPI` and `ServeMetrics` are passed to the function with `Builtin` set to `"openapi"` or `"metrics"`, so the example above serves the document openly on purpose, and a guard that did not mention it would answer 401 for it (and a function that refuses what it does not know would stop the program at start, naming it). The health paths of `WithHealth` are not passed: they are answered before routing and need no credential, which is what a load balancer's probe has. **The user reaches only the handlers that ask**: `/ping`, with no `Account` in its Input, never runs the Provider, and costs what a route with the guard costs. A Provider that finds no user in the context answers 401, which only an exempt route that asks for one can reach.

The guard sees a request before the Input is decoded, so it can read headers and cookies and not a path parameter or a body; a rule that depends on them (the owner of the order in the path) is checked in the Provider or the handler, with the proof already on the context. A guard's refusal (the 401 above) is a returned error, answered as problem+json like any other ([errors](./errors.md)).

## What is refused at Registration

**Every way to build the graph wrongly is a panic naming the route, the field and the line.** Each refusal is a row of the refusal table ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)):

```text
GET /me: WhoAmI.User is not a section (Path, Query, Header, Cookie or Body), and no Provider of User covers this route
	registered at main.go:21
```

```text
GET /x: WithA.A needs PA (at main.go:12) needs PB, and no Provider of PB covers this route
```

```text
GET /x: the Providers form a cycle: WithA.A needs PA (at main.go:12) needs PB (at main.go:13) needs PA
```

The full list:

- a field of a type no Provider covers on that route, and a Provider whose dependency no Provider covers, naming the chain;
- Providers that need each other (a cycle), and a Provider that needs its own type;
- dependencies that are not a struct, or have an unexported field, or a nil function;
- a `Path` or `Body` section in a Provider's dependencies, a `Query` section there on a route that is not a WebSocket route or an event stream, and a `Header`, `Cookie` or `Query` there that is not a struct;
- a second Provider of a type in the same scope, or in an inner scope without `Override`, each naming where the first was registered;
- an `Override` with no enclosing Provider to replace, and an `Override` of the same scope's own Provider;
- a route that needs more than 16 Providers;
- `Provide` or `Override` on a group after its first route or child group, and `app.Provide` after the app's first route or group, because the earlier route was checked without it ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)). The option spellings cannot be late.

## When a Provider runs, and what an error from it does

**A Provider runs once a request, and only when the route needs it.** A route whose Input has no field of the type does not run that type's Provider, and two Providers that need `Credential` share one run. The order is decided at Registration: after the group and route middleware, before the Input is decoded ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)). Among the Providers, those made by `BearerAuth`, `BasicAuth`, `APIKeyAuth` and `CustomAuth` run first, so a request with no credential is the 401 whatever the order of the Input's fields; the others run in the order of the fields that ask for them, each after the Providers it needs. A tenant read from a header and a `User` from a token, on one request with neither, answers the 401. A `User` field and a malformed body on one request therefore answers the 401 and never the 400, and a Provider that does a database lookup is not run for a request a middleware refused.

**An error from a Provider is the answer.** The handler does not run, and the error is turned into a problem+json like a handler's: [`Unauthorized`](../reference/errors.md#unauthorized) and [`Forbidden`](../reference/errors.md#forbidden) for who the caller is, [`ServiceUnavailable`](../reference/errors.md#serviceunavailable) for a directory that is down, and any other error a 500 that does not leak its text ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md); see [errors](./errors.md)). A panic in a Provider is a 500 too, and the connection serves its next request.

A provided field leaves no trace in the [OpenAPI document](./openapi.md): it is neither a parameter nor a body. What a Provider reads through its `Header` or `Cookie` section is a parameter of each route that runs it.

## Cost

**Providers add no allocation: a route with any number of them costs the 2 allocations of a plain typed request.** Each Provider's value and dependency struct live on the stack, in a frame that stays while the later Providers and the handler run below it ([ADR 0003](../adr/0003-the-trade-budget.md), the row for provided types). A Provider that reads a `Header` or `Cookie` section is 2 as well: the values are decoded into the request's slab, as the Input's are. A route runs at most 16.
