# Getting started

**Install bites, write a typed app of two routes, and see what a client gets when it is right, when it is wrong, and when you are.**

**Reference:** [`New`, `App`, options](../reference/app.md) · **Design:** [Principles](../design/principles.md)

## Requirements

**bites needs Go 1.27 or later.** Its route methods (`app.Get`, `app.Post`) are generic methods, which Go gained in 1.27, so that the compiler knows a handler's Input and output types at the call ([ADR 0004](../adr/0004-go-1-27-minimum.md)). The core imports the standard library only ([ADR 0006](../adr/0006-no-third-party-dependency-in-the-core.md)), so adding it adds no other module to yours.

## Installing the module

**Add the module and import the package `bites`.** The module path is `github.com/nevindra/bites` and the package is named `bites`, because a Go package name cannot contain a hyphen.

```sh
go get github.com/nevindra/bites
```

Before 1.0 a release may change an API when that makes the design cleaner, and says what to change ([ADR 0034](../adr/0034-versioning-before-1-0.md)).

## Writing a first app

**A route is a plain function that takes a context and one Input struct and returns a value and an error.** The Input names where each field comes from by the name of its section: `Path`, `Query`, `Header`, `Cookie` or `Body` ([ADR 0010](../adr/0010-one-input-struct-with-sections-by-field-name.md)). Nothing else is declared: no tags, no binding calls, no schema.

<!-- compiles: decls -->
```go
type Note struct {
	ID    int64
	Title string
}

// GetNote reads {id} from the path and an optional ?shout=true from the query.
type GetNote struct {
	Path  struct{ ID int64 }
	Query struct{ Shout bites.Opt[bool] }
}

// CreateNote reads a JSON body with a required, non-empty title.
type CreateNote struct {
	Body struct{ Title bites.NonEmpty }
}

type Notes struct{}

func (Notes) Get(ctx context.Context, in GetNote) (Note, error) {
	title := "a note"
	if in.Query.Shout.Or(false) {
		title = "A NOTE"
	}
	return Note{ID: in.Path.ID, Title: title}, nil
}

func (Notes) Create(ctx context.Context, in CreateNote) (bites.Created[Note], error) {
	note := Note{ID: 1, Title: string(in.Body.Title)}
	return bites.Created[Note]{Body: note, Location: "/notes/1"}, nil
}

func newApp() *bites.App {
	app := bites.New()
	notes := Notes{}
	app.Get("/notes/{id}", notes.Get)
	app.Post("/notes", notes.Create)
	return app
}
```

`Get` returns a `Note`, which is answered as a 200 with the value as JSON. `Create` returns [`Created`](../reference/responses.md#created), a wrapper that makes the status 201 and sets the `Location` header ([ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md)). `Opt` says a field may be absent ([`Opt`](../reference/input.md#opt)), and [`NonEmpty`](../reference/input.md#nonempty) says a string must not be empty. A plain field is required ([ADR 0041](../adr/0041-presence-is-a-type.md)).

The routes are added with [`App.Get`](../reference/app.md#appget) and [`App.Post`](../reference/app.md#apppost) on an app made by [`New`](../reference/app.md#new). The handlers are method values of a struct, which is how a handler reaches a database: see [Handlers](./handlers.md#handlers-that-need-a-service).

## Running it

**[`App.Listen`](../reference/app.md#applisten) serves until the app is shut down.** It returns [`ErrServerClosed`](../reference/app.md#errserverclosed) after a clean [`App.Shutdown`](../reference/app.md#appshutdown), so that value is not a failure.

<!-- compiles: decls -->
```go
func main() {
	app := newApp()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = app.Shutdown(shutdown)
	}()

	if err := app.Listen(":8080"); err != nil && !errors.Is(err, bites.ErrServerClosed) {
		log.Fatal(err)
	}
}
```

The framework installs no signal handler of its own ([ADR 0029](../adr/0029-shutdown-drains-and-then-closes.md)); the lines above that catch the signal and call `Shutdown` are the application's. [Deploying](./deploying.md) covers TLS, proxies and what shutdown does with requests in flight.

## What a client sees for a good request

**A route that returns a value answers 200 with the JSON of that value.**

```http
GET /notes/7?shout=true HTTP/1.1
Host: localhost:8080

HTTP/1.1 200 OK
Content-Type: application/json
Content-Length: 25

{"id":7,"title":"A NOTE"}
```

A `POST` with a title answers the 201 the wrapper declared:

```http
POST /notes HTTP/1.1
Host: localhost:8080
Content-Type: application/json

{"title":"buy milk"}

HTTP/1.1 201 Created
Location: /notes/1
Content-Type: application/json

{"id":1,"title":"buy milk"}
```

## What a client sees for a bad request

**A mistake in the input is a 400 that names every field the client got wrong, in the names the client uses.** The body is `application/problem+json` ([ADR 0012](../adr/0012-client-errors-are-400-problem-json.md)), and each entry of `errors` has a `location` and a `message`. The message never contains the value the client sent.

```http
GET /notes/abc?shout=maybe HTTP/1.1
Host: localhost:8080

HTTP/1.1 400 Bad Request
Content-Type: application/problem+json

{"title":"Bad Request","status":400,"errors":[{"location":"path.id","message":"expected an integer"},{"location":"query.shout","message":"expected true or false"}]}
```

Your handler did not run, and you wrote no code for this answer. The same holds for a body: an empty title, a member the type does not have, and a missing `Content-Type` are each refused before `Create` is called ([Requests](./requests.md), [Errors](./errors.md)).

## What a Registration refusal looks like

**A mistake in the types is refused when the route is registered, before the first request, and the program stops.** The panic is a [`RegistrationError`](../reference/app.md#registrationerror) whose text names the route, the field and the file and line of the call ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)). Suppose the pattern names `{id}` and the Input forgot to declare it:

```go
type GetNote struct {
	Path struct{ Name string } // the pattern says {id}
}

app.Get("/notes/{id}", notes.Get)
```

```text
panic: GET /notes/{id}: the pattern has {id}, but GetNote.Path has no field for it
	registered at main.go:14
```

The same happens for a pointer field (use `Opt[T]` or `Null[T]`), a `Body` on a `GET`, two patterns that match the same requests, an `Upload` route with no `MaxBody`, and every other way a route cannot work. A refusal is output: each one has a row in the refusal table of the tests, so the message is something to read, not something to guess.

## Catching every refusal with one test

**Build the app in a test, and every refusal in the program fails that test.** Registration happens when the routes are added, not when a request arrives, so building the app is all it takes. Keep the routes in one function that returns the app, as `newApp` above does, and call it:

<!-- compiles: decls -->
```go
func TestTheAppRegisters(t *testing.T) {
	newApp() // a Registration mistake panics here, naming the route and the line
}

func TestGettingANote(t *testing.T) {
	srv := bitetest.Start(t, newApp())
	resp := srv.Do(bitetest.Request{Method: "GET", Target: "/notes/7"})
	if resp.Status != 200 {
		t.Fatalf("status %d: %s", resp.Status, resp.Text())
	}
}
```

[`bitetest.Start`](../reference/bitetest.md#biteteststart) serves the app over memory, through the same engine a socket would reach, so the second test needs no port ([Testing](./testing.md), [ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)).

## Where to go next

- [Handlers](./handlers.md): the handler's shape, services, the context, and what a handler must not do.
- [Requests](./requests.md): everything the five sections can read.
- [Responses](./responses.md): statuses, headers and cookies.
- [Errors](./errors.md): the answers you return on purpose.
- [Deploying](./deploying.md): TLS, shutdown and proxies.
