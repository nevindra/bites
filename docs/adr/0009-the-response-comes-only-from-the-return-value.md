# A typed handler answers only by returning, and never holds the writer

A typed handler is `func(ctx context.Context, in In) (Out, error)` and has no `http.ResponseWriter`. Status, headers and body all come from the returned value or error, and the framework writes the response once. Streaming is a return type of its own, not a writer handed to the handler.

This removes, by construction, failures that were shown by probes on 2026-10-30 ([`docs/research/`](../research/README.md)): a second `c.JSON` producing `{"a":1}{"b":2}` (Gin, Echo); a header set after the body silently dropped (Gin, Echo, net/http); a status change after the body ignored with a log line only in debug mode (Gin); `http.Error` without `return` sending both bodies (net/http); a bind helper writing 400 and the handler then writing a 500-shaped body under it (Gin).

**Considered:** a context object with write helpers, as Gin, Echo and Fiber have. Rejected because every one of them allows the second write. The cost is that a handler which wants to write as it goes must use the streaming return type.
