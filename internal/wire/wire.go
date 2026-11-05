// Package wire holds the foundations that the JSON codec, the text decoders and the public
// package share: the presence types Opt and Null (ADR 0041), the naming rules that turn Go
// names into wire names (ADR 0011), the per-request text Slab (ADR 0008), the allocation-free
// query iterator and the per-type text decoders (ADR 0042), whose errors are the client-facing
// messages of ADR 0012.
//
// Everything here is either run once at Registration (naming, CompileText) or built to cost
// no allocation per request (Slab, EachQuery, the compiled TextDecoders). The public package
// re-exports Opt, Null and their constructors through generic type aliases, so the types are
// designed as the public API.
//
// The package imports the standard library only (ADR 0006).
package wire
