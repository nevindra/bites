# Errors

**How a failed request is answered: one problem+json shape for every error, an input mistake that is always a 400 listing each field, and a status found in the error chain.**

**Guide:** [errors](../guide/errors.md) · **Reference:** [errors](../reference/errors.md)

## Overview

Every error answer the framework writes is `application/problem+json` with wire names. There are three sources and each has its rule: the client's input does not fit the route (a 400 that names each field), the handler returns an error (its own status, or a 500 with no message), and the framework refuses the request itself (404, 405, 413, 415, 431, 500, 503).

```text
input mistake            400, errors[] with location (path.id, body.items[2].qty) and message, at most 20
route / limit / media    404, 405 + Allow, 413, 415, 431        same problem+json shape
handler returns error    the nearest StatusError or HasStatus in the chain -> its status and its own message
                         no status in the chain -> the app's WithErrorMapper functions, in order, for a foreign error type
                         still none -> 500, no message, logged
client left              nothing written, logged as abandoned
Timeout middleware       waits for the handler, then 503
request id               "request_id" as the last member, when a middleware set one
```

The shape of the body is taken from Huma. The reason for one shape is that, for the same mistake, other frameworks answer 400, 404, 422 or 500, name a Go field or none, or put the error's text in the body.

## Rules

1. **Every input mistake is a 400 with its errors listed, each with its location in wire names a message that names what was expected and, for the mistakes of a type, a `rule` code (`pattern` among them; with `min` and `max` for `length`, `range` and `items`) so a client can translate it without parsing English.** Decoding does not stop at the first error, and only a syntax error in a JSON body ends it. At most 20 errors are listed, with `"truncated": true` when there were more, and a message never echoes more than 64 bytes of the client's value. [ADR 0012](../adr/0012-client-errors-are-400-problem-json.md)

2. **Every error the framework writes is problem+json, and carries the request ID as the last member when a middleware set one.** The one answer with a 4xx that is not problem+json is a `Page` the handler returns, HTML for a browser, and a `Submitted[T]` form hands its mistakes to that handler instead of the 400 [0078](../adr/0078-a-page-for-a-browser-is-a-page-and-a-form-it-shows-again-is-a-submitted-body.md). 404, 405 (with `Allow`), 413, 415, 431, 500 and 503 share the shape, including the 500 the engine writes after a panic in a middleware. A head the engine refuses before any middleware ran has no ID to carry. [ADR 0012](../adr/0012-client-errors-are-400-problem-json.md)
3. **Only the form of a request has another status than 400.** A body whose `Content-Type` the route does not accept is a 415, and a request over a limit is a 413 or 431. 422 for input that parses but fails validation was rejected, because a client would have two codes for one kind of mistake and the distinction is the server's. [ADR 0012](../adr/0012-client-errors-are-400-problem-json.md)

4. **A located error (`Invalid`) anywhere in what a handler, a Provider or a middleware returned is the 400 that lists it, as from `Validate`; a plain `error` from a handler is a 500 with no message in the body, and is logged with the request.** An error that carries a status is answered with that status and its message. An `Invalid` from a handler was a 500 until 2026-11-04, a trap an agent met in the usability test. [ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md) [ADR 0012](../adr/0012-client-errors-are-400-problem-json.md)
5. **The status is found through the whole chain (a `*StatusError`, or an application error type with an `HTTPStatus() (int, string)` method, the nearer one winning), and the body carries that error's own message, never the wrapping text.** `fmt.Errorf("load user: %w", bites.NotFound("no such user"))` is a 404 saying `no such user`. A status error may carry extra members such as a machine-readable `code`. The framework maps no driver's errors, since that would import the driver; an application's own error type says its status with a method instead, and a type it does not own is mapped by a function it gives `WithErrorMapper`, asked only when the chain has no status. An empty answer is a 204, never a 200 with no body ([ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md)). [ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)

6. **When the context was cancelled because the client left, nothing is written and nothing is logged as an error.** The access log records the request as abandoned. The engine keeps it too: no 500 of its own when the handler set no answer or panicked, and the connection closes. [ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)
7. **A timed-out handler is waited for, never abandoned.** The timeout cancels the request's own context with `context.DeadlineExceeded` as its cause, waits for the handler to return and answers 503 whatever the handler returned; a handler that ran more than twice its deadline is logged at Warn. Answering while the handler still runs would mean a second goroutine touching the request and a response written while the handler might still act. [ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)

## Decisions

| ADR | What it decides |
|---|---|
| [0012](../adr/0012-client-errors-are-400-problem-json.md) | input mistakes are a 400 listing each field, and every error answer is problem+json |
| [0078](../adr/0078-a-page-for-a-browser-is-a-page-and-a-form-it-shows-again-is-a-submitted-body.md) | a `Page` is HTML for a browser with 200 or a 4xx, and a `Submitted[T]` form hands its mistakes to the handler that shows it again |
| [0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md) | a status is found with `errors.As`, a left client is not an error and a timed-out handler is waited for |

## Open questions

- **A handler that ignores its context is late, visibly, rather than leaked**, and holds its connection until it returns ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)).
- **A `Stream` returned in time is not under the timeout's deadline** ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md), [responses](responses.md)).
- **The `Validate` location is a string nothing checks**, so a renamed field leaves a stale location in the 400 ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md), [request input](request-input.md)).
- **A client generated from the description that treats 400 as the only input code** is the price of refusing 422 ([ADR 0012](../adr/0012-client-errors-are-400-problem-json.md)).
