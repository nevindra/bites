module github.com/nevindra/bites/modules/docsui

go 1.27

require github.com/nevindra/bites v0.0.0

// While the modules live beside the core, they build against the core beside them. A release tags
// the core first and replaces this line with its version (ADR 0070).
replace github.com/nevindra/bites => ../..
