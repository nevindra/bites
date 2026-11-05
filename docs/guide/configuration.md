# Configuration

**To read settings from flags, the environment and files you declare them as a struct and call `config.Load`, which returns the filled struct or one error that lists every setting that is wrong.**

**Reference:** [config](../reference/config.md) · **Design:** [principles](../design/principles.md)

Configuration is the module `github.com/nevindra/bites/modules/config` ([ADR 0080](../adr/0080-configuration-is-a-module-that-fills-a-typed-struct-from-flags-environment-and-files.md)), so an application that never imports it never links it, and its YAML parser. Install it with `go get github.com/nevindra/bites/modules/config`.

## Declaring the settings

**The struct says what a setting is, whether it may be missing and what it accepts.** A plain field is required, a [`bites.Opt[T]`](../reference/input.md#opt) may be absent, a struct nests, and a named type brings the `Length`, `Range`, `Values` and `Check` methods it has in an Input ([validation](./validation.md)), with the same sentences:

```go
type Config struct {
	Listen  string
	Timeout time.Duration
	DB      struct {
		URL      config.Secret
		PoolMax  PoolSize
		ReadOnly bool
	}
	Peers []string
	Trace bites.Opt[string]
}

func (Config) Defaults() map[string]string { return map[string]string{"Listen": ":8080", "Timeout": "5s"} }

type PoolSize int

func (PoolSize) Range() (min, max int64) { return 1, 100 }
```

The key of `DB.PoolMax` is `db.pool_max`. In a file it is `{"db": {"pool_max": 20}}`, in the environment it is `APP_DB_POOL_MAX` under the prefix `APP`, and as a flag it is `-db-pool-max`. A name no rule produces is given with a `WireNames` method, as in an Input, and all three forms follow.

## Loading them

**`Load` reads only what it is given: it looks at no environment variable, no `os.Args` and no path.** Pass the process in `main`:

```go
cfg, err := config.Load[Config](
	config.File(os.DirFS("/etc/app"), "app.yaml"),
	config.Env("APP", os.Environ(), "APP_CONFIG"),
	config.Flags(os.Args[1:]),
	config.SecretFiles(os.DirFS("/")),
	config.Logger(logger),
)
if err != nil {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
```

A setting comes from the first origin that has it: **flag, then environment, then file, then default.** The order is fixed, it does not depend on the order of the options, and a setting present in several origins is not an error: that is how a deployment overrides a file. A later `File` wins over an earlier one, so a base file comes first and a local one after it. A file that does not exist is an error naming it, unless it is given with `OptionalFile`. `-h` and `-help` do not exit: `Load` returns an error for which `errors.Is(err, flag.ErrHelp)` holds and whose text is the usage, and `main` decides what to do with it.

There is no `MustLoad`, because a library that exits takes the exit code, the stream and the format from `main`. The value is returned and nothing is registered anywhere: `main` builds the pool, the key and the clients from it and gives them to the handlers' struct and the [Providers](./providers.md) by closure.

## When settings are wrong

**Every wrong setting is listed at once**, so a deployment is fixed in one pass and not over five restarts:

```text
config: 3 settings are wrong
  db.url: missing; set APP_DB_URL, APP_DB_URL_FILE or "db.url" in app.yaml
  timeout: expected a duration such as 5s or 1m30s (env APP_TIMEOUT)
  APP_DB_UrL: unknown setting (env); did you mean APP_DB_URL?
```

No message contains a setting's value, because a password pasted into the wrong variable would otherwise reach the log. The message names the key, the origin and the rule. `errors.As(err, &ce)` with a `*config.Error` gives the same list as `Problems`, each with its `Key`, `Origin` and `Message`.

**A name that no setting has is refused**: a key in a file, a variable under the prefix and a flag. Without that, a misspelled `APP_DB_UrL` leaves the default in force and looks like a setting that worked. The application may itself read a variable under the prefix (`APP_CONFIG` for the path of its file): name it among the non-settings of `Env`, as in the example above, and it is skipped.

**On Kubernetes, a prefix equal to a Service's name meets variables you did not choose.** For every Service in the namespace the cluster injects `<SERVICE>_SERVICE_HOST`, `<SERVICE>_PORT` and `<SERVICE>_PORT_<n>_TCP...`, so a prefix `APP` and a Service named `app` give `APP_PORT=tcp://10.0.0.1:80`, which no setting maps to. The message says so; the fix is `enableServiceLinks: false` on the pod, or listing the variable among the non-settings of `Env`.

## Secrets

**A `config.Secret` prints `[redacted]` in `Println`, `%+v`, `log/slog` and a JSON encoding of the struct, and `Reveal()` is the only way to its text.** It has no flag, because the command line is visible in `ps` and `/proc`; a flag for one is an error that names the other ways. Set it in the environment, in a file, or with `APP_DB_URL_FILE=/run/secrets/db_url`, a file whose content (one trailing newline removed) is the value, which is how Kubernetes and Docker mount a secret. `_FILE` exists for a Secret and for nothing else. The files are read through the `fs.FS` of `SecretFiles`, `os.DirFS("/")` in a real deployment.

## Lists, maps and files

**A list is an array in a file and one comma-separated value in the environment and in a flag** (`APP_PEERS=a:1,b:2`, `-peers a:1,b:2`), trimmed around the commas, so an element cannot contain a comma there. Its default is written the same way, `"Peers": "a:1,b:2"` in `Defaults`, and an origin that sets the list replaces the whole default. **A map and a list of structs are read from a file only**, and so is their default. The file is JSON or YAML by its extension; a scalar is the text written, so `no` is the string `no` and never the false of YAML 1.1, a duplicate key and a `null` are errors, and a YAML tag, anchor or merge key is refused because it is a second way to write a value.

## Testing

**A test passes the origins it wants as values**, so it needs neither `t.Setenv` (global, and refused by `t.Parallel`) nor the disk:

```go
cfg, err := config.Load[Config](
	config.Env("APP", []string{"APP_TIMEOUT=2s"}),
	config.Flags([]string{"-listen=:0"}),
	config.File(fstest.MapFS{"app.yaml": {Data: []byte("listen: :9\n")}}, "app.yaml"),
)
```

A test of the application builds the `Config` literal and passes it to the constructors `main` uses. A deploy-time test, `Load` with the production file and a fake environment, catches a missing setting in CI.

## What it does not do

**There is no reload.** `Load` reads once, at start-up, because a setting changed at run time cannot reach a pool already opened, a listener already bound or a limit fixed at Registration, and a reload that applies half a file is a configuration nobody wrote. A change is a restart. A file mounted from a Kubernetes ConfigMap changes on disk and the running process keeps its old values until it restarts, as it does for a changed environment variable. There is no `${VAR}` interpolation, no `.env` reader (read it yourself and pass the lines to `Env`), no remote configuration centre and no configurable order of the origins.
