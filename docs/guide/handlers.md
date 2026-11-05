# Handlers

**A handler is a function from a context and one Input to a value and an error, and everything the framework does is read off those types.**

**Reference:** [`App`](../reference/app.md) · **Design:** [Typed handlers](../design/typed-handlers.md)

## The shape of a handler

**Every typed handler has the signature `func(ctx context.Context, in In) (Out, error)`.** `In` is a struct you declare, the Input. `Out` is what the client receives. The framework reads both types once, when the route is registered, so a type that cannot work is refused then and not at the first request ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)). The route methods are [`App.Get`](../reference/app.md#appget), [`App.Post`](../reference/app.md#apppost), [`App.Put`](../reference/app.md#appput), [`App.Patch`](../reference/app.md#apppatch), [`App.Delete`](../reference/app.md#appdelete), [`App.Head`](../reference/app.md#apphead), [`App.Options`](../reference/app.md#appoptions) and [`App.Handle`](../reference/app.md#apphandle) for any other method, and a [`Group`](../reference/app.md#group) has the same ones.

A handler with nothing to read takes `struct{}`, and one with nothing to say returns [`NoContent`](../reference/responses.md#nocontent):

<!-- compiles: decls -->
```go
type Item struct {
	ID   int64
	Name string
}

type ListItems struct {
	Query struct {
		Limit bites.Opt[bites.PositiveInt]
	}
}

func listItems(ctx context.Context, in ListItems) ([]Item, error) {
	limit := int(in.Query.Limit.Or(20))
	return []Item{{ID: 1, Name: "first"}}[:min(limit, 1)], nil
}

func ping(ctx context.Context, in struct{}) (bites.NoContent, error) {
	return bites.NoContent{}, nil
}
```

<!-- compiles: body -->
```go
app.Get("/items", listItems)
app.Get("/ping", ping)
```

Both are registered as plain functions. A function literal and a method value are registered the same way.

## The Input and its sections

**An Input is a struct whose fields are named after the part of the request they read.** A field named `Path`, `Query`, `Header`, `Cookie` or `Body` is a [Section](../../GLOSSARY.md); each Section is a struct (or, for `Body`, a struct, list, map, `[]byte`, `Raw[T]`, `Form[T]` or `Upload`), and each of its fields is one value of the request. The name of the Section says where the value comes from, so there are no tags to forget ([ADR 0010](../adr/0010-one-input-struct-with-sections-by-field-name.md)). A field that is not a Section is a value a Provider fills ([Providers](./providers.md)).

A plain field is required, `Opt[T]` may be absent, and a decoding mistake is a 400 that names the field. [Requests](./requests.md) has each Section in turn, and [Validation](./validation.md) has the rules a type cannot say.

## The output

**A handler that returns a value answers 200 with that value as JSON.** A struct, a slice, a map, a number or a string all work, and an empty `Opt` field is left out of the JSON while a `Null` is written as `null` ([ADR 0041](../adr/0041-presence-is-a-type.md)). A type that cannot be written as JSON is refused at Registration.

To answer another status, return a wrapper such as [`Created`](../reference/responses.md#created), [`NoContent`](../reference/responses.md#nocontent) or [`Redirect`](../reference/responses.md#redirect). Every wrapper also carries headers and cookies. [Responses](./responses.md) has the whole list ([ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md)).

## Returning an error

**An error return is the only way to answer with a failure status.** Return a [`StatusError`](../reference/errors.md#statuserror) made by a constructor such as [`NotFound`](../reference/errors.md#notfound) and the client gets that status with your message. Return any other error and the client gets a 500 with no message, while the error is logged with the method and path. The value you return alongside an error is dropped ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)).

<!-- compiles: decls -->
```go
type GetItem struct {
	Path struct{ ID int64 }
}

var errNoRow = errors.New("no rows")

func findItem(ctx context.Context, id int64) (Item, error) {
	if id != 1 {
		return Item{}, errNoRow
	}
	return Item{ID: 1, Name: "first"}, nil
}

func getItem(ctx context.Context, in GetItem) (Item, error) {
	item, err := findItem(ctx, in.Path.ID)
	if errors.Is(err, errNoRow) {
		return Item{}, bites.NotFound("no such item")
	}
	return item, err
}
```

```http
GET /items/2 HTTP/1.1

HTTP/1.1 404 Not Found
Content-Type: application/problem+json

{"title":"Not Found","status":404,"detail":"no such item"}
```

[Errors](./errors.md) covers the shapes of the answers, wrapping with `%w`, extra members and panics.

## Handlers that need a service

**A service is a plain Go value, and a handler is a method on it.** The framework has no container, no injection and no registry: the struct you build holds the database or client, and `users.Get` is a method value that carries it ([ADR 0014](../adr/0014-services-are-plain-go-values.md)). Declare the dependency as a small interface, and a test passes a fake.

<!-- compiles: decls -->
```go
type ItemStore interface {
	Find(ctx context.Context, id int64) (Item, error)
}

type Items struct {
	store ItemStore
}

func (s *Items) Get(ctx context.Context, in GetItem) (Item, error) {
	item, err := s.store.Find(ctx, in.Path.ID)
	if errors.Is(err, errNoRow) {
		return Item{}, bites.NotFound("no such item")
	}
	return item, err
}

type memStore struct{}

func (memStore) Find(ctx context.Context, id int64) (Item, error) {
	return findItem(ctx, id)
}

func register(app *bites.App, store ItemStore) {
	items := &Items{store: store}
	app.Get("/items/{id}", items.Get)
}
```

Registration reads the method's types exactly as it reads a function's. What a request-scoped value needs (the signed-in user, say) is not a service: it is a Provider's job ([Providers](./providers.md)).

## The context

**`ctx` is cancelled when the client goes away and when the handler returns.** Hand it to every call that can block, such as a database query or an outgoing request, so work stops when nobody is waiting for the answer ([ADR 0016](../adr/0016-the-handler-context-is-cancelled-on-disconnect.md)). A handler that never looks at its context pays nothing for the watching, which starts the first time `Done` is used.

<!-- compiles: decls -->
```go
func slowSearch(ctx context.Context, in struct{}) (string, error) {
	select {
	case <-time.After(2 * time.Second):
		return "done", nil
	case <-ctx.Done():
		return "", ctx.Err() // the client left: nothing is written, nothing is logged as an error
	}
}
```

When the context was cancelled because the client disconnected, the framework writes nothing and does not log it as an error, so returning `ctx.Err()` is correct ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)). To tell a client that left from other cancellation, compare [`bites.Cause(ctx)`](../reference/errors.md#cause) with [`bites.ErrClientGone`](../reference/errors.md#errclientgone); `context.Cause` cannot see that cause and says `context.Canceled`. The context's parent is never cancelled by `Shutdown`; the engine cancels the contexts of the requests it is closing itself ([Deploying](./deploying.md)). A handler whose request has a deadline sees it as `context.DeadlineExceeded`: see [`Timeout`](../reference/middleware.md#timeout) in [First-party middleware](./first-party-middleware.md).

## What a handler must not do

**A handler never holds a writer, because there is none.** Status, headers, cookies and body all come from the value you return, and the framework writes the response once ([ADR 0009](../adr/0009-the-response-comes-only-from-the-return-value.md)). So a handler cannot write twice, set a header after the body, or send an error body and then a success body. A body written over time is a return type of its own, a [`Stream`](../reference/responses.md#stream) ([Streaming and SSE](./streaming.md)).

Two smaller rules follow from the same design:

- **An Input is yours to keep.** It is a value the handler owns. Strings in it, a `[]byte` body and `ctx` are never reused for another request, so a goroutine may keep them ([ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)). An `Upload`, a `Part`, a `StreamWriter` and a `Socket` are the exceptions: each works only while the handler or its function runs, and fails with an error afterwards.
- **A handler with more than one answer returns a `OneOf` of them, not an interface.** A [`OneOf2`](../reference/responses.md#oneof2) to `OneOf4` is checked by the compiler and needs no option; a handler that returns an interface must declare each type with [`Returns`](../reference/responses.md#returns), and an undeclared one is a 500 ([Responses](./responses.md#answering-with-one-of-several-types)).

## Where to go next

- [Routing](./routing.md): patterns, groups and the order a request goes through.
- [Requests](./requests.md): reading the five sections.
- [Responses](./responses.md): statuses, headers and cookies.
