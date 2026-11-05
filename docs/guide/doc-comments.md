# Writing descriptions as doc comments

**`bites-doc` reads the doc comments of your Input and output types and generates the `Descriptions` methods, so the prose of the API document is written once, in the comment above the field.**

**Reference:** [`bites-doc`](../reference/tools.md#bites-doc) · **Design:** [OpenAPI](../design/openapi.md)

[The OpenAPI document](./openapi.md) takes field descriptions from a `Descriptions() map[string]string` method on a type, checked at Registration against the fields the codec reads. Writing that map by hand puts the prose away from the field. The generator ([ADR 0072](../adr/0072-descriptions-are-generated-from-doc-comments-by-bites-doc.md)) writes it from the comments.

## Write the comment, add one line

Comment the fields, and put a `go:generate` line in the package:

```go
//go:generate go run github.com/nevindra/bites/cmd/bites-doc

// ListQuery filters and pages a list of posts.
type ListQuery struct {
	// Page counts from 1.
	Page int
	// Tag keeps the posts that have it.
	Tag bites.Opt[string]
}
```

`go generate ./...` writes `bites_descriptions_gen.go` beside the sources:

```go
func (ListQuery) Descriptions() map[string]string {
	return map[string]string{
		"Page": "Page counts from 1.",
		"Tag":  "Tag keeps the posts that have it.",
	}
}
```

The document then has those sentences on the `page` and `tag` parameters. A comment after the field on the same line counts too, and the lines of a paragraph are joined into one string. The text is kept as Go style writes it, so the field's name stays in front.

## What it describes, and what it tells you

Every named struct type in the package with a documented, exported field gets a method, unexported types included (a Path or Query section is often a type nothing outside the package names), and so do the fields of a same-package struct it embeds. Section types must be named for their fields to be described: `Path struct{ ID int64 }` has no type to hang a method on, so write `Path UserPath` and the generator says so when it meets the anonymous one. A field with `json:"-"` is not on the wire, so a comment on it is reported and left out, because Registration would refuse its description. A type that already has a `Descriptions` method written by hand is left as it is.

Registration still checks the result against the wire. If you rename a field and forget to regenerate, the stale key names a field that no longer exists and the app refuses to start, naming the route and the line. Run `bites-doc -check` in CI to fail the build when the generated file is out of date. [`examples/described`](../../examples/described/described.go) is a package that does all of this, and a test builds its document.

The prose of an operation (`Summary`, `Description`, `Tag`) is still given as route options; the generator describes fields only.
