# Typed handlers

**What a handler is, how its types become the contract, why a mistake in them is refused at Registration, and why it answers only by returning.**

**Guide:** [handlers](../guide/handlers.md) · **Reference:** [app](../reference/app.md)

## Overview

A typed handler is a function that takes a context and one Input and returns an output value and an error. It never sees the connection or a response writer. Its Input is one struct whose Sections are named `Path`, `Query`, `Header`, `Cookie` and `Body`, and the section a field sits in says where its value comes from, so no tag is written.

```text
app.Get("/users/{id}", users.Get)          users is a struct the application owns
         |
         v   Registration reads the handler's types once, by reflection
   route pattern  ---- checked against ---->  Input sections and fields
         |                                     (a mismatch panics here, with route, field, file and line)
         v
   plans: decoder, encoder, error locations, API description
         |
   per request: walk the plans with offsets, call the handler, write the return value once
```

Get, Post and the other route methods are generic methods (Go 1.27), so the compiler knows the Input and output types at the call. Everything that does not fit panics at that call, in the way `http.ServeMux` panics on two conflicting patterns, and one test that builds the application's router turns every such mistake into a failing test. What the check does not give is a compile-time guarantee: an application with no test that builds its router finds out when it starts.

## Rules

1. **A mistake in a handler's types is refused when the route is registered.** Registration reads the Input and output types once, checks them against the route pattern and builds the decoder and encoder; a `{id}` with no field for it, a field the pattern lacks, or a type that cannot be decoded from text or encoded to JSON panics with the route, the field and the file and line of the registration (`RegistrationError`). Every mistake has a row in the refusal table, and the checks live in one place so a code generator could call them later. [ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)

2. **A typed handler answers only by returning.** It is `func(ctx context.Context, in In) (Out, error)` with no writer: status, headers and body come from the returned value or error and the framework writes the response once, so a second write, a header after the body and a double body cannot be written. A handler that wants to write as it goes returns a `Stream`. [ADR 0009](../adr/0009-the-response-comes-only-from-the-return-value.md)
3. **A route's input is exactly one struct, whose fields named `Path`, `Query`, `Header`, `Cookie` and `Body` are its Sections; a route with no input takes `struct{}`.** Fields of any other name are filled by a Provider or refused at Registration. A `Body` section on a GET, HEAD or DELETE route is refused, `Opt` and lists are refused in `Path`, and a section has at most 64 fields and 8 lists. A single struct was chosen over one argument a part because Go has no variadic generics, and over tags because naming the section removes them. [ADR 0010](../adr/0010-one-input-struct-with-sections-by-field-name.md)

4. **In the text sections an unknown query parameter is ignored and a non-list field sent twice is a 400.** A URL carries tracking and cache-busting parameters the client does not control, but an ambiguous request is refused. [ADR 0010](../adr/0010-one-input-struct-with-sections-by-field-name.md)
5. **A handler reaches Postgres and other services through the struct it is a method of.** The application builds and owns the struct and registers its method value; the framework has no container, no injection, no registry of services and no global state for them. Values that depend on the request, such as the authenticated user, are Providers, a separate decision. [ADR 0014](../adr/0014-services-are-plain-go-values.md)

## Decisions

| ADR | What it decides |
|---|---|
| [0007](../adr/0007-mistakes-are-refused-at-registration.md) | a mismatch between a handler's types and its route is a panic at Registration with route, field and registration site |
| [0009](../adr/0009-the-response-comes-only-from-the-return-value.md) | a typed handler holds no writer; the response is the return value, written once |
| [0010](../adr/0010-one-input-struct-with-sections-by-field-name.md) | one Input struct whose Sections are named by field |
| [0014](../adr/0014-services-are-plain-go-values.md) | services are fields of the struct the handler is a method of; the framework owns none |

## Open questions

- **Compile-time refusal.** The check runs when the program starts, so an application with no test that builds its router learns on deploy; code generation would refuse at compile time and waits for proof that start-up is not enough ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md), [roadmap](../roadmap.md#the-definition-of-finished)).
- **A handler that wants to write as it goes** must use the streaming return type, which costs more than a plain answer ([ADR 0009](../adr/0009-the-response-comes-only-from-the-return-value.md), [responses](responses.md)).
- **Bounds on a section** (64 fields, 8 lists) are a judgement about what a real API reaches ([ADR 0010](../adr/0010-one-input-struct-with-sections-by-field-name.md)).
- **How an Input gets a field that is not a request part** is the Providers design ([middleware and Providers](middleware-and-providers.md)).
