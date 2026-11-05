module github.com/nevindra/bites/examples/chat

go 1.27

require (
	github.com/jackc/pgx/v5 v5.10.0
	github.com/nevindra/bites v0.0.0
	github.com/nevindra/bites/modules/pgnotify v0.0.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

// While the example lives beside the core, it builds against the core beside it (ADR 0070).
replace github.com/nevindra/bites => ../..

replace github.com/nevindra/bites/modules/pgnotify => ../../modules/pgnotify
