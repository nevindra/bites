// Package bites is an HTTP framework in which a handler's types are the contract (ADR 0010): a
// plain function taking one Input struct and returning one value and an error is a route, and its
// types produce the routing, the decoding, a 400 that names the field a client got wrong, the
// response and the OpenAPI document.
//
//	type GetUser struct {
//		Path  struct{ ID int64 }
//		Query struct{ Expand bites.Opt[bool] }
//	}
//
//	app := bites.New()
//	app.Get("/users/{id}", users.Get)
//	log.Fatal(app.Listen(":8080"))
//
// # Registration
//
// Get, Post and the other route methods are generic methods (Go 1.27) so that the Input and
// Output types are known to the compiler at the call and read once, by reflection, when the
// route is added (ADR 0007). Everything that does not fit panics there, with the route, the
// field and the file and line of the registration (RegistrationError), and nothing is left for
// the first request to find. Per request there is no reflection on the hot path: the plans are
// walked with offsets, and a typed JSON request costs two allocations (ADR 0003).
//
// # What a route can take and return
//
// An Input has the Sections Path, Query, Header, Cookie and Body, and fields of any other name
// are filled by a Provider (Provide, Override). Presence is a type (Opt, Null); a few constraint
// types (NonEmpty, PositiveInt), the Length, Range, Items, Unique, Members, MultipleOf, Values and Check methods of an
// application's own named types, and a Validate method cover what types cannot, and a Body
// may be JSON, Raw, bytes, Form or Upload. A handler answers by what it returns: a value, a status
// wrapper such as Created, Text, Bytes, File, Stream or SSE, or a OneOf2 to OneOf4 of them for a
// handler with more than one answer (Returns declares the types of an interface return, the
// fallback). Every error answer is application/problem+json.
//
// # Around the handler
//
// Middleware, Use and the first-party middleware (CORS, RequestID, Timeout) wrap the chain,
// Observers (WithObserver: AccessLog, NewMetrics, your own) watch each request from its start to
// its real end, groups share a prefix and options, and WebSocket registers a route whose Socket,
// Inbox and Topic carry typed messages. App.OpenAPI serves the API document, WithLimits and the
// route options bound what a client may send, App.ListenTLS serves TLS, and App.Shutdown drains
// connections. The engine, the router and the JSON codec are in this repository and the core
// imports the standard library only. Package bitetest runs an App over in-memory connections and
// a fake Clock, with no port.
//
// # More
//
// The guide (docs/guide), the reference (docs/reference) and the design pages (docs/design) are
// written in the repository and held true by its tests, as docs/README.md maps them; the
// decisions are in docs/adr and what is not built yet is in docs/roadmap.md.
package bites
