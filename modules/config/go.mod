module github.com/nevindra/bites/modules/config

go 1.27

require (
	github.com/nevindra/bites v0.0.0
	go.yaml.in/yaml/v3 v3.0.6
)

// While the modules live beside the core, they build against the core beside them. A release tags
// the core first and replaces this line with its version (ADR 0070).
replace github.com/nevindra/bites => ../..
