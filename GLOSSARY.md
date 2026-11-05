# bites

A Go HTTP framework in which a handler's types are the contract: the route, the decoded input, the client errors and the API description all come from the types the handler already declares.

## Routes and handlers

**Typed handler**:
A function that takes a context and one Input and returns an output value and an error; it never sees the connection or the response writer.
_Avoid_: controller, endpoint function

**Input**:
The one struct a typed handler takes, made of Sections.
_Avoid_: request struct, params, DTO

**Section**:
A field of an Input named `Path`, `Query`, `Header`, `Cookie` or `Body`, whose name says which part of the request fills it.
_Avoid_: binding, source, tag

**CSV**:
`CSV[T]`, a list a client sends as one comma-separated value (`?stage=won,lost`), read in a Query or a Header; a plain slice is filled from repeated keys, and an element of a CSV cannot contain a comma.
_Avoid_: delimited list, split list

**Date**:
`Date`, a calendar date with no time of day and no zone, read and written as `YYYY-MM-DD` and described as `format: date`; it has no instant, so it has no `Time` method and `In(loc)` names the zone.
_Avoid_: civil date type, date-only time

**Registered text type**:
A type from another module that the app told the framework has a text form and an OpenAPI format, with `WithTextFormat[T](format)`; it decodes with its own `UnmarshalText` and says the format's sentence when a value does not.
_Avoid_: custom scalar, type adapter

**Registered JSON type**:
A type that writes or reads its own JSON, such as a database timestamp, whose shape the app declared with `WithJSONSchema[T](JSONSchema{...})` because the API document cannot read a method; without that, or `JSONSchema{Type: "any"}` for free-form JSON, a field of it is refused at Registration. `json.RawMessage` is free-form by name.
_Avoid_: custom JSON scalar, schema override

**Form**:
A Body type, `Form[T]`, read as `application/x-www-form-urlencoded` with the text decoding of the Query section: its fields are the form's fields, a list is a repeated key, and a mistake is located as `body.name`.
_Avoid_: form binding, form params

**Submitted**:
A Body type, `Submitted[T]`, read as a Form whose mistakes are handed to the handler instead of answered as a 400, so the handler shows the form again as a Page with each mistake beside its field and what was typed still in it.
_Avoid_: form result, bound form, form state

**Page**:
An answer type, `Page`, an HTML page for a browser sent with its own status, 200 or a 4xx; the one answer that carries an error status in place of a returned error.
_Avoid_: view, template response, HTML response

**Provider**:
A function registered for a type that fills Input fields of that type from the request, such as the authenticated user. It may need other provided types, which it receives as the fields of its dependency struct.
_Avoid_: extractor, injector, resolver

**Scope**:
Where `Use`, `Provide` and `Override` apply: the app, a group, a group nested in it, or one route, each enclosing the next. A scope has one Provider for a type; an Override replaces one from an enclosing scope.
_Avoid_: level, layer, container

**Override**:
A Provider registered with `Override` to replace, for its scope and those inside it, the Provider of the same type from an enclosing scope; a fake user in a test is the usual case.
_Avoid_: shadowing, rebinding

**Wire name**:
The name a client sends or receives for a field, derived from its Go name by the Naming policy or taken from a `json` tag.
_Avoid_: key, param name

**Naming policy**:
The one application-wide rule that turns Go field names into Wire names.
_Avoid_: case style, field mapping

**Validate**:
The `Validate() error` method an Input may have, run once after every Section has decoded without an error; its located errors (`Invalid`) are answered in the same 400 as decoding errors.
_Avoid_: validator, validation tag, `validate:"..."`

**Constraint type**:
A string or integer type (`NonEmpty`, `PositiveInt`, `PositiveInt64`) that decoding checks wherever its base type decodes and the API description states. An application's own named type becomes one by carrying a `Length`, `Range`, `ExclusiveMin`, `ExclusiveMax`, `MultipleOf`, `Items`, `Unique`, `Members` or `Pattern` method, checks itself with a `Check` method, and names its `format` in the API description with a `Format` method (ADR 0021).
_Avoid_: validation tag, refined type, newtype

