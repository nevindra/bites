# Errors

**The errors a handler returns to choose a status, the located errors a Validate method returns, the cause of a cancelled request, and the problem+json shape every error answer has.**

**Guide:** [errors](../guide/errors.md) · **Design:** [errors](../design/errors.md)

Every error answer the framework writes is `application/problem+json` with wire names ([ADR 0012](../adr/0012-client-errors-are-400-problem-json.md)). There are two shapes. The 400 for a request that does not fit the Input lists its errors:

```json
{
  "title": "Bad Request",
  "status": 400,
  "errors": [
    {"location": "path.id", "message": "expected an integer", "rule": "type"},
    {"location": "body.room", "message": "must be between 3 and 40 characters", "rule": "length", "min": 3, "max": 40},
    {"location": "body.items[2].qty", "message": "required", "rule": "required"}
  ],
  "truncated": true,
  "request_id": "9f3a..."
}
```

An entry has `location` and `message`, and may have `rule`, a machine-readable code, with `min` and `max` for the three rules that have bounds (a side the type does not bound, such as the top of `Range() (0, math.MaxInt64)`, is left out; a `range` on a float type carries them as JSON numbers, `"min":0.01`), and a `format` member naming the format for the rule `format`. `type` is a value of the wrong type, `required` a missing member, `length` a string outside a `Length` method's bounds, `range` an integer or a float outside a `Range` method's, `items` a list outside an `Items` method's, `members` a map outside a `Members` method's (with `min` and `max`), `unique` an item that repeats an earlier one of a `Unique` list (located at that item, no members), `multiple` a number that is not a multiple of a `MultipleOf` method's step (the step is the member `multiple`; a float `range` with an `ExclusiveMin` or `ExclusiveMax` names that side `exclusive_min` or `exclusive_max` instead of `min` or `max`), `format` a string that is not of its type's checked `Format` (`email`, `uri`, `hostname`, `ipv4`, `ipv6`, `date`, `date-time` or `uuid`; the entry names it in `format`), `pattern` a string that does not match a `Pattern` method's expression (no bounds), `nonempty` a `NonEmpty` that is empty, `positive` a `PositiveInt` or `PositiveInt64` that is not, `values` a string outside an enum's `Values`, and `check` a `Check` method's error (the message is its text). A mistake with no code has no `rule` member, and an `Invalid` from `Validate` has none. A rule and its bounds never contain the client's value. Decision: [ADR 0012](../adr/0012-client-errors-are-400-problem-json.md).

