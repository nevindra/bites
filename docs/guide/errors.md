# Errors

**Every error answer is `application/problem+json` in one shape: a mistake in the input is a 400 that lists each field, and an error you return is the status you chose or a 500 that says nothing.**

**Reference:** [Errors](../reference/errors.md) · **Design:** [Errors](../design/errors.md)

## The shape of every error answer

**Every error the framework writes has a `title` and a `status`, and may have a `detail`, an `errors` list, your own members and a `request_id`.** A client parses one shape for 400, 404, 405, 413, 415, 431, 500 and 503 alike ([ADR 0012](../adr/0012-client-errors-are-400-problem-json.md)). The body carries no stack, no Go type name and no text from a wrapped error.

```http
GET /nope HTTP/1.1

HTTP/1.1 404 Not Found
Content-Type: application/problem+json

{"title":"Not Found","status":404}
```

## Reading a 400

**A 400 names every field the client got wrong, with its location in wire names.** Decoding does not stop at the first mistake, so the client fixes them all in one round trip (a syntax error in a JSON body is the one that ends decoding). Each entry of `errors` has a `location` (`path.id`, `query.page_size`, `header.X-Api-Key`, `body.items[2].qty`) and a `message` that says what was expected, and often a `rule` code a client can translate instead of parsing the message ([ADR 0012](../adr/0012-client-errors-are-400-problem-json.md)).

```http
GET /search?page=0&color=blue HTTP/1.1

HTTP/1.1 400 Bad Request
Content-Type: application/problem+json

{"title":"Bad Request","status":400,"errors":[{"location":"query.page","message":"expected a positive integer","rule":"positive"},{"location":"query.color","message":"expected one of: red, green","rule":"values"}]}
```

The `rule` is one of `type`, `required`, `length`, `range`, `items`, `members`, `unique`, `multiple`, `pattern`, `format`, `nonempty`, `positive`, `values` and `check`; `length`, `range`, `items` and `members` also carry the bounds as `min` and `max` (a `range` on a float whose bound excludes its value names it `exclusive_min` or `exclusive_max`), `multiple` carries the step as `multiple`, and `format` carries a `format` member naming the format (`"format":"email"`), as in `{"location":"body.room","message":"must be between 3 and 40 characters","rule":"length","min":3,"max":40}`, so a client can show "between 3 and 40" in its own language. A mistake without a code has no `rule` member, and an error from your `Validate` method never has one. The server does not translate; the code is what lets the client do it.

At most 20 errors are listed, with `"truncated":true` when there were more, so a body of a million bad items cannot become a million-line answer. A message never echoes the client's value, and a field name the client sent is echoed cut to 64 bytes, so an error answer cannot be used to reflect content. An error from a `Validate` method is listed in the same answer ([Validation](./validation.md)), and so is one a handler returns when it finds a field wrong only after a lookup:

<!-- compiles: decls -->

```go
type NewOrder struct {
	Body struct{ ProductID int64 }
}

func productExists(ctx context.Context, id int64) bool { return id != 42 }

func order(ctx context.Context, in NewOrder) (bites.NoContent, error) {
	if !productExists(ctx, in.Body.ProductID) {
		return bites.NoContent{}, bites.Invalid("body.product_id", "names no product")
	}
	return bites.NoContent{}, nil
}
```

`Invalid` found anywhere in the returned error (wrapped with `%w`, or several joined with `errors.Join`) makes the answer this 400, from a handler, a Provider or a middleware alike.

Two answers are not 400, because the request's form is wrong rather than its content: a body whose `Content-Type` the route does not accept is a 415, and a request over a limit is a 413 or 431 ([Requests](./requests.md#reading-a-json-body), [Limits and timeouts](./limits.md)).

## Answering with a status

