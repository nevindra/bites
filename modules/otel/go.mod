module github.com/nevindra/bites/modules/otel

go 1.27

require (
	github.com/nevindra/bites v0.0.0
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/trace v1.47.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

// While the modules live beside the core, they build against the core beside them. A release tags
// the core first and replaces this line with its version (ADR 0070).
replace github.com/nevindra/bites => ../..
