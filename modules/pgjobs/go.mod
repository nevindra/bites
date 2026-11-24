module github.com/nevindra/bites/modules/pgjobs

go 1.27

require (
	github.com/jackc/pgx/v5 v5.10.0
	github.com/nevindra/bites v0.0.0
	github.com/nevindra/bites/modules/pgnotify v0.0.0-20261124162723-0fae1ed82051
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

// While the modules live beside the core, they build against the core beside them, and pgjobs
// against the pgnotify beside it. A release tags the core first, then pgnotify, and replaces these
// lines with their versions (ADR 0070). pgnotify is required at the commit that gave it PublishTx, so
// an application outside this repository resolves it without a replace of its own.
replace github.com/nevindra/bites => ../..

replace github.com/nevindra/bites/modules/pgnotify => ../pgnotify
