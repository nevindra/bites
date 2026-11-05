module github.com/nevindra/bites/examples/shop

go 1.27

require (
	github.com/nevindra/bites v0.0.0
	github.com/nevindra/bites/modules/docsui v0.0.0
	github.com/nevindra/bites/modules/jwt v0.0.0
)

// While the example lives beside the core, it builds against the core and the modules beside it
// (ADR 0070).
replace (
	github.com/nevindra/bites => ../..
	github.com/nevindra/bites/modules/docsui => ../../modules/docsui
	github.com/nevindra/bites/modules/jwt => ../../modules/jwt
)