Every other error answer (404, 405 with `Allow`, 413, 415, 431, 500, 503 and the status of a `StatusError`) has `title`, `status`, and `detail` when there is a message, and `request_id` as the last member when a middleware set one ([`RequestID`](./middleware.md#requestid)). At most 20 errors are listed, with `"truncated": true` when there were more, and a message never echoes more than 64 bytes of the client's value. Decoding does not stop at the first error; a syntax error in a JSON body is the one that ends it. The two exceptions to 400 are about the request's form: a body the route does not accept is a 415 and a request over a limit is a 413 or a 431.

A handler's error is found through the whole chain (a `*StatusError`, or an error type with an `HTTPStatus` method, [`HasStatus`](#hasstatus)), so `fmt.Errorf("load user: %w", bites.NotFound("no such user"))` is a 404 and the outer text never reaches the client ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)). Then the functions given to [`WithErrorMapper`](#witherrormapper) are asked, for an error of a type the application does not own. Any error still without a status is a 500 with no message in the body, logged with the request. When the client disconnected, nothing is written and nothing is logged as an error. A `Timeout` answers 503 whatever the handler returned ([middleware](./middleware.md#timeout)). The framework maps no driver's errors: turning `sql.ErrNoRows` into a 404 is a line of the application's.

<!-- compiles: decls -->

```go
var errNoRows = errors.New("no rows")

func findUser(id int64) (string, error) {
	if id == 0 {
		return "", errNoRows
	}
	return "ada", nil
}

type GetUser struct{ Path struct{ ID int64 } }

func getUserName(ctx context.Context, in GetUser) (bites.Text, error) {
	name, err := findUser(in.Path.ID)
	if errors.Is(err, errNoRows) {
		return bites.Text{}, fmt.Errorf("find user %d: %w", in.Path.ID, bites.NotFound("no such user").With("code", "user_not_found"))
	}
	if err != nil {
		return bites.Text{}, err
	}
	return bites.Text{Body: name}, nil
}
```

Guide: [errors](../guide/errors.md).

## Status errors

### `StatusError`

```go
type StatusError struct {
	Status  int
	Message string
	// Has unexported fields.
}
```

StatusError is an error that carries its own HTTP status. A handler (or a Provider, a middleware, a `Validate` method) returns one, directly or wrapped with `%w`, and the answer has that status and `Message` as its `detail`. To read one from a returned error, use `errors.As(err, &se)` with `var se *bites.StatusError`. Upload reads return one for what the client got wrong (400, 408, 413, 431), so returning it answers correctly.

A `Status` below 400 or above 599 is answered as a 500 and logged, because it can only be a mistake.

### `StatusError.Error`

```go
func (e *StatusError) Error() string
```

Error returns `Message`, or the status and its text (`404 Not Found`) when the message is empty.

### `StatusError.With`

```go
func (e *StatusError) With(name string, value any) *StatusError
```

With returns a copy that also carries a problem+json member, such as a machine-readable code. The members `title`, `status` and `detail` belong to the framework and are ignored. The extra members are written in name order, so the answer is the same bytes every time. The receiver is not changed.

### `StatusError.WithHeader`

```go
func (e *StatusError) WithHeader(name, value string) *StatusError
```

WithHeader returns a copy whose answer also carries the response header `name: value`, after any it already carries. It is how a Provider, which answers only with an error, sends the `WWW-Authenticate` challenge of a 401 or the `Retry-After` of a 429. A header the engine writes itself (`Content-Type`, `Content-Length`, `Date`, `Connection`, `Transfer-Encoding`), or a name that is not an HTTP token, is left out and logged at Error; a CR, LF or NUL in the value is replaced by a space. The receiver is not changed.

```go
return User{}, bites.TooManyRequests("slow down").WithHeader("Retry-After", "30")
```

Guide: [security](../guide/security.md). Decision: [ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md).

### `HasStatus`

```go
type HasStatus interface {
	error
	HTTPStatus() (status int, message string)
}
```

HasStatus is implemented by an application's own error type to say which HTTP answer it is, so a handler returns it, wrapped or not, without converting it at each return: `func (e *NotFoundError) HTTPStatus() (int, string) { return 404, e.What + " does not exist" }`. It is found by the same walk of the error chain as a [`StatusError`](#statuserror) (through `Unwrap` and through the branches of an `errors.Join`), and the nearer of the two wins: an application error wrapping a `StatusError` answers as itself, and a `StatusError` wrapping the application's error answers as the status error. The answer has the returned status and the returned message as its `detail`; the text of the errors that wrap it, and the error's own `Error()`, never reach the client. A status below 400 or above 599 is answered as a 500 and logged, as for `NewError`. The answer carries no extra members and no headers: an error that needs a machine-readable `code` or a `Retry-After` is converted with [`NewError`](#newerror), `With` and `WithHeader` in the handler. A `Verify` function given to the auth Providers may return one, and a 401 gets its `WWW-Authenticate` challenge like a `StatusError`'s. The framework still maps no driver's errors: the application implements the method on its own type.

### `WithErrorMapper`

```go
func WithErrorMapper(fn func(error) *StatusError) Option
```

WithErrorMapper gives the app a function that turns an error of a type the application does not own, and so cannot give an `HTTPStatus` method (a database driver's, say), into the answer it stands for. The function is given the error as it was returned, wrapped and joined as it came, so it uses `errors.Is` and `errors.As`, and returns a [`StatusError`](#statuserror), or nil for an error it does not know. The core knows no driver; the application writes the mapping:

```go
bites.WithErrorMapper(func(err error) *bites.StatusError {
	var pg *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return bites.NotFound("no such record")
	case errors.As(err, &pg) && pg.Code == "23505":
		return bites.Conflict("already exists").With("code", "duplicate")
	case errors.As(err, &pg) && (pg.Code == "23503" || pg.Code == "23514"):
		return bites.UnprocessableEntity("the record breaks a rule")
	}
	return nil
})
```

It is asked only when the error's chain has no `*StatusError` and no [`HasStatus`](#hasstatus), so an answer the application's own types give always wins, and it is asked for every error that is answered: a handler's, a Provider's (a `Verify` function's included), a middleware's, a stream's and a WebSocket upgrade's. It is not asked when the client has left. The answer is exactly a `StatusError`'s: the message is the `detail` and an empty one leaves it out, `With` members and `WithHeader` headers are sent, and the message of a 5xx is sent as written, so write one the client may read. A mapped 401 gets no `WWW-Authenticate` unless the mapper adds it with `WithHeader`.

Logging: a mapped 4xx is not logged, as a `StatusError` returned by a handler is not. A mapped 5xx is logged at Error with the original error, the status, the method and the path, because the mapper's message hides the reason. A status below 400 or above 599 is a 500 and is logged with the error, as for `NewError`. A function that panics is recovered: the panic and the original error are logged, the error is answered as unmapped (a 500 with no message), and the functions after it are not asked.

It may be given more than once: the functions are asked in the order given and the first answer wins, so a module can add its own. A nil function makes `New` panic: ``WithErrorMapper: the function is nil: give the function that turns an error into a *StatusError, or leave the option out``, with the line that gave it. It is the app's and not a Group's, because a driver's error means the same on every route.

Cost: none on a success, which stays at 2 allocations for a typed JSON request. On the error path a mapped error costs what the same `StatusError` returned by the handler costs; an unmapped error pays one call per function and, only if one panics, a deferred recover. Decision: [ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md).

### `NewError`

```go
func NewError(status int, message string) *StatusError
```

NewError returns a StatusError for `status`, which should be 400 or above; any other status is answered as a 500 and logged. Use it for a status that has no constructor here. The problem's `title` is the status's registered reason phrase (`NewError(423, "...")` is `Locked`), or `Status N` for a status IANA registers no phrase for.

### `BadRequest`

```go
func BadRequest(message string) *StatusError
```

BadRequest is a 400 for a mistake the Input's types could not catch. Its answer has the second shape (`detail`), not the list of the decoding 400.

### `Unauthorized`

```go
func Unauthorized(message string) *StatusError
```

Unauthorized is a 401. It is what a Provider that authenticates returns ([providers](./providers.md#provide)).

### `Forbidden`

```go
func Forbidden(message string) *StatusError
```

Forbidden is a 403.

### `NotFound`

```go
func NotFound(message string) *StatusError
```

NotFound is a 404.

### `Conflict`

```go
func Conflict(message string) *StatusError
```

Conflict is a 409.

### `PreconditionFailed`

```go
func PreconditionFailed(message string) *StatusError
```

PreconditionFailed is a 412: a condition the request sent, such as `If-Match` with the version the client last read, does not hold for the resource now (RFC 9110 section 15.5.13). A handler that compares the version itself returns it, so the client reads the resource again instead of overwriting a change it has not seen. [`File`](./responses.md#file) answers 412 by itself for its own validators.

<!-- compiles: body -->

```go
app.Put("/rooms/{id}", func(ctx context.Context, in struct {
	Path   struct{ ID int64 }
	Header struct{ IfMatch string }
	Body   struct{ Name string }
}) (bites.NoContent, error) {
	if in.Header.IfMatch != `"7"` {
		return bites.NoContent{}, bites.PreconditionFailed("the room changed since version 7 was read: read it again")
	}
	return bites.NoContent{}, nil
}, bites.ErrorStatuses(412))
```

### `PreconditionRequired`

```go
func PreconditionRequired(message string) *StatusError
```

PreconditionRequired is a 428: the route changes a resource only when the request says which version it read, and this one sent no `If-Match` (RFC 6585 section 3). Return it when the header is missing, and [`PreconditionFailed`](#preconditionfailed) when it names an old version; the message says what to send. A required `Header` field would answer a missing `If-Match` with a 400 naming `header.If-Match`, which does not say which value to send, so an `Opt` field and this constructor are the way to say it.

<!-- compiles: body -->

```go
app.Put("/notes/{id}", func(ctx context.Context, in struct {
	Path   struct{ ID int64 }
	Header struct{ IfMatch bites.Opt[string] }
	Body   struct{ Text string }
}) (bites.NoContent, error) {
	tag, ok := in.Header.IfMatch.Get()
	if !ok {
		return bites.NoContent{}, bites.PreconditionRequired(`send If-Match with the ETag you read, "3"`)
	}
	if tag != `"3"` {
		return bites.NoContent{}, bites.PreconditionFailed("the note changed since you read it: read it again")
	}
	return bites.NoContent{}, nil
}, bites.ErrorStatuses(412, 428))
```

### `UnprocessableEntity`

```go
func UnprocessableEntity(message string) *StatusError
```

UnprocessableEntity is a 422, for an application that wants it; the framework itself answers every input mistake with 400.

### `TooManyRequests`

```go
func TooManyRequests(message string) *StatusError
```

TooManyRequests is a 429. To send `Retry-After`, answer from a middleware with `Problem` and `SetHeader` ([middleware](./middleware.md#problem)).

### `ServiceUnavailable`

```go
func ServiceUnavailable(message string) *StatusError
```

ServiceUnavailable is a 503.

## Located errors

### `Invalid`

```go
func Invalid(location, message string) error
```

Invalid is a located input error for one field. A `Validate` method returns it, and so may a handler, a Provider or a middleware that finds a field wrong only after a lookup (an order that names a product that does not exist): found anywhere in the returned error's tree, it makes the answer the 400 that lists it, and a status elsewhere in the tree does not change that. Several are returned together with `errors.Join`, and a located error wrapped with `%w` is found too. The 400 lists them in the order returned, within the same cap of 20 and the same `truncated`. `location` is written by hand in wire names (`body.end`, `query.limit`, `body.items[2].qty`); the framework cannot check it, so a wire name changed by the naming policy leaves a stale string behind. The message is a short sentence about the field, without the client's value.

`Validate` is a method of the Input, declared by value with the type `func() error`. It runs once, after every Section has decoded without an error and before the handler, so it never sees a half-filled Input. It has no context and must not block; a check that needs a database is the handler's job. An error from `Validate` with no located error in it is answered as a handler's error would be: its status if it carries one (`Conflict(...)`), else a 500 with no message, logged.

<!-- compiles: decls -->

```go
type Booking struct {
	Body struct {
		Start time.Time
		End   time.Time
	}
}

func (b Booking) Validate() error {
	var errs []error
	if !b.Body.Start.After(time.Now()) {
		errs = append(errs, bites.Invalid("body.start", "must be in the future"))
	}
	if !b.Body.End.After(b.Body.Start) {
		errs = append(errs, bites.Invalid("body.end", "must be after body.start"))
	}
	return errors.Join(errs...)
}
```

Refused at Registration:

| Mistake | Message (after the route label) |
|---|---|
| a `Validate` of another type | `WrongValidate has a Validate method of type func() string; it must be func() error, so that it runs after decoding and reports its errors with Invalid` |
| a pointer receiver | `PtrValidate.Validate has a pointer receiver, but an Input is taken by value, so the method would never be found: declare it on PtrValidate` |
| a `Validate` on a Section | `BodyValidate.Body has a Validate method, but only the Input's Validate runs: move the checks to BodyValidate.Validate` |

Allocations: a `Validate` that returns nil adds none (2 for a typed JSON request); one that fails with one `Invalid` allocates 3 ([ADR 0003](../adr/0003-the-trade-budget.md), [ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)).

Guide: [validation](../guide/validation.md).

### `FieldError`

```go
type FieldError struct {
	// Location is in wire names, never Go field names: body.name, query.limit, header.X-Token
	// (a header by its canonical name, which the naming policy does not touch, ADR 0011),
	// body.items[2].qty. The framework cannot check it, because it is written by hand.
	Location string
	// Message says what was expected. Write it as the framework's own are written: a short
	// sentence about the field, without the client's value in it.
	Message string
}
```

FieldError is one located input error: where in the request, and what is wrong with it. It is what `Invalid` returns (as a `*FieldError`) and what the 400 lists. To read the located errors out of a returned error, use `errors.As(err, &fe)` with `var fe *bites.FieldError`. On a WebSocket route, a message that did not decode is a `*MalformedError` whose `Errors` are `[]FieldError` located as `message.<field>` ([websocket](./websocket.md#malformederror)).

### `FieldError.Error`

```go
func (e *FieldError) Error() string
```

Error returns the location, a colon and a space, and the message (`body.end: must be after body.start`).

## Cancellation

### `ErrClientGone`

```go
var ErrClientGone = reqctx.ErrClientGone
```

ErrClientGone is the cause of a request's context when the client disconnected while the handler ran ([ADR 0016](../adr/0016-the-handler-context-is-cancelled-on-disconnect.md)). Compare what [`Cause`](#cause) returns with it; `context.Cause` cannot report it and returns `context.Canceled`. When the cause is ErrClientGone and the handler returned an error, set no answer or panicked, nothing is written and the connection is closed; a value the handler returned is still written ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)). Its text is `bites: client disconnected`.

### `Cause`

```go
func Cause(ctx context.Context) error
```

Cause reports why `ctx` was cancelled, or nil while it is live. For a request's context, and for a context derived from one with `WithValue`, `WithCancel` or `WithTimeout`, it is the request's own cause where `context.Cause` would only say `context.Canceled`: [`ErrClientGone`](#errclientgone) when the client left, [`ErrServerClosed`](./app.md#errserverclosed) when `Shutdown` cut a live stream, `context.DeadlineExceeded` after [`Timeout`](./middleware.md#timeout). A cause of the derived context's own, its deadline or the cause given to its cancel function, is reported as it is. For any other context it is `context.Cause`. It allocates nothing.

```go
if bites.Cause(ctx) == bites.ErrClientGone {
	return Empty{}, ctx.Err() // nobody is waiting
}
```
