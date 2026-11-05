# Configuration is a module that fills a typed struct from flags, the environment and files, and refuses every wrong setting at start-up

An application deployed in several environments, or split into services, reads settings from more than its code: a database address, a listen port, a timeout, a signing key. Written by hand it is `os.Getenv` in a dozen places, each with its own default and its own silent zero when the variable is missing or mistyped (`strconv.Atoi` ignored, a port of 0), found in production. Kratos (`config.New`, `Scan`), go-zero (`conf.MustLoad`), go-micro (`config`), Encore (`config.Load[T]`) and humacli have it ([`docs/research/framework-gap/microservices.md`](../research/framework-gap/microservices.md) sections 1.2, 2.2, 3.2 and 4.2, and [`huma.md`](../research/framework-gap/huma.md) section 16, which cite https://go-kratos.dev/docs/component/config/, https://go-zero.dev/reference/configuration/overview/, https://go-micro.dev/docs/config.html, https://encore.dev/docs/go/develop/config). This is the roadmap's "configuration read into a typed value", of the Important tier. Decided 2026-11-04; built the same day in `modules/config`. The probes behind its facts are in [`docs/research/config-module/`](../research/config-module/main.go).

**Decision.** Configuration is the module `github.com/nevindra/bites/modules/config` (package `config`, [ADR 0070](0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)). The application declares its settings as a struct, and `config.Load[T]` returns a filled `T` or an error that lists every setting that is wrong. Types are the contract here as in an Input: the struct says what a setting is, whether it may be missing, and what values it accepts, with the rules the core already has.

```go
type Config struct {
	Listen  string        // listen: APP_LISTEN, -listen
	Timeout time.Duration // timeout
	DB      struct {
		URL      config.Secret // db.url: APP_DB_URL, no flag
		PoolMax  PoolSize      // db.pool_max: APP_DB_POOL_MAX, -db-pool-max
		ReadOnly bool
	}
	Peers []string      // peers: APP_PEERS=a:1,b:2, -peers a:1,b:2
	Trace bites.Opt[string]
}

func (Config) Defaults() map[string]string { return map[string]string{"Listen": ":8080", "Timeout": "5s"} }

type PoolSize int

func (PoolSize) Range() (min, max int64) { return 1, 100 }

func main() {
	cfg, err := config.Load[Config](
		config.File(os.DirFS("/etc/app"), "app.yaml"),
		config.Env("APP", os.Environ()),
		config.Flags(os.Args[1:]),
		config.SecretFiles(os.DirFS("/")),
		config.Logger(logger),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	app := bites.New(bites.WithLogger(logger))
	db := openDB(cfg.DB.URL.Reveal(), int(cfg.DB.PoolMax))
	app.Provide(userFromSession(db)) // a Provider closes over what main built from cfg (ADR 0014, ADR 0023)
	...
}
```

**One way to load.** `Load[T any](opts ...Option) (T, error)` is the only entry. It reads no environment variable, no `os.Args` and no file path by itself: the application passes `os.Environ()`, `os.Args[1:]` and an `fs.FS`, so the module has no global state and reads exactly what it is given ([ADR 0026](0026-no-global-state-and-one-behaviour.md), whose rule it extends: not even the module looks at the process). There is no `MustLoad`: a library that exits or panics for a deployment mistake takes the choice of exit code, message stream and log format from `main`, and a panic's stack trace is noise for a missing variable. What is a mistake in the code is a panic and what is a mistake in the deployment is the returned error (below). The value is returned, not filled through a pointer, because a value the application pre-fills would act as a default that cannot tell zero from set ([ADR 0041](0041-presence-is-a-type.md)), a second spelling of `Defaults`.

**Origins and their order.** A setting is read from one of four origins, and the first that has it wins: **flag, then environment, then file, then default.** The order is fixed and does not depend on the order of the options; what is passed is the set of origins in use. A setting present in several is not an error (that is the point of overriding), and the log line of `config.Logger` says which origin won. `Flags`, `Env` and `File` each take no second spelling: `File` may be given more than once, and a later file wins over an earlier one (a base file, then a local one); two `Env`, two `Flags` or two `SecretFiles` options are refused. A file that does not exist is an error naming its path, unless it is given as `config.OptionalFile`; nothing is skipped in silence. There are no remote centres in this ADR (see Considered).

**Naming.** A setting has one canonical key, the struct's Go field names under [ADR 0011](0011-one-naming-policy-for-wire-names.md)'s snake_case (`bites.SnakeCase`) joined with `.`: `db.pool_max`. The file's key is that name, nested as the struct is (`{"db": {"pool_max": 20}}`). The environment variable is the prefix, then the key upper-cased with `.` as `_`: `APP_DB_POOL_MAX`. The flag is the key with `.` and `_` as `-`: `-db-pool-max`. A name no rule produces is given with the ADR 0011 method `WireNames() map[string]string` on the struct that has the field (keyed by Go field name, checked against the fields, each struct naming its own); it changes the word, and all three forms follow. Embedded structs are flattened as in an Input. The prefix of `Env` must be given and not empty: an unprefixed `Env` would read `PATH` and `HOME`.

**Types.** A field is a string; a bool (`true`, `false`, `1`, `0`); a signed or unsigned integer; a float; a `time.Duration`, read as Go reads one (`5s`, `1m30s`; `30` has no unit and is refused); a `time.Time` (RFC 3339); a `bites.Date`; any type implementing `encoding.TextUnmarshaler`; `config.Secret`; a struct of these, to any depth; and a slice of these. Every origin hands the decoder text, so a duration or an integer in `APP_TIMEOUT` decodes exactly as it does in a query parameter ([ADR 0042](0042-text-values-are-decoded-by-type.md)): a file's number is taken as the digits written (`1e3` stays `1e3` and is refused for an integer), and a YAML scalar as the text written, so `no` is the string `no` for a string field and a refused value for a bool, never the false of YAML 1.1 (probe, `docs/research/config-module/yaml/`). A slice is a JSON or YAML array in a file, and one comma-separated value in the environment and in a flag (`-peers a:1,b:2`), trimmed around the commas; an element cannot contain a comma there, and a list of such values is set in a file (the price [ADR 0042](0042-text-values-are-decoded-by-type.md) states for `CSV`). A flag given twice for one key is refused as in a query. A map and a slice of structs are read from a file only; the same key in the environment is the unknown setting below, whose message says to use the file. A pointer, a channel, a function and an interface are refused at `Load` (below), as in an Input. `null` in a file is refused (`null is not a value, remove the key`), because a setting is present or absent and absence has its rules.

**Presence and defaults** are the Input's ([ADR 0041](0041-presence-is-a-type.md)): **a plain field is required**; `bites.Opt[T]` may be absent (the settings it describes are read with `Get` and `Or`); a struct, at any depth, may have `Defaults() map[string]string` by Go field name, written as an operator would write it (`"Timeout": "5s"`), decoded by the same decoder and checked by the same rules as a sent value, each struct stating the defaults of its own fields and the outer struct's entry winning for a field both name. A default its type refuses, a name that is a field of nothing, and a default on an `Opt`, a map, a list of structs or a struct are refused, as in an Input. **A list of plain values has a default**, written as the comma-separated text the environment and a flag take (`"PublicOrigins": "https://a.example, https://b.example"`), each element checked by the element type's rules at `Load` and filled as from the environment, so an origin that sets the list replaces it whole (decided 2026-11-04: the first build refused it, and an application moved the default of a list of origins into an `Opt` and an `Or` in its code, out of the one place a reader looks; an Input's list keeps no default, since a client sends a list as repeated keys and no text form is the obvious one there). A value present but wrong (`APP_TIMEOUT=abc`) is never replaced by the default.

