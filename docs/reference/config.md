# config

**The module that fills a typed struct from flags, the environment and files, and refuses every wrong setting at start-up with one error that lists them all.**

**Guide:** [configuration](../guide/configuration.md) · **Design:** [principles](../design/principles.md)

`github.com/nevindra/bites/modules/config` is a module of its own ([ADR 0070](../adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)), so the core carries none of it. Its names are not checked by the documentation tests, which read the root, `bitetest` and `stdhttp`; its own tests are run with `cd modules/config && go test ./...`. The decision is [ADR 0080](../adr/0080-configuration-is-a-module-that-fills-a-typed-struct-from-flags-environment-and-files.md). It depends on `go.yaml.in/yaml/v3` for YAML files, in its own `go.mod`.

A setting is read from the first origin that has it: flag, then environment, then file (a later file before an earlier one), then `Defaults`. The order is fixed and does not depend on the order of the options. The module reads no environment variable, no `os.Args` and no path by itself.

## The struct

The key of a field is its Go name in snake case joined with dots, under the naming policy of [ADR 0011](../adr/0011-one-naming-policy-for-wire-names.md): `DB.PoolMax` is `db.pool_max`, in a file nested as the struct is. Its variable is the `Env` prefix and the key in capitals, `APP_DB_POOL_MAX`, and its flag is the key with dashes, `-db-pool-max`. A `WireNames() map[string]string` method on the struct that has the field changes the word in all three forms. An embedded struct is flattened.

A field is a string, a bool (`true`, `false`, `1`, `0`), an integer, a float, a `time.Duration` (`5s`, `1m30s`; `30` has no unit and is refused), a `time.Time` (RFC 3339 with an offset), a `bites.Date`, a type with `UnmarshalText`, a `Secret`, a struct of these to any depth, or a slice of these. A slice is an array in a file and one comma-separated value in the environment and in a flag. A map and a slice of structs are read from a file only. A pointer, a channel, a function, an interface and a `bites.Null` are refused when `Load` is called.

A plain field is required. A `bites.Opt[T]` may be absent: read it with `Get` and `Or`. A struct, at any depth, may have `Defaults() map[string]string` by Go field name, written as an operator would write it (`"Timeout": "5s"`, or `"DB.PoolMax": "20"` for a field of a nested struct); the outer struct's entry wins for a field both name. A list of plain values takes its default as the comma-separated text the environment takes (`"Origins": "https://a.example, https://b.example"`), each element checked by its type, and any origin that sets the list replaces it whole; a default on an `Opt`, a map, a list of structs or a struct is refused. A value that is present but wrong is never replaced by the default. `Descriptions() map[string]string` gives the usage its sentences.

A named type carries the rules a type carries in an Input ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)): `Length`, `Range`, `Values` and `Check`, with the same sentences. `Items`, `Format` and `Pattern` are refused, with a message saying to put the rule in `Check`. The root struct may have `Validate() error`, run once after every setting decoded, which may return `bites.Invalid("db.pool_max", "must not exceed db.max_conns")` (several joined with `errors.Join`); a `Validate` on a nested struct is refused.

### Registration-class refusals

These are panics when `Load` is called, before any origin is read, and each ends with `called at file:line`.

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
| a `Validate` on a nested struct | `config.Load[Config]: field DB has a Validate method, which is never called; declare it on Config` |
| a key that is the flag set's own `h` or `help` | `config.Load[Config]: key help collides with the flag -help; rename the field with WireNames` |
| a file with another extension | `config.File: the file "app.toml" does not end in .json, .yaml or .yml, the two formats read` |

### `config.Load`

```go
func Load[T any](opts ...Option) (T, error)
```

Load returns a `T` filled from the origins it is given, or an error: a `*Error` that lists every wrong setting, or, for `-h` and `-help`, an error for which `errors.Is(err, flag.ErrHelp)` holds and whose text is the usage (one line per key: its flag, its variable, its file key, its type, its default or `required`, and its description). Load prints nothing and never exits; there is no `MustLoad`, so the exit code and the stream stay `main`'s choice.

`T` must be a struct. A mistake in `T` or in the options is a panic (above); a mistake in the deployment is the returned error.

### `config.Option`

```go
type Option interface{ /* unexported */ }
```

Option says one origin, or the logger, to `Load`. It is made by `File`, `OptionalFile`, `Env`, `Flags`, `SecretFiles` and `Logger`. The order of the options does not decide which origin wins; it decides the order in which the unknown settings of each origin are listed.

### `config.File`

```go
func File(fsys fs.FS, name string) Option
```

File reads the settings in the file `name` of `fsys`, JSON (through `encoding/json/jsontext`) or YAML (through `go.yaml.in/yaml/v3`), chosen by the extension `.json`, `.yaml` or `.yml`. It may be given more than once, and a later file wins over an earlier one. A file that does not exist or cannot be read is an error naming it. A file that is not valid is one error naming the file, the line and the parser's reason, and no setting is read from it. A scalar is the text written: `no` is the string `no` and `1e3` stays `1e3`. A duplicate key is an error naming both lines, `null` is refused, and so are a YAML tag, anchor, alias and merge key. It panics for a nil `fsys` and for another extension.

