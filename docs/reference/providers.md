# Providers

**The functions that fill the fields of an Input that are not sections, registered on the app, a group or a route, and checked at Registration.**

**Guide:** [providers](../guide/providers.md) · **Design:** [middleware and Providers](../design/middleware-and-providers.md)

Besides its Sections, an Input may have fields of a type that a Provider fills: a function from the request view to that type or an error. The usual case is the authenticated user: `Auth User` in an Input is filled by the Provider registered for `User`. A Provider is a function the application registers; it reaches Postgres or a token key through whatever it closes over, as a handler does through its struct ([ADR 0014](../adr/0014-services-are-plain-go-values.md)). The framework injects nothing else.

A Provider has the shape `func(ctx context.Context, req Request, deps D) (T, error)`:

- `T` is the type it provides. It must be a distinct type for each Provider; a Provider that refines a value provides a new type, such as `Admin` from `User`.
- `D` is a struct whose exported fields are the other provided types it needs, filled before the Provider runs, or `struct{}` when it needs none. `T` and `D` are inferred from the function. A field of `D` named `Header` or `Cookie` is a section instead, a struct decoded and checked as the Input's is before the Provider runs (a missing or malformed value is the 400 naming it), and stated in the API document as a parameter of every route that runs the Provider. A `Query` field is a section too, on a WebSocket route or a route that answers `SSE` or `Events` only, since a browser opens those without a header of its own; on any other route it is refused (`GET /x: WithA: Provide[PA] reads a Query section, which a Provider may read only on a route a browser opens without headers of its own, a WebSocket route or one that answers SSE or Events: elsewhere send the value in a header, or ask for it in the Input`), and a strict query lets its keys through. A `Path` or `Body` field of `D` is refused (`app: Provide[PA]: the dependency Path is a section a Provider cannot read; a Provider reads a Header, a Cookie or (on a route a browser opens without headers) a Query section, and the path and the body are the Input's: ask for the value in the Input, or give the Provider a Header`), and so is a section that is not a struct (`app: Provide[PA]: the dependency Header is a section and must be a struct, not string`).
- `req` is the read-only request view of [middleware](./middleware.md#request). `Request.AddLogAttrs` records what the Provider found for the access log.
- An error it returns is the answer, like a handler's (a `Unauthorized` is a 401, a `Forbidden` a 403), and the handler does not run. A panic in one is the engine's 500.

A Provider runs only when the route's Input, or a Provider it needs, names its type, so a costly one is paid only by the routes that ask for it, and at most once a request. It runs after the group and route middleware and before the Input is decoded. A route runs at most 16 Providers. A route with Providers is served through the middleware chain even when it has no middleware.

Scopes: a Provider registered on the app covers every route; on a group, every route in it and in the groups made in it; as an option on a route, that route. Each route resolves one list, nearest scope first, at Registration, and a Provider's own dependencies resolve from that same list, so one request never holds two different `User` values. A second Provider of a type where one already covers the scope is refused; `Override` replaces one, for a scope and everything inside it, and is refused when there is nothing to replace.

An optional credential: a field or a dependency of type `Opt[T]` that no Provider of `Opt[T]` covers resolves to the optional form of the route's Provider of `T` when that one is an auth Provider ([`AnyAuth`](./security.md#anyauth) has the rule), which runs only when the request carries its credential and leaves the `Opt` absent otherwise ([ADR 0023](../adr/0023-provided-sections-are-checked-at-registration.md), [ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)).

Allocations: a typed POST whose Input asks for provided types is 2, at any depth of the graph, with a Provider that reads its own `Header` section, and with or without middleware ([ADR 0003](../adr/0003-the-trade-budget.md)).

<!-- compiles: decls -->

```go
type Session struct{ Token string }

type User struct{ ID int64 }

type Admin struct{ User User }

func session(ctx context.Context, req bites.Request, _ struct{}) (Session, error) {
	tok := req.Header("Authorization")
	if tok == "" {
		return Session{}, bites.Unauthorized("sign in first")
	}
	return Session{Token: tok}, nil
}

func user(ctx context.Context, req bites.Request, deps struct{ Session Session }) (User, error) {
	req.AddLogAttrs(slog.String("token_len", strconv.Itoa(len(deps.Session.Token))))
	return User{ID: 1}, nil
}

func admin(ctx context.Context, req bites.Request, deps struct{ User User }) (Admin, error) {
	if deps.User.ID != 1 {
		return Admin{}, bites.Forbidden("admins only")
	}
	return Admin{User: deps.User}, nil
}

type DeleteAccount struct {
	Path  struct{ ID int64 }
	Admin Admin
}