**Return a [`StatusError`](../reference/errors.md#statuserror) to answer with a status and a message of your choosing.** The constructors cover the common statuses: [`BadRequest`](../reference/errors.md#badrequest) (400), [`Unauthorized`](../reference/errors.md#unauthorized) (401), [`Forbidden`](../reference/errors.md#forbidden) (403), [`NotFound`](../reference/errors.md#notfound) (404), [`Conflict`](../reference/errors.md#conflict) (409), [`PreconditionFailed`](../reference/errors.md#preconditionfailed) (412), [`PreconditionRequired`](../reference/errors.md#preconditionrequired) (428), [`UnprocessableEntity`](../reference/errors.md#unprocessableentity) (422), [`TooManyRequests`](../reference/errors.md#toomanyrequests) (429) and [`ServiceUnavailable`](../reference/errors.md#serviceunavailable) (503). [`NewError`](../reference/errors.md#newerror) takes any status from 400 to 599, and its `title` is the status's registered reason phrase (`NewError(423, ...)` is `Locked`), or `Status N` for a status with none; any other status is answered as a 500 and logged, because it can only be a mistake. The message becomes `detail`.

<!-- compiles: decls -->
```go
type Pay struct {
	Path struct{ OrderID int64 }
}

var errPaid = errors.New("already paid")

func charge(ctx context.Context, id int64) error {
	if id == 7 {
		return errPaid
	}
	return nil
}

func pay(ctx context.Context, in Pay) (bites.NoContent, error) {
	switch err := charge(ctx, in.Path.OrderID); {
	case errors.Is(err, errPaid):
		return bites.NoContent{}, bites.Conflict("this order is already paid")
	case err != nil:
		return bites.NoContent{}, err
	}
	return bites.NoContent{}, nil
}
```

```http
POST /orders/7/pay HTTP/1.1

HTTP/1.1 409 Conflict
Content-Type: application/problem+json

{"title":"Conflict","status":409,"detail":"this order is already paid"}
```

The framework answers input mistakes with 400, never 422; [`UnprocessableEntity`](../reference/errors.md#unprocessableentity) is there for an application that wants it, and a client then has two codes for one kind of mistake.

## Adding members to the problem

**[`StatusError.With`](../reference/errors.md#statuserrorwith) returns a copy that carries one more problem+json member, such as a machine-readable code.** The members `title`, `status` and `detail` belong to the framework and are ignored ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)).

<!-- compiles: decls -->
```go
func payWithCode(ctx context.Context, in Pay) (bites.NoContent, error) {
	if err := charge(ctx, in.Path.OrderID); err != nil {
		return bites.NoContent{}, bites.Conflict("this order is already paid").
			With("code", "order_paid").
			With("retryable", false)
	}
	return bites.NoContent{}, nil
}
```

```http
HTTP/1.1 409 Conflict
Content-Type: application/problem+json

{"title":"Conflict","status":409,"detail":"this order is already paid","code":"order_paid","retryable":false}
```

## Wrapping errors and finding the status

**The framework finds a status error (or an error type that has an `HTTPStatus` method) anywhere in the chain, and the client sees that error's message, never the outer text.** So wrapping adds context for your log without leaking it ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)):

<!-- compiles: decls -->
```go
func loadOrder(ctx context.Context, in Pay) (bites.NoContent, error) {
	err := charge(ctx, in.Path.OrderID)
	if err != nil {
		// "load order 7" never reaches the client; "no such order" does, as a 404.
		return bites.NoContent{}, fmt.Errorf("load order %d: %w", in.Path.OrderID, bites.NotFound("no such order"))
	}
	return bites.NoContent{}, nil
}

func isNotFound(err error) bool {
	var se *bites.StatusError
	return errors.As(err, &se) && se.Status == 404
}
```

**Your own error types can say their status, so they need no conversion.** An error type with an `HTTPStatus() (int, string)` method is found by the same walk, and is answered with that status and that message. A service that returns `*NotFoundError` from forty places then needs no `nilohttp.Err(err)` at each handler ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)):

<!-- compiles: decls -->
```go
// NotFoundError is the repository's own error. It knows nothing about HTTP except what answer it is.
type NotFoundError struct{ What string }

func (e *NotFoundError) Error() string { return "repo: " + e.What + " not found" }

func (e *NotFoundError) HTTPStatus() (int, string) { return 404, e.What + " does not exist" }

func showDeal(ctx context.Context, in struct{}) (bites.NoContent, error) {
	// A 404 saying "deal does not exist"; "load deal" and "repo: deal not found" stay in the log.
	return bites.NoContent{}, fmt.Errorf("load deal: %w", &NotFoundError{What: "deal"})
}
```

If a chain holds both a `StatusError` and such a type, the nearer one decides. The message is all the client reads; a code or a header is added by converting in the handler with `NewError(...).With(...)`. An error without either is still a 500 with no message.

The framework maps no driver's errors, because that would mean importing the driver ([ADR 0006](../adr/0006-no-third-party-dependency-in-the-core.md)). Turning `pgx.ErrNoRows` or `sql.ErrNoRows` into a 404 is yours: one line in the handler, as `pay` does above, or once for the whole app in [`WithErrorMapper`](#mapping-errors-you-do-not-own). A `StatusError` is an error whose `Error()` is the message, or `404 Not Found` when the message is empty.

## Mapping errors you do not own

**A driver's error cannot have an `HTTPStatus` method, so the app says once what each one means with [`WithErrorMapper`](../reference/errors.md#witherrormapper).** The function gets the error as it was returned, wrapped or not, and returns the `StatusError` it stands for, or nil. Without it every handler converts, or every unconverted `pgx.ErrNoRows` is a 500 ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)):

```go
app := bites.New(bites.WithErrorMapper(func(err error) *bites.StatusError {
	var pg *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return bites.NotFound("no such record")
	case errors.As(err, &pg) && pg.Code == "23505": // unique_violation
		return bites.Conflict("already exists").With("code", "duplicate")
	case errors.As(err, &pg) && (pg.Code == "23503" || pg.Code == "23514"): // foreign_key, check
		return bites.UnprocessableEntity("the record breaks a rule")
	}
	return nil
}))
```

A handler then returns `fmt.Errorf("insert order: %w", err)` and the client gets the 409. The rules:

- **Your own typed answer wins.** The function is asked only when the chain holds no `StatusError` and no `HasStatus`, so a `NotFound(...)` you return is never second-guessed by the mapper.
- **It covers every error that is answered**: a handler's, a Provider's, a middleware's, a stream's. It is the app's, not a group's; a driver's error means the same on every route.
- **It is given more than once to add, not to replace**: the functions are asked in the order given and the first answer wins. A nil function panics in `New`.
- **A mapped 4xx is not logged; a mapped 5xx is**, with the original error, because the message you chose hides the reason. The message of a 5xx is sent as you wrote it, so never return `err.Error()` from a mapper.
- **A status outside 400 to 599, or a mapper that panics, is a 500 with no message**, logged with the original error.
- **A success pays nothing.** A mapped error costs what the same `StatusError` returned by the handler costs.

## An unknown error is a 500 with no detail

**An error with no status in its chain is a 500 with no message, and it is logged with the method and path.** What went wrong stays in your log, and the client learns nothing about your database ([ADR 0012](../adr/0012-client-errors-are-400-problem-json.md)):

```http
HTTP/1.1 500 Internal Server Error
Content-Type: application/problem+json

{"title":"Internal Server Error","status":500}
```

A handler that returns an error with a value discards the value; it is the error that is answered. When the context was cancelled because the client left, nothing is written and nothing is logged as an error. A handler cut off by `Timeout` is waited for, and the answer is then a 503 ([First-party middleware](./first-party-middleware.md), [ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)).

## Panics

**A panic in a handler, a Provider or a middleware is recovered for that request, and the client gets the same 500.** The engine does this and it cannot be turned off, because a process that dies on one bad request takes every other connection with it. The panic and its stack are logged with the request, and the connection is closed after the answer. If a streamed response has already begun, the connection is closed with no terminating chunk, since a half-written response cannot be repaired ([ADR 0028](../adr/0028-panics-are-recovered-by-the-engine.md)).

A panic at Registration is a different thing: it is a [`RegistrationError`](../reference/app.md#registrationerror), a refusal, and it is not recovered, because a program with a refused route is meant to stop ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)).

## The request ID

**Once a middleware gives the request an ID, every error answer carries it as the last member, `request_id`.** That includes the answers the framework writes before your handler: a 404, a 405, a 400 and the 500 after a panic. [`RequestID`](../reference/middleware.md#requestid) is the middleware that sets it and echoes it in a response header; [`Request.SetRequestID`](../reference/middleware.md#requestsetrequestid) is what a middleware of your own calls ([ADR 0022](../adr/0022-middleware-sees-a-request-view-and-returns-a-response-value.md), [First-party middleware](./first-party-middleware.md)).

```http
GET /search?page=0 HTTP/1.1

HTTP/1.1 400 Bad Request
Content-Type: application/problem+json
X-Request-ID: 4256382ac0011b6f

{"title":"Bad Request","status":400,"errors":[{"location":"query.page","message":"expected a positive integer","rule":"positive"}],"request_id":"4256382ac0011b6f"}
```

A refusal of the request head (431) is answered before any middleware has run, so it has no ID to carry. After a panic the `request_id` is in the body, and the response header is not set.

## Where to go next

- [Validation](./validation.md): located errors from your own rules.
- [Middleware](./middleware.md): returning an error answer from a middleware with `Problem`.
- [Testing](./testing.md): asserting on a problem+json body.