### `config.OptionalFile`

```go
func OptionalFile(fsys fs.FS, name string) Option
```

OptionalFile is `File` for a file that may not exist. A file that exists and is wrong is still an error.

### `config.Env`

```go
func Env(prefix string, environ []string, notSettings ...string) Option
```

Env reads the variables of `environ` (`os.Environ()`) that start with `prefix` and an underscore. The prefix must not be empty. A variable under the prefix that no setting maps to is an error that names the near setting when there is one (`APP_DB_UrL: unknown setting (env); did you mean APP_DB_URL?`). The names after `environ` are variables the application says are not settings, skipped.

A Kubernetes pod holds variables a service did not choose: for every Service in the namespace Kubernetes injects `<SERVICE>_SERVICE_HOST`, `<SERVICE>_PORT`, `<SERVICE>_PORT_<n>_TCP...`, so a prefix equal to a Service's name meets them. The message for such a variable says so:

```text
APP_PORT: unknown setting (env); Kubernetes injects APP_SERVICE_HOST, APP_PORT and APP_PORT_<n>_TCP... for a Service named app: set enableServiceLinks: false on the pod, or list APP_PORT among the non-settings of Env
```

A `Secret` may also be set by its variable with the suffix `_FILE`. The same key in a map or a list of structs is an unknown setting whose message says to use the file. It panics for an empty prefix.

### `config.Flags`

```go
func Flags(args []string) Option
```

Flags reads `args` (`os.Args[1:]`) as `-db-pool-max=20`, `--db-pool-max 20` or, for a bool, `-read-only`. A flag nobody defined, a flag given twice, a flag without its value and an argument that is not a flag are errors listed with the others. A `Secret` has no flag: setting one is an error that never holds the value. `-h` and `-help` return the usage as described under `Load`.

### `config.SecretFiles`

```go
func SecretFiles(fsys fs.FS) Option
```

SecretFiles gives the `fs.FS` from which the files named by `_FILE` variables are read; for a real deployment `os.DirFS("/")`, so that `APP_DB_URL_FILE=/run/secrets/db_url` reads that file (a leading slash is removed). The content, with one trailing newline removed, is the value. Without it a `_FILE` variable is an error that says so. Both the variable and its `_FILE` form set is an error. It is a separate option from `File` because the directory of the configuration files is not where a platform mounts secrets. It panics for a nil `fsys`.

### `config.Logger`

```go
func Logger(l *slog.Logger) Option
```

Logger makes `Load`, when it succeeds, write one record per setting: `config: setting key=db.pool_max value=20 origin="file app.yaml, line 12"`. The origin is `default`, `absent` for an `Opt` nothing set, `file <name>, line <n>`, `env <NAME>` or `flag -<name>`, and the value of a `Secret` is `[redacted]`. Without `Logger` nothing is logged.

### `config.Secret`

```go
type Secret string
```

Secret is a setting whose text must never be printed. `Println`, `%v`, `%+v` and `%#v`, `log/slog` and a JSON or text encoding of the whole configuration write `[redacted]`. A Secret has no flag, because the command line is visible in `ps` and `/proc`.

### `config.Secret.Reveal`

```go
func (s Secret) Reveal() string
```

Reveal returns the secret's text. It is the only way to it. The other methods, `String`, `GoString`, `Format`, `MarshalText`, `MarshalJSON` and `LogValue`, return `[redacted]`.

### `config.Error`

```go
type Error struct{ Problems []Problem }
```

Error is what `Load` returns when settings are wrong; find it with `errors.As`. `Problems` holds every wrong setting, in field order, then the unknown ones in the order their origins were given. Its text shows at most 20 lines, then `and N more`:

```text
config: 5 settings are wrong
  db.url: missing; set APP_DB_URL, APP_DB_URL_FILE or "db.url" in app.yaml
  timeout: expected a duration such as 5s or 1m30s (env APP_TIMEOUT)
  db.pool_max: expected an integer between 1 and 100 (file app.yaml, line 12)
  peers[1]: expected a host:port (flag -peers)
  APP_DB_UrL: unknown setting (env); did you mean APP_DB_URL?
```

No message of the module contains a setting's value, secret or not: a password pasted into `APP_DB_POOL_MAX` is `expected an integer`, with the key and the origin to look at.

### `config.Problem`

```go
type Problem struct{ Key, Origin, Message string }
```

Problem is one wrong setting. `Key` is the canonical key (`db.pool_max`), the variable or flag for an unknown setting, or the file name for a file that could not be read. `Origin` says where the wrong value came from (`env APP_TIMEOUT`, `file app.yaml, line 12`, `flag -peers`) and is empty for a missing setting. `Message` says what is wrong and never holds the value.