**Rules a type carries** are the ones [ADR 0021](0021-validation-is-a-method-and-a-few-constraint-types.md) gives a type, and a `PoolSize` written for a query parameter is a valid setting without a change: `Length`, `Range` (integer and float), `Values` (an enum) and `Check` are read from the field's type with their exported methods, and fail with the sentences of the Input's 400 (`expected an integer between 1 and 100`, `must be at least 12 characters`), copied into the module and held equal to the core's by a test that decodes the same type through a route. `Items`, `Format` and `Pattern` are refused in a configuration type with a message saying to put the rule in `Check`: the module does not carry the core's format tables, and a rule accepted and not checked would be the silence this ADR exists to prevent. An application's `Validate() error` on the root struct runs once after every setting decoded without a mistake and may return `bites.Invalid("db.pool_max", "must not exceed db.max_conns")`; the location is the canonical key, and it is listed with the others. The core's helper is reused because `Invalid` is already how an agent says "this field is wrong in this place".

**Secret.** `config.Secret` is `type Secret string` with `Reveal() string` as the only way to the text. Its `String`, `GoString`, `Format`, `MarshalText`, `MarshalJSON` and `LogValue` give `[redacted]`, so `fmt.Println(cfg)`, `%+v`, `slog.Info("cfg", "cfg", cfg)` and a JSON encoding of the whole struct never print it, and it is empty-checked as a string is. **No message of this module ever contains a setting's value**, secret or not: a database password pasted into `APP_DB_POOL_MAX` would otherwise reach the log as `expected an integer, got "hunter2"`. The message names the key, the origin and the rule, and the operator looks at the origin. A Secret has no flag, because the command line is visible in `ps` and `/proc`; setting one by flag is the problem `db.url: a Secret cannot be set by flag (the command line is visible to every user of the machine); use APP_DB_URL, APP_DB_URL_FILE or "db.url" in a file (flag -db-url)`, listed with the others, which never contains the value. A Secret in the environment may instead be `APP_DB_URL_FILE=/run/secrets/db_url`, a file whose content (one trailing newline removed) is the value, which is how Kubernetes and Docker mount a secret. It is read through the `fs.FS` of the option `config.SecretFiles(os.DirFS("/"))` (a leading slash is removed from the path, because an `fs.FS` has none), and a `_FILE` variable without that option is a problem that says so; both `APP_DB_URL` and `APP_DB_URL_FILE` set is refused (`APP_DB_URL and APP_DB_URL_FILE are both set; set one`). The first position read the file "through the same `fs.FS` as the files", and building it showed that cannot work: the directory of the configuration files (`os.DirFS("/etc/app")`) does not hold `/run/secrets/db_url`, and an `fs.FS` cannot step out of its root. The path in an error is never printed, because a password pasted into the wrong variable would be one. The `_FILE` form exists for Secret and for nothing else, so a `_FILE` variable for another key is an unknown setting.