**Member**:
One name and value of a JSON object, and what a `Members` method counts on a named map type (ADR 0021). A list has items and an object has members, as RFC 8259 says.
_Avoid_: property, entry, field (for a map's), key (for the whole pair)

**Error mapper**:
A function given to `WithErrorMapper` that turns an error of a type the application does not own (a driver's) into the `StatusError` it stands for. It is asked only when the error's chain has no status of its own (ADR 0049).
_Avoid_: error handler, error middleware, recoverer

**Status wrapper**:
A generic output type such as `Created[T]` that gives a success response a status other than 200.
_Avoid_: response object, result type

**Stream**:
A response whose body is written over time by a function the handler returns, after its status and headers are fixed. The function runs after the handler and every middleware have returned, and receives a StreamWriter that works only while it runs.
_Avoid_: chunked response, writer

**StreamWriter**:
The writer a Stream's function receives. It buffers up to 64 KiB, sends a chunk on `Flush`, when full and when the function returns, and fails every write after the function has returned.
_Avoid_: response writer, ResponseWriter

**SSE**:
A Stream that is live by construction, whose function writes Events (`EventWriter.Send`, `Comment`) and not bytes.
_Avoid_: event stream handler

**Events**:
An SSE whose events carry a value of one type `T`, written as JSON by an encoder compiled at Registration; its function sends values through an EventSender (`Send`, `SendEvent`, `Comment`), and the API document states `T` as the data of each event.
_Avoid_: typed SSE, JSON stream

**Going away**:
The Close 1001 that Shutdown sends every WebSocket, each after its own random share of a window (1 ms a socket, at most 3 s, at most half of Shutdown's context), so that the clients do not reconnect together; `OnClose` sees status 1001 with `Remote` false.
_Avoid_: shutdown close, kick

**Live stream**:
A Stream with no end of its own (every SSE and Events stream, and a Stream declared `Live`), whose context is cancelled with `ErrServerClosed` when Shutdown starts; a plain Stream is drained instead.
_Avoid_: infinite stream, long-poll

**Upload**:
A Body type whose handler reads a multipart/form-data request part by part while it arrives, never buffered whole. The route must state MaxBody. A Part, and the Upload it came from, work only while the handler runs.
_Avoid_: multipart body, file upload handler

**Part**:
One part of an Upload: a name, a file name and a content type, all data the client sent and never paths, and a body read with `Read`. It stops working when the Upload moves to the next part.
_Avoid_: file header

**Multipart**:
A Body type read as multipart/form-data and decoded into a struct before the handler runs: text fields as a Form decodes them, and file fields as FormFiles. The route must state MaxBody. Choose it when the handler wants the fields and files together as values, and an Upload when it needs the bytes while they arrive.
_Avoid_: multipart form, form upload, MultipartFormFiles

**FormFile**:
One file received by a Multipart body: its field name, the file name and content type the client sent (data, never a path), the bytes actually received, and an `Open` that returns a reader over this request's copy, in memory or in an unlinked temporary file. A FormFile works only while the handler runs; afterwards `Open` returns `ErrUploadClosed`.
_Avoid_: file header, multipart.FileHeader, upload file

**File**:
An output type: a file sent with its length, its validators (ETag, Last-Modified) and one Range from an `os.Root` or an `fs.FS`, so its name cannot name anything outside the directory; it answers its own conditional requests.
_Avoid_: static file handler, ServeFile

**Static**:
A directory of files served under a path prefix by one route, every file answered as a File is, with precompressed twins, a cache rule by name and an optional single-page-app fallback.
_Avoid_: file server, asset handler, static middleware

**Twin**:
A precompressed copy of a file beside it (`app.js.br`, `app.js.zst`, `app.js.gz`) that Static answers instead of the file when the client's Accept-Encoding weighs it highest.
_Avoid_: variant, sidecar, precompressed asset

**Fallback**:
The file Static answers for a browser navigation that names no file, so a single-page application's own router shows the page; never for a path whose first segment another route uses.
_Avoid_: catch-all, SPA mode, history fallback

**Registration**:
The moment a route and its typed handler are added to the router, when their types are checked against the route pattern.
_Avoid_: mounting, binding

**Refusal**:
A Registration mistake answered by a panic with an exact message naming the route, the field and where it was registered; each has a row in the refusal table (`refusal_test.go`) that holds its message like any other output ([ADR 0007](docs/adr/0007-mistakes-are-refused-at-registration.md)).
_Avoid_: validation error (that is a client's 400), startup check

**Problem+json**:
The shape of every error answer, `application/problem+json` with `title`, `status`, an optional `detail`, the request ID and, for a 400, `errors` ([ADR 0012](docs/adr/0012-client-errors-are-400-problem-json.md)).
_Avoid_: error envelope, error JSON

**Rule code**:
The optional `rule` member of an entry in a 400's `errors` (`type`, `required`, `length`, `range`, `items`, `members`, `unique`, `multiple`, `pattern`, `nonempty`, `positive`, `values`, `check`), with `min` and `max` for `length`, `range`, `items` and `members`: a short code a client translates instead of parsing the English `message` ([ADR 0012](docs/adr/0012-client-errors-are-400-problem-json.md)).
_Avoid_: error code, error key, validation tag

**Wire location**:
Where a client's mistake is, as the `location` of an entry in a 400's `errors`: the section and the wire names on the way to the value, such as `path.id`, `query.page` or `body.items[2].qty`.
_Avoid_: field path, JSON pointer

**Middleware**:
A function that wraps the rest of the chain: it sees a Request view, calls `next.Call(ctx)` or does not, and returns a Response value. App middleware runs before routing; group and route middleware after it.
_Avoid_: filter, interceptor, handler wrapper, hook

**RouteInfo**:
The copy of a route's identity (method, pattern with its groups' prefixes, operation ID, tags, whether it is a WebSocket or a Static route, and which built-in route it is, if one) that Registration gives the function of `UseEach`, which returns the Middleware for that route, none, or a refusal. It is how a middleware learns its route, since app middleware runs before the match ([ADR 0048](docs/adr/0048-groups-and-the-order-things-run-in.md)).
_Avoid_: route object, route handle

**Request view**:
The read-only `Request` a middleware is given: the method, path, query, headers and remote address, copied once into the request's slab and never the engine's buffer.
_Avoid_: context, request object

**Response value**:
The small `Response` a middleware sees and returns: a status, a content type, the headers middleware added and the size of a body that is already encoded; the body bytes are not part of it.
_Avoid_: writer, response object

**Request ID**:
The id a middleware gives a request with `SetRequestID`; every problem+json answer then carries it as `request_id`.
_Avoid_: correlation id, trace id

**First-party middleware**:
The first-party pieces of [ADR 0032](docs/adr/0032-four-first-party-middleware.md): the middleware `CORS`, `RequestID` and `Timeout`, and the `AccessLog` Observer, made by constructors that refuse a configuration that cannot work.
_Avoid_: plugin, built-in filter

**Preflight**:
The `OPTIONS` request with `Origin` and `Access-Control-Request-Method` a browser sends before a cross-origin request; `CORS` answers it with a 204 before routing.
_Avoid_: CORS request, options request

**Access log**:
The one slog record, `request`, that the `AccessLog` Observer writes for each request when it has really ended, with the status and size that were sent and the attributes Providers added.
_Avoid_: request log, audit log

**Observer**:
What `WithObserver` takes ([ADR 0051](docs/adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)): told when a request starts, before app middleware, and when it has really ended, after the last byte or after the connection was given up, with an `Outcome`. It watches and never answers; metrics, tracing and the access log are built on it.
_Avoid_: hook, interceptor, listener, middleware (for this)

**Outcome**:
What an Observer's End is told: the route pattern, the status sent, the body bytes read and written, the duration, and whether the request streamed, upgraded, panicked or was abandoned.
_Avoid_: result, response info, stats

**Trace context**:
The W3C `traceparent` and `tracestate` a request carries; with `WithTraceContext` the app reads them, gives the request a span id of its own, and `TraceFrom(ctx)` returns the `Trace`. The core records no span ([ADR 0059](docs/adr/0059-the-core-carries-w3c-trace-context-and-records-no-span.md)).
_Avoid_: correlation id (for this), span context, trace header

**Server span**:
The OpenTelemetry span `modules/otel` records for one request from Observer Start to End, the child of the caller's span, and named to the core with `Request.SetTrace` ([ADR 0063](docs/adr/0063-opentelemetry-is-a-module-whose-observer-records-the-server-span-and-names-it-to-the-core.md)).
_Avoid_: request span, root span (it is a root only when no caller sent a trace)

**SocketObserver**:
What an Observer also implements to be told of the app's WebSockets ([ADR 0051](docs/adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)): a socket opened, closed with its cause, and a pong missed, by route. `Metrics` is one.
_Avoid_: socket hook, WebSocket listener

**Close cause**:
Why a WebSocket ended, one of eight fixed values (`CloseCause`: client, server, handler error, queue full, pong timeout, shutdown, client fault, lost). It is not the close status, which is the wire's word.
_Avoid_: close reason (that is the text sent with the status), close code

**App origins**:
The list `WithAllowOrigins` gives, the origins of the app's own pages: the default of its WebSocket routes and of a `CSRF` with no `Origins`, which a route's or a group's own socket list may only narrow ([ADR 0057](docs/adr/0057-csrf-trusts-the-browsers-origin-and-falls-back-to-a-double-submit-token.md)). CORS's origins are not taken from it.
_Avoid_: allow list, whitelist, trusted origins

**Metrics**:
The Observer `NewMetrics` returns, which counts requests by method, route pattern and status code and is served in the Prometheus text format by `ServeMetrics` ([ADR 0060](docs/adr/0060-metrics-are-an-observer-in-the-prometheus-text-format-labelled-by-what-the-app-bounds.md)).
_Avoid_: stats, telemetry (for this), monitoring

**Series**:
One label set (method, route, code) of `Metrics`, with its histogram and counters; how many there are is bounded by the app's routes, never by a client, and capped by `MaxSeries`.
_Avoid_: metric (for one label set), time series key

**Gzip**:
The response compression of [ADR 0031](docs/adr/0031-gzip-only-and-off-by-default.md): `WithGzip(Gzip{MinSize})` turns it on, and the engine compresses a finished body of at least `MinSize` bytes (1 KiB at least) for a client whose `Accept-Encoding` accepts gzip, with a pooled writer and a pooled scratch buffer. A middleware sees the uncompressed Response value.
_Avoid_: compression middleware, deflate, encoder

**Vary: Accept-Encoding**:
The header every answer carries once Gzip is on, compressed or not, so a cache keeps the two representations apart.
_Avoid_: cache key header

**Undocumented route**:
A route served as any other but left out of the API document by `Undocumented()`, on the route or its group ([ADR 0069](docs/adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)); mounted handlers and the docs UI page are undocumented routes. It hides the route from the document, not from a client.
_Avoid_: hidden route, private route (it is not access control)

**API document**:
The OpenAPI 3.1 document of the app's typed routes, built once from the route records when the app starts serving and served as fixed bytes at the path `OpenAPI` was given. WebSocket routes are not in it.
_Avoid_: swagger file, spec, API schema

**Route record**:
What Registration keeps of a typed route for the API document: its operation, built from the same plans and types the route was compiled from, so a mistake in what it describes is refused at the line that registered the route.
_Avoid_: route metadata, route table entry

**operationId**:
The name of an operation in the API document, which a client generator turns into the name of the method that calls it: made from the method and path (`getUsersById`) unless `OperationID` gives one, and unique as a generator compares names ([ADR 0020](docs/adr/0020-descriptions-come-from-registration-and-later-from-doc-comments.md)).
_Avoid_: route name, handler name, operation name

**Component name**:
The name of a schema under `components.schemas` in the API document, which a client generator turns into the name of a type: the Go type's name, qualified by package only when two types share it, with `.Request`, `.RequestOpen` or `.Response` after it when the schemas of one type differ by mode, or the name `Info.SchemaName` gives the type ([ADR 0020](docs/adr/0020-descriptions-come-from-registration-and-later-from-doc-comments.md)).
_Avoid_: model name, schema id, definition name

**Descriptions**:
The optional `Descriptions() map[string]string` method of a struct type, giving its fields' prose by Go field name; Registration checks each name against the type's fields so a description cannot outlive its field.
_Avoid_: doc tag, field comment

**Example**:
The optional `Example() T` method of a named type, giving a value of the type for the API document; Registration writes it with the codec and reads it back through the type's own rules, so an example the type refuses is a panic, and the document states it as `examples` in the type's schema.
_Avoid_: sample, example tag, example JSON

**DeprecatedFields**:
The optional `DeprecatedFields() []string` method of a struct type, naming its deprecated fields by Go field name; a field of a section is a deprecated parameter, and Registration checks each name against the type's fields as it does for Descriptions.
_Avoid_: deprecated tag, deprecated marker

**Defaults**:
The optional `Defaults() map[string]string` method of a text Section's struct, saying by Go field name what an omitted field means, as text a client would send; the field stays a plain type, is no longer required, and the API document states `default` ([ADR 0041](docs/adr/0041-presence-is-a-type.md)).
_Avoid_: default tag, `Default()` on a type, `Or(50)` in the handler for a documented default

**OneOf**:
The return type of a handler with more than one answer, `OneOf2[A, B]` to `OneOf4[A, B, C, D]`: it holds one of its type arguments, chosen with the method of that variant's letter (`Answer{}.B(v)`), each compiled at Registration as if the handler returned it alone ([ADR 0013](docs/adr/0013-other-statuses-are-wrapper-types.md)).
_Avoid_: Either, union (a JSON value of several kinds is a Tagged union), variant type (a variant is one of its type arguments)

**Tagged union**:
A struct whose `Discriminator() string` method names the member that carries its tag, and whose every field is an `Opt` of a struct, one **variant**, tagged by the field's wire name: a JSON value of several kinds, such as a WebSocket protocol's frames, with exactly one variant set ([ADR 0083](docs/adr/0083-a-json-value-of-several-kinds-is-a-tagged-union-of-opt-variants.md)).
_Avoid_: OneOf (a handler's answers), polymorphic body, a struct of a `type` enum and optional fields

**Server-only variant**:
A variant of a Tagged union that its `ServerOnly() []string` method names by Go field name: carried between servers (a Topic's reference crosses its broker as one) and never described to, sent to or read from a client, so the API document leaves it out and `Socket.Send` refuses it ([ADR 0083](docs/adr/0083-a-json-value-of-several-kinds-is-a-tagged-union-of-opt-variants.md)).
_Avoid_: internal variant, hidden variant, reference variant (it is what a Topic's reference is, not what the variant is)

**Returns**:
The route option, `Returns[T]()`, that declares one type a handler returning an interface may hold; the route's statuses, its schemas and its plans come from the list. It is the fallback for a handler that a OneOf cannot describe.
_Avoid_: Produces, response map

**Slow path**:
A route served by a plain `http.Handler` through `stdhttp.Mount`, whose request builds `net/http` objects and so is outside the allocation budget of ADR 0003.
_Avoid_: compatibility mode, adapter, legacy handler

**Mount**:
Serving a plain `http.Handler` on a route with `stdhttp.Mount`, one typed route a method, as the slow path.
_Avoid_: bridge, adapter, wrap

**Module**:
A feature with a third-party dependency, or one most applications do not need, kept in its own Go module under `modules/` so the core never imports it ([ADR 0070](docs/adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)).
_Avoid_: plugin, extension, contrib

**Inbox**:
The value a WebSocket route returns on upgrade, holding that socket's state and the functions called with its messages and its close.
_Avoid_: session (Session means a cookie session, from milestone 3), handlers, connection, client

**Reassembly budget**:
The app-wide cap on the memory held for WebSocket messages that did not arrive whole; a socket that would exceed it is closed with 1013.
_Avoid_: buffer pool, message cache

**Flusher**:
The goroutine that writes a WebSocket's queued frames when a Send could not write them inline; it starts when the queue is non-empty and exits when it is empty, so an idle socket has none.
_Avoid_: writer goroutine, write loop

**Send budget**:
The app-wide cap on the bytes queued to be sent on all WebSockets together; a socket that would exceed it is closed with 1013. The per-socket cap (`SendQueue`) closes with 1008.
_Avoid_: output buffer, outbound limit

**Close error**:
A `*CloseError{Status, Reason}` returned from `OnMessage` or `OnMalformed`, wrapped or not, which closes the socket with that status instead of 1011 ([ADR 0047](docs/adr/0047-a-socket-is-a-handle-and-a-topic-is-a-broadcast.md)).
_Avoid_: WSError, close code error

**Socket**:
The typed handle a WebSocket route is given for sending to one client; it is never reused for another connection.
_Avoid_: conn, peer, client

**Owner**:
The string that names whose a Session or a Socket is, usually the User's id: a session type states it with a `SessionOwner` method and a store that implements `OwnerSessionStore` finds every session of one, and a `SocketOwners` closes every Socket of one, on every instance when it is made with `NewSocketOwners` ([ADR 0056](docs/adr/0056-a-session-is-sealed-in-a-cookie-and-changed-by-returning-its-cookie.md), [ADR 0047](docs/adr/0047-a-socket-is-a-handle-and-a-topic-is-a-broadcast.md)).
_Avoid_: principal, subject, user key

**Service**:
Something that lives as long as the app: a value with a name and up to three functions (`Start`, `BeforeShutdown`, `Stop`), registered with `app.Service`, started in registration order after the listener is bound and stopped in reverse after the drain.
_Avoid_: component, lifecycle hook, plugin, daemon

**Worker**:
A Service that runs one function on its own goroutine for the life of the app, made with `bites.Worker`; `bites.Every` makes the one that calls a function once per interval on the app's clock. Neither is restarted when it fails.
_Avoid_: daemon, background job, cron

**Topic**:
A set of Sockets that one published message reaches, framed once. Made with `NewTopic` and a Broker it reaches the Sockets of every instance of the application.
_Avoid_: hub, room, channel, broadcaster

**Topics**:
A family of Topics named by a key known only at run time (a room per id, the frames of one User): each key is a Topic made when its first Socket subscribes and dropped when its last leaves, and `Publish(key, v)` reaches the key's Sockets on every instance even when this one has none ([ADR 0084](docs/adr/0084-a-name-made-from-data-is-a-key-of-a-topics-family.md)). A name the code knows is one Topic.
_Avoid_: topic map, room registry, topic per user

**Broker**:
What carries a Topic's messages between the instances of an application ([ADR 0081](docs/adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md)): an interface of the core that a module (`pgnotify` for Postgres `LISTEN`/`NOTIFY`) or `bitetest.Broker` implements. It delivers a message to every instance, the publisher's included.
_Avoid_: bus, pubsub, message queue, hub

**Conformance suite**:
A function of `bitetest` (`TestRateLimitStore`, `TestSessionStore`, `TestOwnerSessionStore`, `TestBroker`) that a store's or a Broker's own test calls to hold it to the promises of its interface's doc comment, one subtest each ([ADR 0086](docs/adr/0086-a-store-is-held-to-its-interface-by-a-conformance-suite-in-bitetest.md)).
_Avoid_: contract test, compliance test, TCK

**Gap**:
The signal that a Broker may have lost messages (a `LISTEN` connection that dropped and came back); a Topic's `OnGap` receives it and the application tells its Sockets to read their state again.
_Avoid_: missed event, resync (that is what the application sends in answer)

## Configuration

**Setting**:
One value an application reads at start-up from outside its code (a listen address, a pool size, a key): a field of the struct given to `config.Load`, with a canonical key such as `db.pool_max` that an origin finds it by ([ADR 0080](docs/adr/0080-configuration-is-a-module-that-fills-a-typed-struct-from-flags-environment-and-files.md)).
_Avoid_: option (for a setting; an Option is what is given to `Load`), parameter, env var (for the setting itself)

**Origin**:
Where a setting is read from: a flag, the environment, a file or its default, tried in that order, the first that has it winning. The log and the error name the origin of each value.
_Avoid_: source (a Section is already the word for where a value of a request comes from), provider (a Provider is another thing)

**Secret**:
A setting of type `config.Secret`, whose text prints as `[redacted]` everywhere and is reached only by `Reveal()`. It has no flag, and may be read from a file named by the variable with the suffix `_FILE`.
_Avoid_: credential (for the type), sensitive string, password

## Documentation

**Documentation layer**:
One of the three layers of the documentation, the guide, the reference and the design pages ([ADR 0050](docs/adr/0050-the-documentation-is-three-layers-held-by-tests.md)): a guide page says how to do a task, a reference page has a heading for every exported name of its area, and a design page joins the ADRs of one topic. `docs/README.md` is the map that joins them.
_Avoid_: manual, API docs (for the reference alone), architecture doc

## Engine and workload

**Engine**:
The part that accepts connections, reads requests and writes responses; everything below the router.
_Avoid_: server, transport

**Parked connection**:
A connection idle for 100 ms that has no goroutine: it is registered with the engine's poller for one event and its goroutine has ended. Bytes, a hangup, its idle timeout on the timer wheel, Shutdown or the close of a WebSocket wake it, which starts one goroutine for it again ([ADR 0005](docs/adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md)).
_Avoid_: sleeping connection, event-loop connection, detached connection

**Wake**:
What gives a parked connection a goroutine again, exactly once however many causes arrive together; the woken goroutine reads what woke it at once.
_Avoid_: resume (in prose), unpark, notify

**Poller**:
The engine's one epoll instance and goroutine where idle connections park, beside the disconnect watcher of [ADR 0016](docs/adr/0016-the-handler-context-is-cancelled-on-disconnect.md).
_Avoid_: event loop, reactor, netpoller (the Go runtime's own)

**Verification function**:
The application's function an auth Provider calls with the credential it parsed; it returns the value the credential stands for, or an error wrapping `ErrInvalidCredentials` for a credential that is not good ([ADR 0054](docs/adr/0054-auth-providers-parse-the-credential-and-the-application-verifies-it.md)).
_Avoid_: authenticator, validator, strategy

**Challenge**:
The `WWW-Authenticate` value of a 401, naming the scheme and realm a client must answer with.
_Avoid_: auth header, prompt

**Session**:
A value of the application's type kept for a browser across requests, sealed in a cookie or held in a store, read as `Session[T]` through a Provider and changed by returning the cookie `Save` gives ([ADR 0056](docs/adr/0056-a-session-is-sealed-in-a-cookie-and-changed-by-returning-its-cookie.md)).
_Avoid_: session for a WebSocket's value (that is `Inbox`), session bag, flash

**Verifier**:
The value of `modules/jwt` whose `Verify` method checks a token's algorithm, signature and claims and decodes the claims into the application's type; it is the verification function given to `BearerAuth` ([ADR 0062](docs/adr/0062-jwt-verification-is-a-module-whose-key-fixes-the-algorithm.md)).
_Avoid_: token parser, validator (a token that fails is an invalid credential, not invalid input)

**Key source**:
What finds the key a token names by `kid`: a fixed set (`jwt.Keys`) or a fetched JWKS (`jwt.JWKS`). Its failure to look is a 500, never a 401 ([ADR 0062](docs/adr/0062-jwt-verification-is-a-module-whose-key-fixes-the-algorithm.md)).
_Avoid_: keyring, keystore

**Security scheme**:
The kind of credential the API document says a route needs (`bearerAuth`, `basicAuth`, `apiKeyAuth`, `cookieAuth.sid`, `mutualTLS`), read at Registration from the auth Providers the route runs; a Provider the application wrote states its own with a `SecurityScheme` given to `CustomAuth` ([ADR 0069](docs/adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)).
_Avoid_: auth type, security definition (OpenAPI 2's word)

**Auth Provider**:
A Provider the framework knows reads a credential, because `BearerAuth`, `BasicAuth`, `APIKeyAuth`, `CustomAuth` or `AnyAuth` made it: the API document states its scheme, `AnyAuth` can offer it among others, and an `Opt` field can make it optional ([ADR 0069](docs/adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)).
_Avoid_: authenticator, auth middleware, guard

**Optional credential**:
An Input field or dependency `Opt[T]` whose `T` has an auth Provider: the Provider runs only when the request sends its credential, the field is absent otherwise, and a credential that is sent and not good is still a 401. The API document adds the empty requirement `{}` ([ADR 0069](docs/adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)).
_Avoid_: anonymous auth, soft auth, guest mode

**Rate limiter**:
`RateLimiter`: a token bucket per key (the client address by default, or a user's id through `Allow` in a Provider), held in a bounded in-memory store or a shared one, which for Postgres is the module `pgratelimit` ([ADR 0055](docs/adr/0055-rate-limiting-is-a-token-bucket-per-key-with-a-bounded-store.md), [ADR 0085](docs/adr/0085-a-postgres-rate-limit-store-is-a-module.md)).
_Avoid_: throttle, quota manager

**Trusted proxy**:
A load balancer or proxy whose network the app lists with `WithTrustedProxies`, by CIDR; only a connection from one may name the client's address or scheme in a forwarding header, and the public host and prefix when the app says its proxies write them ([ADR 0025](docs/adr/0025-no-proxy-is-trusted-by-default.md)). No proxy is trusted by default.
_Avoid_: real IP middleware, proxy headers, proxy depth, trusted hops

**Public host**:
`Request.PublicHost`: the host, with its port, that the client asked for: the `X-Forwarded-Host` a trusted proxy wrote when the app says its proxies write it (`ForwardedHost`), the `Host` header otherwise. The `Host` header itself is what this hop was sent. With the public prefix (`Request.PublicPrefix`, the path a proxy strips, from `X-Forwarded-Prefix` under `ForwardedPrefix`) and the scheme it makes `Request.PublicURL` ([ADR 0025](docs/adr/0025-no-proxy-is-trusted-by-default.md)).
_Avoid_: external host, real host, base path (for the prefix)

**Client address**:
`Request.ClientAddr`: the peer of the connection or, when the peer is a trusted proxy, the rightmost address of `X-Forwarded-For` that is not a trusted proxy. The peer itself is the remote address.
_Avoid_: real IP, remote IP, client IP (in prose)

**Readiness**:
Whether the app wants traffic: 200 on the Ready path while it serves, 503 from the moment Shutdown starts or while `SetReady(false)` holds ([ADR 0052](docs/adr/0052-health-and-readiness-follow-shutdown.md)). It checks no dependency. Liveness is the other path, 200 for as long as the app serves.
_Avoid_: health check (for readiness alone), up, status endpoint

**Certs**:
The server certificate in force behind a `tls.Config`'s GetCertificate, replaced while serving by Reload or Set; a failed reload changes nothing.
_Avoid_: cert store, keypair reloader, hot reload

**Shutdown delay**:
What `WithShutdownDelay(d)` adds before `Shutdown` stops accepting: readiness fails and the app keeps serving for `d`, bounded by half of what is left of Shutdown's context, so a balancer stops sending first ([ADR 0052](docs/adr/0052-health-and-readiness-follow-shutdown.md)).
_Avoid_: grace period (that is Shutdown's context), pre-stop sleep, drain delay

**Reference workload**:
The application shape every design is judged against first: a JSON API over Postgres, with long-lived WebSockets.
_Avoid_: benchmark, use case
