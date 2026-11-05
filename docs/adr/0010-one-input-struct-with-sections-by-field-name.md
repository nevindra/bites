# A route's input is one struct whose sections are named Path, Query, Header, Cookie and Body

A typed handler takes exactly one input value. Its type is a struct whose fields named `Path`, `Query`, `Header`, `Cookie` and `Body` say where their contents come from; a route with no input takes `struct{}`. No tag says where a value comes from: the section it sits in does. Fields of any other name are filled by a Provider ([ADR 0023](0023-provided-sections-are-checked-at-registration.md)) or refused at Registration.

```go
type GetUser struct {
    Path  struct{ ID int64 }
    Query struct{ Expand Opt[bool] }
}

func (u *Users) Get(ctx context.Context, in GetUser) (User, error)
```

How text in `Path`, `Query`, `Header` and `Cookie` is decoded is [ADR 0042](0042-text-values-are-decoded-by-type.md); what `Body` may be is [ADR 0044](0044-the-body-type-decides-how-a-body-is-read.md); whether a field may be absent or null is [ADR 0041](0041-presence-is-a-type.md). A `Body` section on a GET, HEAD or DELETE route is refused at Registration.

**Rules for the text sections, settled while building them:** a query parameter the type does not have is ignored (a URL carries tracking and cache-busting parameters a client does not control), while a non-list field sent twice is a 400 `sent more than once`. `Opt` and lists are refused in `Path`, since a path segment is always present and single. Embedded structs are inlined as in the body. A section has at most 64 fields and 8 lists, a bound on the work one request can cause that a real API does not reach.

**An Input may embed a struct of sections** (decided 2026-11-04, from an application whose twenty routes of a space each repeated `Path struct{ ID uuid.UUID }`). An embedded struct whose fields are all sections (or embedded structs of sections) gives its sections to the Input, at their offsets in the Input, so `struct{ spacePath; Body Rename }` reads `in.Path.ID`, the 400 still says `path.id`, and the type may be unexported. An embedded struct with no section is what it was before, a field a Provider fills (`struct{ User }`), so no Input that registered before changes meaning. An embedded struct that mixes sections with other fields is refused, because whether its other fields are provided or are a mistake cannot be told from the type; and a section given twice is refused, because Go's promotion would read the shallower one and hide the other in silence. Before, such an embedding was refused as an unexported field (`spaceIn.idPath is unexported, so nothing can fill it`) or, for an exported type, asked for a Provider of it. Rejected: flattening every embedded struct (it would turn `struct{ User }`, a Provider's value, into a set of fields to fill), and a tag that marks an embedded struct as sections (a tag the compiler cannot check).

**Considered:** one argument a part (`func(ctx, p Path[P], b Body[B])`), closer to nilo's argument list. Rejected because Go has no variadic generics, so registration would need a method for each argument count, and a handler's signature would change shape whenever it gains a part. Huma's single struct needs a tag on every field (`path:"id"`) because it lets path, query and body fields sit side by side; naming the section removes the tag.