**Unknown settings are refused.** A key in a file that no field has, an environment variable under the prefix that no field maps to, and a flag nobody defined are each an error naming the origin, the key and, when one is near, the field (`did you mean`; near means differing in case, `_` and `-`, or one edit, the rule of [ADR 0042](0042-text-values-are-decoded-by-type.md)), because a misspelled `APP_DB_UrL` otherwise leaves the default in force and looks exactly like a setting that worked. The environment of a Kubernetes pod holds variables a service did not choose: for every Service in the namespace Kubernetes injects `<SERVICE>_SERVICE_HOST`, `<SERVICE>_SERVICE_PORT`, `<SERVICE>_PORT` and `<SERVICE>_PORT_<n>_TCP...`, so a prefix equal to a Service's name (`APP` and a Service `app`) meets them. The application may also itself read a variable under the prefix (`APP_CONFIG` for the path of its file). `Env("APP", os.Environ(), "APP_CONFIG", "APP_PORT")` takes the variable names the application says are not settings, as exceptions it wrote, in the one place a reviewer greps. Anything else under the prefix is refused, and a variable that has the shape of one Kubernetes injects (`PORT`, `PORT_<n>_...`, `SERVICE_HOST`, `SERVICE_PORT` or `SERVICE_PORT_...` after the prefix) is refused with the likely cause named, exactly:

```
APP_PORT: unknown setting (env); Kubernetes injects APP_SERVICE_HOST, APP_PORT and APP_PORT_<n>_TCP... for a Service named app: set enableServiceLinks: false on the pod, or list APP_PORT among the non-settings of Env
```

The prefix and the variable are the ones of the case, and the service name is the prefix in lower case with `_` as `-`. A near miss gets `did you mean` instead. The same key of a map or of a list of structs in the environment or in a flag is refused with `a map or a list of structs is read from a file only, set labels in the file`, and a `_FILE` variable for a key that is not a Secret with `only a Secret is read from a file named by a _FILE variable, and listen is not one`.