func deleteAccount(ctx context.Context, in DeleteAccount) (bites.NoContent, error) {
	return bites.NoContent{}, nil
}
```

<!-- compiles: body -->

```go
app = bites.New()
app.Provide(session)
app.Provide(user)
app.Provide(admin)
app.Delete("/accounts/{id}", deleteAccount)
```

What Registration refuses, as rows of the refusal table (the label is `app`, `group "/g"` or the route, and the line `registered at file.go:N` follows):

| Mistake | Message |
|---|---|
| a field that is not a section and no Provider covers it | `GET /x: WithAuth.Auth is not a section (Path, Query, Header, Cookie or Body), and no Provider of string covers this route` |
| a Provider needs a type no Provider covers | `GET /x: WithA.A needs PA (at file.go:N) needs PB, and no Provider of PB covers this route` |
| Providers that need each other | `GET /x: the Providers form a cycle: WithA.A needs PA (at file.go:N) needs PB (at file.go:N) needs PA` |
| a Provider that needs its own type | `app: Provide[PA]: the Provider of PA needs PA, which is a cycle; a Provider that refines a value provides a distinct type, such as Admin from User` |
| dependencies that are not a struct | `app: Provide[PA]: the Provider's dependencies are int; they must be a struct whose fields are the provided types it needs, or struct{} when it needs none` |
| an unexported dependency | `app: Provide[PA]: the dependency b is unexported, so nothing can fill it` |
| a nil Provider | `app: Provide[PA]: the Provider is nil` |
| a second Provider of a type in one scope | `app: Provide[PA]: this scope already has a Provider of PA (at file.go:N); a scope has one Provider for a type` |
| a Provider of a type an enclosing scope provides | `group "/g": Provide[PA]: an enclosing scope already has a Provider of PA (at file.go:N); use Override[PA] to replace it for this scope and those inside it` |
| an Override with nothing to replace | `group "/g": Override[PA]: no enclosing scope has a Provider of PA to replace; use Provide[PA]` |
| an Override of the same scope's own Provider | `group "/g": Override[PA]: the Provider of PA it would replace is this scope's own (at file.go:N); an Override replaces one from an enclosing scope, so keep one of the two` |
| a route that needs more than 16 Providers | `GET /x: Many needs 17 Providers; a route runs at most 16` |
| an `Opt[T]` whose `T` has a Provider that reads no credential the framework knows | `GET /x: MaybeWho.P is not a section (Path, Query, Header, Cookie or Body), and no Provider of Opt[Principal] covers this route; the Provider of Principal (at file.go:N) does not read a credential the framework knows, so it has no optional form: make it with BearerAuth, BasicAuth, APIKeyAuth, CustomAuth or AnyAuth, or provide Opt[Principal] itself` |
| a route that needs `T` and `Opt[T]` | `GET /x: BothWho needs both Principal and Opt[Principal], so the credential is required on this route: ask for Principal` |
| `Provide` or `Override` after the scope has a route or a child group | `group "/g": Provide is called after GET /g/x (at file.go:N) was registered in this group, so it would not reach it: move Provide above it`; for the app, `app: Provide is called after GET /x (at file.go:N) was registered, which was checked without it: move Provide above it` |

Guide: [providers](../guide/providers.md). Decisions: [ADR 0023](../adr/0023-provided-sections-are-checked-at-registration.md), [ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md).

## The option and the function

### `Provide`

```go
func Provide[T, D any](fn func(context.Context, Request, D) (T, error)) RouteOption
```

Provide registers `fn` as the Provider of `T` for a route, or for every route of a group when it is given to `Group`. It is the option spelling of [`App.Provide`](#appprovide) and [`Group.Provide`](#groupprovide). An option cannot be late: it is applied when the scope is made. A nil `fn` is refused (`GET /x: Provide[PA]: the Provider is nil`).

### `Override`

```go
func Override[T, D any](fn func(context.Context, Request, D) (T, error)) RouteOption
```

Override replaces the Provider of `T` that an enclosing scope registered, for this route or group and everything inside it: a fake `User` in a test, or a subtree that authenticates differently. With nothing to replace it is refused. The Providers that need `T`, wherever they were registered, receive the replacement on the routes it covers.

<!-- compiles: body -->

```go
app = bites.New()
app.Provide(session)
app.Provide(user)
api := app.Group("/api", bites.Override(func(ctx context.Context, req bites.Request, _ struct{ Session Session }) (User, error) {
	return User{ID: 99}, nil
}))
api.Delete("/accounts/{id}", deleteAccount, bites.Provide(admin))
```

## On the app and on a group

### `App.Provide`

```go
func (a *App) Provide[T, D any](fn func(context.Context, Request, D) (T, error))
```

Provide registers `fn` as the Provider of `T` for every route of the app. It must come before the app's first route and first group: a route's Providers are checked at its own line, and a group copies the app's Providers when it is made, so a later `Provide` would be missing there. An app has no `Override`, since nothing encloses it.

### `Group.Provide`

```go
func (g *Group) Provide[T, D any](fn func(context.Context, Request, D) (T, error))
```

Provide registers `fn` as the Provider of `T` for every route of this group and of the groups made in it. Like `Use` it must come before the group's first route and first child group.

### `Group.Override`

```go
func (g *Group) Override[T, D any](fn func(context.Context, Request, D) (T, error))
```

Override replaces, for this group and the groups made in it, the Provider of `T` that an enclosing scope registered. It must come before the group's first route and first child group, and is refused when no enclosing scope has a Provider of `T`.