**Every wrong setting is listed at once**, in field order, then the unknown ones in the order their origins were given, at most 20 lines then `and N more` ([ADR 0012](0012-client-errors-are-400-problem-json.md)'s cap), as one `*config.Error` whose `Problems []Problem{Key, Origin, Message}` is machine-readable (`errors.As`) and whose text is what an operator reads. A line is `key: message (origin)`; for an unknown setting the origin comes before the suggestion, as below. The key of an unknown setting is the variable or the flag as written, and the key of a file that could not be read is its name. A flag without its value, a flag given twice and an argument that is not a flag (`settings are given as -name=value`) are problems listed with the unknown ones of the flags:

```
config: 5 settings are wrong
  db.url: missing; set APP_DB_URL, APP_DB_URL_FILE or "db.url" in app.yaml
  timeout: expected a duration such as 5s or 1m30s (env APP_TIMEOUT)
  db.pool_max: expected an integer between 1 and 100 (file app.yaml, line 12)
  peers[1]: expected a host:port (flag -peers)
  APP_DB_UrL: unknown setting (env); did you mean APP_DB_URL?
```

and, when exactly one is wrong, `config: 1 setting is wrong` with one line. `-h` and `-help` do not exit: `Load` returns an error for which `errors.Is(err, flag.ErrHelp)` holds and whose text is the usage, one line per key giving its flag, its variable, its file key, its type, its default or `required` or `optional`, and its description from the struct's `Descriptions() map[string]string` ([ADR 0020](0020-descriptions-come-from-registration-and-later-from-doc-comments.md)); the module prints nothing itself. A file that is not valid JSON or YAML is one error naming the file, the line and the parser's reason, and no setting is read from it. A duplicate key in a file is an error naming both lines (`duplicate key; first written at line 3`), because both parsers' defaults keep the last silently (probe 3a: `encoding/json/v2` into a map and `go.yaml.in/yaml/v3` into a `Node` took the repeated key without complaint; `jsontext` refuses it).

**Registration-class mistakes are panics at `Load`, before any origin is read**, naming the field and where `Load` was called, as [ADR 0007](0007-mistakes-are-refused-at-registration.md) does, and each has a row in the module's refusal table. The message is the text below followed by a line `called at file.go:12`. The example of two fields with one key is `DBUrl` and `DB.URL`: the first position wrote `DBURL`, which the naming policy reads as the one word `dburl`, so it never collided. A `Defaults` entry on an `Opt`, a list or a struct, and a `Check` with a pointer receiver, are refused the same way.

| Mistake | Message |
|---|---|
| a type that cannot be a setting | `config.Load[Config]: field DB.Conn has type net.Conn, which cannot be read from text; use a string and open it in main` |
| a pointer | `config.Load[Config]: field Trace is a *string; use bites.Opt[string] for a setting that may be missing` |
| two fields with one key | `config.Load[Config]: DBUrl and DB.URL are both db_url (APP_DB_URL, -db-url); name one with WireNames` |
| an unexported field | `config.Load[Config]: field cache is unexported, so no origin can fill it; export it or move it out of Config` |
| `Items`, `Format` or `Pattern` | `config.Load[Config]: field Peers has an Items rule, which settings do not check; put the rule in a Check method` |
| a default its type refuses | `config.Load[Config]: the default of Timeout, "5 seconds", is not a duration: expected a duration such as 5s or 1m30s` |
| a list default with an element its type refuses | `config.Load[Config]: the default of Ports, "80,http", has element 1, "http", which is refused: expected an integer` |
| a default for a map or a list of structs | `config.Load[Config]: Defaults names Tags, which is a map or a list of structs; those are read from a file only, so write its default in a file` |
| a `Defaults`, `WireNames` or `Descriptions` entry naming nothing | `config.Load[Config]: Defaults names Timout, which is not a field of Config; did you mean Timeout?` |
| `Env` with an empty prefix | `config.Env: the prefix is empty, which would read PATH and HOME; give the application's name, such as "APP"` |
| two `Env`, two `Flags`, two `SecretFiles` | `config.Load[Config]: Env was given twice; give one Env` |
| a file that is not `.json`, `.yaml` or `.yml` | `config.File: the file "app.toml" does not end in .json, .yaml or .yml, the two formats read` |
| a `Validate` on a nested struct | `config.Load[Config]: DB has a Validate method, which is never called; declare it on Config` |
| a key that is the flag set's own `h` or `help` | `config.Load[Config]: key help collides with the flag -help; rename the field with WireNames` |

**Reloading: no.** `Load` reads once, at start-up, and the value is a plain Go value the application copies into the servers, pools and Providers it builds ([ADR 0014](0014-services-are-plain-go-values.md)); nothing watches a file. A setting changed at run time cannot reach a `*sql.DB` already opened, a listener already bound or a limit fixed at Registration ([ADR 0024](0024-limits-are-on-by-default.md)), and a reload that applies half a file is a configuration the operator never wrote. A change is a restart, which a rolling deploy makes safe. The honest cost: a file mounted from a Kubernetes ConfigMap changes on disk and the running process keeps the old values until it restarts, which is the same behaviour as a changed environment variable and is stated in the guide.

**How it hands values to Providers without global state.** `Load` returns a value; nothing is registered anywhere. `main` builds what the configuration says (the pool, the key, the clients) and gives it to the handlers' struct and to the Providers by closure, exactly as any service ([ADR 0014](0014-services-are-plain-go-values.md), [ADR 0023](0023-provided-sections-are-checked-at-registration.md)). A Provider of the whole `Config` is possible and not advised: it copies the struct per request for no gain.

**How a test sets configuration.** An application test builds the `Config` literal and passes it to the same constructors `main` uses; there is no global to reset. A test of the loading itself passes the origins it wants: `config.Env("APP", []string{"APP_TIMEOUT=2s"})`, `config.Flags([]string{"-listen=:0"})`, `config.File(fstest.MapFS{"app.yaml": {Data: []byte("listen: :9\n")}}, "app.yaml")`, so it needs neither `t.Setenv` (global, and refused by `t.Parallel`) nor the disk.

**What is logged.** `config.Logger(l *slog.Logger)` makes `Load`, when it succeeds, write one record per setting in the form of the core's start-up settings ([ADR 0026](0026-no-global-state-and-one-behaviour.md)): `config: setting key=db.pool_max value=20 origin="file app.yaml, line 12"`, with `origin` one of `default`, `absent` (an `Opt` nothing set, with `value=<absent>`), `file <name>, line <n>`, `env <NAME>` and `flag -<name>`, and `value=[redacted]` for a Secret; the value of a map or a list of structs is `(from the file)`. The logger is an option and not a default because the module has no global to take it from; without it nothing is logged and the doc comment of the option says so. A setting that came from the default is marked, which is how an operator sees that nothing set `timeout`.

**File formats.** JSON, through `encoding/json/jsontext` (no dependency), and YAML, through `go.yaml.in/yaml/v3`, chosen by the file's extension (`.json`, `.yaml`, `.yml`); another extension is refused naming the two. YAML is in because JSON has no comments, and the files an operator edits, the ones a ConfigMap holds, are YAML. The cost is in the module's own `go.mod` and `go.sum` and says so in its package doc ([ADR 0070](0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)); an application that never imports `modules/config` never links it. The YAML node's scalar is used as the text written, which keeps YAML 1.1's conversions (`no`, `yes`, `0o17`) out; anchors, merge keys and tags are refused with the line (`a tag, an anchor or a merge key is a second way to write a value; write the value`). TOML is left until an application asks.

**What it costs.** Start-up only: `Load` reflects over `T` once and reads its files once. **Axes** ([ADR 0003](0003-the-trade-budget.md)): allocations per request, none, since nothing runs on the request path; memory per idle connection, none; the module is not linked by the core and `deps_test.go` is unaffected. The one dependency, `go.yaml.in/yaml/v3`, is the module's. The module is tested with `cd modules/config && go test ./...`.

**What it needs from the core.** A module uses the exported API only ([ADR 0070](0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)), and one piece is missing: `bites.Opt[T]` has no exported way to set a value through reflection (its fields are unexported and `Some` is generic, so the module cannot construct an `Opt` of a type known only at run time). The core adds `func (o *Opt[T]) Set(v T)`, with its reference heading and a doc comment saying it exists for decoders that fill an `Opt` through `reflect`; the module finds it with `reflect.Value.Addr().MethodByName("Set")`. It is a second spelling beside `Some` and `OptOf`, which the first principle of [ADR 0077](0077-four-principles-in-the-order-they-win.md) allows only with an ADR, and the reason is that no existing spelling can be reached by reflection: `Some` is generic, so its type argument must be known when the code is compiled, and the fields of `Opt` are unexported. Its doc comment tells application code to use `Some`, so an agent that sees only signatures builds an `Opt` as before ([ADR 0041](0041-presence-is-a-type.md) has the pointer). The core's own text decoder is not exported and is not made so: the module decodes the types listed above itself with `strconv`, `time` and `TextUnmarshaler`, and the test that decodes one type through a route and through `Load` and compares the sentences holds the two together. A core export of the decoder (`bites.DecodeText`) was weighed and left out: it would freeze the internal decoder's signature for one consumer, and its sentences are what a `Length` or a `Range` method needs rather than its machinery.

**Refusals in the table above, and what cannot be caught early:** whether the files exist, whether the environment holds the variable, and whether a value is valid are facts about the deployment and are returned as the `*config.Error`, not panics. A deploy-time test (`Load` with the production file and a fake environment) catches them in CI.

**Scenarios it was put against.**

- **A service moved from a developer's laptop to Kubernetes.** Defaults give `:8080` and a 5 s timeout, `app.yaml` from a ConfigMap sets the database pool, the pod's environment sets `APP_DB_URL_FILE=/run/secrets/db_url` and a flag in the container args sets `-listen=:9000`. Each wins in the stated order, and the settings log says `env APP_DB_URL_FILE`, `file app.yaml, line 4` and `flag -listen`. The password is never in a log, in `ps` or in `%+v`.
- **A hostile or careless deployment.** `APP_DB_POOL_MAX=hunter2` (a password in the wrong variable) is `db.pool_max: expected an integer (env APP_DB_POOL_MAX)` with no value in it. `-db-url=postgres://u:p@h/d` is refused by the Secret rule before the command line is repeated anywhere.
- **A typo.** `APP_DB_UrL=...` and `db_ur:` in the file are listed with `did you mean`, together with every other mistake, in one start-up failure rather than one at a time over five deploys.
- **Six wrong settings.** All six are in one `*config.Error`, and a CI job that loads the production file with a placeholder environment catches the missing ones before a deploy.
- **A setting two rules apply to.** `PoolSize` with `Range` and an `Opt[PoolSize]` for a replica count behave as the same types do in a query, and a `Validate` that compares two fields answers with `bites.Invalid`.
- **Two services in one repository sharing a base file.** `File(a)`, `File(b)` with the later winning gives the base-then-local layering; a key in both is not an error, a key in neither a required field names every way to set it.
- **An agent that sees only signatures, doc comments and refusals.** It writes a struct, calls `Load` and meets, in this order: a panic naming the field when the type is wrong, a list of every missing key with the variable, flag and file key that would set it, and `-h` listing them all. It never needs to know the order of origins to be right, since the usage and the log both state which origin won.
- **A test run in parallel.** No `t.Setenv`, no files: origins are values passed in.

**What it fails at.**

- **A list element with a comma** cannot be set in the environment or by flag; the file is the way, and the message says so. The alternative (a repeated variable) has no form in the environment.
- **A map or a list of structs cannot be overridden by one environment variable**; replacing a whole `labels` map means replacing it in a file. Overrides are per scalar, which is what twelve-factor deployment uses.
- **No reload.** A secret rotated on disk needs a restart; the application that cannot restart reads the file itself and holds it in an `atomic.Pointer`.
- **The prefix rule can refuse a harmless variable.** A cluster that injects variables under the application's prefix needs them listed in `Env`, once, by hand; that is the price of catching `APP_DB_UrL`.
- **The sentences of `Length` and `Range` are copied**, so the core changing one needs the module to follow; the equality test fails first.
- **Three origins and a fixed order are not configurable**; a deployment that wants the file to beat the environment cannot say so. It is the order go-zero documents ("priority env, then file, then tag default", https://go-zero.dev/reference/configuration/overview/) and go-micro's for its wiring ("explicit code options always win", then env, then defaults, https://go-micro.dev/docs/config.html); Kratos leaves the order to the application, later sources overriding earlier ones (https://go-kratos.dev/docs/component/config/). Flags above the environment is the humacli rule ("CLI arg wins").
- **YAML brings a dependency** into every application that uses the module, which JSON-only would not.
- **A secret in the environment is visible to a process that can read `/proc/<pid>/environ`**; `_FILE` is the answer and is not the default.

**Considered and rejected.**

- **Configuration in the core** (`bites.LoadConfig`). The core reads no environment variable and imports the standard library only ([ADR 0026](0026-no-global-state-and-one-behaviour.md), [ADR 0006](0006-no-third-party-dependency-in-the-core.md)), and YAML needs a dependency; a module is how the rule is kept ([ADR 0070](0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)).
- **Struct tags** (`env:"DB_URL" default:"5s" validate:"min=1"`, as go-zero's `json:",env=...,range=[1:100]"` and humacli's `default`/`doc`/`short` tags). A tag is a second spelling of what `Defaults`, `Range` and `WireNames` already say, is not checked by the compiler, and its rules would be a third dialect beside the Input's. Here no tag exists to misspell, and every mistake is a refusal of [ADR 0007](0007-mistakes-are-refused-at-registration.md)'s kind. The gain of a tag, the key beside the field, is given by the field's name and a comment.
- **`MustLoad` or a `Load` that exits** (go-zero). Rejected above; also two spellings of one operation.
- **Filling a pointer the application pre-filled** (`Load(&cfg)`), so that its values are the defaults. A zero cannot be told from a set value, so a required field with a zero default is silently optional, the failure [ADR 0041](0041-presence-is-a-type.md) recorded for a route-option default.
- **An untyped store read by key** (`c.Get("db.url").String()`, go-micro and viper). It moves a missing or mistyped setting from start-up to the first use, which is the problem; Kratos' `Scan` and Encore's `config.Load[T]` are the typed form this takes.
- **`${VAR}` interpolation in files** (go-zero `UseEnv`). It is a second spelling of an environment override, makes a file value not plain text, and fails silently when the variable is unset unless extra rules are added.
- **Reading `.env` files.** A dotenv file is an environment: the application reads it and passes the lines to `Env`, in the one place that knows its path.
- **A configurable order of origins**, or a `Source` interface for remote centres (Consul, etcd, Nacos, Kubernetes, as Kratos' contrib modules and go-zero's `configcenter`). A remote source is a network call at start-up with its own failure modes and a watch that implies reloading; it is a separate module on its trigger, and the closed set of three origins is what lets the order be one sentence. The reopening condition is an application that must read from a centre.
- **Watching and reloading** (Kratos `Watch`, go-zero `AddListener`). Rejected above; the evidence is the pool and the limits that cannot change.
- **JSON only, from the standard library.** It would keep the module free of a dependency, and was the first position. It fails the operator: no comments, quoting every string, and the files a deployment keeps are YAML. TOML (go-zero reads it) is left until asked.
- **Word "source" for an origin.** [`GLOSSARY.md`](../../GLOSSARY.md) keeps it as a word to avoid for a Section, so the word is *origin*, and a new glossary entry gives it (with *Setting* and *Secret*).
- **Reading `_FILE` through the `fs.FS` of the files.** It was the first position, and `os.DirFS("/etc/app")` cannot reach `/run/secrets`; a separate `SecretFiles` option takes the `fs.FS` the secrets are mounted in.
- **Calling the module `env` or `settings`.** It reads files and flags as well; `config` is what the four frameworks call it.

**Tests the implementation must carry** (`modules/config/config_test.go`, sentences about behaviour, run with and without `-race`):

- `TestAFlagBeatsTheEnvironmentWhichBeatsAFileWhichBeatsADefault`
- `TestTheOrderOfOriginsDoesNotDependOnTheOrderOfTheOptions`
- `TestALaterFileBeatsAnEarlierOne`
- `TestAMissingFileIsAnErrorNamingItAndAnOptionalFileIsNot`
- `TestAFieldNameBecomesAFileKeyAnEnvironmentVariableAndAFlagByTheNamingPolicy`
- `TestWireNamesChangesTheWordInAllThreeForms`
- `TestTwoFieldsWithOneKeyArePanicsAtLoadNamingBoth`
- `TestADurationAnIntegerAndABoolDecodeFromTextAsAQueryParameterDoes`
- `TestADurationWithoutAUnitIsRefusedNamingTheKey`
- `TestAYAMLScalarIsTheTextWrittenSoNoIsNotFalse`
- `TestAJSONNumberIsTheDigitsWritten`
- `TestADuplicateKeyInAFileIsAnErrorNamingBothLines`
- `TestNullInAFileIsRefused`
- `TestAnAnchorATagAndAMergeKeyInYAMLAreRefusedWithTheLine`
- `TestAPlainFieldIsRequiredAndTheErrorNamesEveryWayToSetIt`
- `TestAnOptFieldMayBeAbsentAndSaysSoThroughGetAndOr`
- `TestDefaultsFillWhatNoOriginSetAndAreCheckedByTheTypesRules`
- `TestAValueSentBadlyIsAnErrorAndNeverFallsBackToTheDefault`
- `TestADefaultItsTypeRefusesIsAPanicNamingTheFieldAndTheText`
- `TestLengthRangeValuesAndCheckOfANamedTypeAreCheckedWithTheSentencesOfAnInput`
- `TestTheSentencesOfLengthAndRangeEqualTheOnesTheCoreAnswersInAQuery`
- `TestItemsFormatAndPatternInAConfigTypeAreRefusedAtLoad`
- `TestValidateOnTheRootRunsAfterDecodingAndItsInvalidIsListedWithTheOthers`
- `TestAValidateOnANestedStructIsRefusedAtLoad`
- `TestSeveralWrongSettingsAreListedAtOnceInFieldOrderEachNamingKeyAndOrigin`
- `TestMoreThanTwentyProblemsAreCutWithAnAndNMoreLine`
- `TestTheErrorIsFoundWithErrorsAsAndListsProblemsWithKeyOriginAndMessage`
- `TestNoMessageContainsASettingsValue`
- `TestAPasswordPastedIntoANumbersVariableNeverReachesTheError`
- `TestAnUnknownKeyInAFileIsRefusedWithItsLineAndANearOneIsSuggested`
- `TestAnUnknownVariableUnderThePrefixIsRefusedAndOneTheApplicationNamedIsNot`
- `TestAnUnknownFlagIsRefusedInTheSameListAsTheOthers`
- `TestEnvWithAnEmptyPrefixIsAPanic`
- `TestASliceIsAnArrayInAFileAndCommaSeparatedInTheEnvironmentAndAFlag`
- `TestAFlagGivenTwiceForOneKeyIsRefused`
- `TestAMapAndAListOfStructsAreReadFromAFileOnlyAndTheEnvironmentSaysSo`
- `TestAPointerAChannelAndAnUnexportedFieldArePanicsAtLoad`
- `TestASecretPrintsRedactedInPrintfSlogAndJSONAndRevealsOnlyThroughReveal`
- `TestASecretHasNoFlagAndSettingOneByFlagIsRefusedWithoutItsValue`
- `TestASecretIsReadFromAFileNamedByTheFileVariableWithItsNewlineTrimmed`
- `TestASecretSetByBothTheVariableAndItsFileVariableIsRefused`
- `TestAFileVariableForAKeyThatIsNotASecretIsAnUnknownSetting`
- `TestHelpReturnsAnErrorForWhichErrorsIsFlagErrHelpHoldsAndListsEveryKeyItsOriginsAndItsDefault`
- `TestTheLoggerWritesOneRecordPerSettingWithItsOriginAndTheSecretRedacted`
- `TestWithoutALoggerNothingIsWritten`
- `TestLoadReadsNoEnvironmentVariableAndNoProcessArguments` (sets a variable with `t.Setenv` and loads with no `Env`)
- `TestTwoLoadsInParallelDoNotShareState`
- `TestNoImportOfTheCoresInternalPackagesIsMade` (the module test of ADR 0070)
- `TestAnOptIsFilledThroughSetAndTheCoreReadsItBack` (the core hook, held also in `internal/wire`)
- `TestAKubernetesServiceLinkVariableIsRefusedNamingTheCauseAndTheTwoWaysOut`
- `TestAFileWithAnExtensionOtherThanJSONOrYAMLIsAPanicNamingTheCall`
- `TestASyntaxErrorInAFileIsOneErrorNamingTheFileAndTheLine`

**Documentation this needs** (named so it is not forgotten): the module's reference page `docs/reference/config.md` with a heading per exported name (`Load`, `Option`, `File`, `OptionalFile`, `Env`, `Flags`, `Logger`, `Secret`, `Error`, `Problem`), a guide `docs/guide/configuration.md`, the glossary entries *Setting*, *Origin* and *Secret*, the design page for operations (where [ADR 0029](0029-shutdown-drains-and-then-closes.md) is joined), the roadmap line moved to built (the maintainer's, not part of this change), the deploying guide's mention of configuration, and `docs/reference/input.md` for `Opt.Set`. The reference page also lists `SecretFiles` and the methods of `Secret`.
