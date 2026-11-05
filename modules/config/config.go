// Package config fills a typed struct from flags, the environment and files, and refuses every
// wrong setting at start-up (ADR 0080).
//
// The application declares its settings as a struct and calls Load[T] with the origins it wants
// to read: File for a JSON or YAML file, Env for the environment, Flags for the command line.
// Load reads no environment variable, no os.Args and no path by itself: the application passes
// os.Environ(), os.Args[1:] and an fs.FS, so a test passes the values it wants and needs neither
// t.Setenv nor the disk. A setting is read from the first origin that has it, in a fixed order:
// flag, then environment, then file, then Defaults.
//
// A plain field is required, a bites.Opt[T] may be absent, and a struct field nests. The key of a
// field is its Go name in snake case joined with dots (db.pool_max), its environment variable is
// the prefix and the key in capitals (APP_DB_POOL_MAX), and its flag is the key with dashes
// (-db-pool-max). Load returns a *Error that lists every setting that is wrong at once; no
// message of this module contains a setting's value.
//
// The module depends on go.yaml.in/yaml/v3 for YAML files; an application that never imports it
// links none of it.
package config

import (
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"reflect"
	"runtime"
	"strings"
)

// Option says one origin, or the logger, to Load. An Option is made by File, OptionalFile, Env,
// Flags, SecretFiles or Logger. The order of the options does not decide which origin wins.
type Option interface{ apply(*loadOptions) }

type originRef struct {
	kind byte // 'f' file, 'e' env, 'a' flags
	idx  int
}

type loadOptions struct {
	files    []fileOpt
	env      []envOpt
	flags    []flagsOpt
	secretFS []secretOpt
	logger   *slog.Logger
	order    []originRef
}

type fileOpt struct {
	fsys     fs.FS
	name     string
	optional bool
}

func (o fileOpt) apply(l *loadOptions) {
	l.files = append(l.files, o)
	l.order = append(l.order, originRef{'f', len(l.files) - 1})
}

// File reads the settings in the file name of fsys, which must end in .json, .yaml or .yml. It
// may be given more than once: a later file wins over an earlier one, so a base file comes
// first and a local one after it. A file that does not exist or cannot be read is an error that
// names it (see OptionalFile). It panics for a nil fsys and for another extension, naming the
// line of the call.
func File(fsys fs.FS, name string) Option { return newFile(fsys, name, false, "File") }

// OptionalFile is File for a file that may not exist. A file that exists and is wrong is still an
// error; only a missing file is skipped, and Load logs nothing about it.
func OptionalFile(fsys fs.FS, name string) Option { return newFile(fsys, name, true, "OptionalFile") }

func newFile(fsys fs.FS, name string, optional bool, fn string) Option {
	if fsys == nil {
		panic(fmt.Sprintf("config.%s: the fs.FS is nil; pass os.DirFS(dir) or an fstest.MapFS\n\tcalled at %s", fn, caller(2)))
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".json", ".yaml", ".yml":
	default:
		panic(fmt.Sprintf("config.%s: the file %q does not end in .json, .yaml or .yml, the two formats read\n\tcalled at %s", fn, name, caller(2)))
	}
	return fileOpt{fsys, name, optional}
}

type envOpt struct {
	prefix  string
	environ []string
	skip    []string
	site    string
}

func (o envOpt) apply(l *loadOptions) {
	l.env = append(l.env, o)
	l.order = append(l.order, originRef{'e', len(l.env) - 1})
}

// Env reads the settings from environ, in the form os.Environ() returns it ("NAME=value"), for the
// variables that start with prefix and an underscore: with the prefix "APP", the key db.pool_max
// is APP_DB_POOL_MAX. The prefix must not be empty, because an unprefixed Env would read PATH and
// HOME; it panics, naming the line of the call, when it is.
//
// A variable under the prefix that no setting maps to is an error, because a misspelled name would
// otherwise leave the default in force and look like a setting that worked. The names after
// environ are variables the application says are not settings (APP_CONFIG for the path of its
// file); they are skipped, in the one place a reviewer greps. A Secret may also be set by
// the variable of its name with the suffix _FILE, a file read through SecretFiles.
func Env(prefix string, environ []string, notSettings ...string) Option {
	if prefix == "" {
		panic(fmt.Sprintf("config.Env: the prefix is empty, which would read PATH and HOME; give the application's name, such as \"APP\"\n\tcalled at %s", caller(1)))
	}
	return envOpt{prefix, environ, notSettings, caller(1)}
}

type flagsOpt struct {
	args []string
	site string
}

func (o flagsOpt) apply(l *loadOptions) {
	l.flags = append(l.flags, o)
	l.order = append(l.order, originRef{'a', len(l.flags) - 1})
}

// Flags reads the settings from the command line arguments, os.Args[1:], as -db-pool-max=20,
// --db-pool-max 20 or, for a bool, -read-only. A flag nobody defined, a flag given twice and an
// argument that is not a flag are errors. A Secret has no flag. -h and -help make Load return an
// error for which errors.Is(err, flag.ErrHelp) holds and whose text is the usage.
func Flags(args []string) Option { return flagsOpt{args, caller(1)} }

type secretOpt struct {
	fsys fs.FS
	site string
}

func (o secretOpt) apply(l *loadOptions) { l.secretFS = append(l.secretFS, o) }

// SecretFiles gives the fs.FS from which the files named by *_FILE variables are read: for a real
// deployment os.DirFS("/"), so that APP_DB_URL_FILE=/run/secrets/db_url reads /run/secrets/db_url
// (a leading slash is removed, because an fs.FS has none). Without it a _FILE variable is an
// error that says so. It is a separate option from File because the directory of the
// configuration files (/etc/app) is not where a platform mounts secrets.
func SecretFiles(fsys fs.FS) Option {
	if fsys == nil {
		panic(fmt.Sprintf("config.SecretFiles: the fs.FS is nil; pass os.DirFS(\"/\") or an fstest.MapFS\n\tcalled at %s", caller(1)))
	}
	return secretOpt{fsys, caller(1)}
}

type loggerOpt struct{ l *slog.Logger }

func (o loggerOpt) apply(l *loadOptions) { l.logger = o.l }

// Logger makes Load, when it succeeds, write one record per setting to l, in the form
// "config: setting key=db.pool_max value=20 origin=\"file app.yaml, line 12\"". The origin is
// "default", "absent" for an Opt nothing set, "file <name>, line <n>", "env <NAME>" or "flag
// -<name>", and the value of a Secret is "[redacted]". Without Logger nothing is logged: the
// module has no global logger to take it from.
func Logger(l *slog.Logger) Option { return loggerOpt{l} }

func caller(skip int) string {
	_, file, line, _ := runtime.Caller(skip + 1)
	return fmt.Sprintf("%s:%d", file, line)
}

// Problem is one wrong setting. Key is the canonical key (db.pool_max), the variable name or flag
// for an unknown setting, or the file name for a file that could not be read. Origin says where
// the wrong value came from ("env APP_TIMEOUT", "file app.yaml, line 12", "flag -peers"), and is
// empty for a missing setting. Message says what is wrong and never holds the value.
type Problem struct {
	Key     string
	Origin  string
	Message string

	unknown bool
}

func (p Problem) line() string {
	var b strings.Builder
	b.WriteString("  ")
	if p.Key != "" {
		b.WriteString(p.Key)
		b.WriteString(": ")
	}
	msg, tail := p.Message, ""
	if p.unknown {
		if head, rest, ok := strings.Cut(msg, "; "); ok {
			msg, tail = head, "; "+rest
		}
	}
	b.WriteString(msg)
	if p.Origin != "" {
		b.WriteString(" (" + p.Origin + ")")
	}
	b.WriteString(tail)
	return b.String()
}

// Error is what Load returns when settings are wrong: every wrong setting, in field order, then
// the unknown ones in the order their origins were given. Find it with errors.As.
type Error struct {
	// Problems holds every wrong setting. Its text shows at most 20 and ends with "and N more".
	Problems []Problem
}

const maxShown = 20

// Error lists the problems, one a line, after "config: N settings are wrong".
func (e *Error) Error() string {
	n := len(e.Problems)
	var b strings.Builder
	if n == 1 {
		b.WriteString("config: 1 setting is wrong")
	} else {
		fmt.Fprintf(&b, "config: %d settings are wrong", n)
	}
	for i, p := range e.Problems {
		if i == maxShown {
			fmt.Fprintf(&b, "\n  and %d more", n-maxShown)
			break
		}
		b.WriteString("\n" + p.line())
	}
	return b.String()
}

// helpError is what -h returns: the usage, which errors.Is finds as flag.ErrHelp.
type helpError struct{ usage string }

func (e *helpError) Error() string        { return e.usage }
func (e *helpError) Is(target error) bool { return target == flag.ErrHelp }

// Load returns a T filled from the origins it is given, or an error: a *Error that lists every
// wrong setting, or, for -h, an error for which errors.Is(err, flag.ErrHelp) holds and whose
// text is the usage. Load prints nothing and never exits.
//
// T must be a struct. A mistake in T or in the options is a panic before any origin is read,
// naming the field and the line of the call; a mistake in the deployment (a missing file, a
// variable that does not parse) is the returned error. See the package documentation for the
// rules of naming, types, presence and Defaults. A struct may declare Defaults() map[string]string
// by Go field name, WireNames() map[string]string and Descriptions() map[string]string; the root
// may declare Validate() error, which runs once when every setting decoded, and may return
// bites.Invalid("db.pool_max", "must not exceed db.max_conns"), listed with the others.
func Load[T any](opts ...Option) (T, error) {
	var out T
	t := reflect.TypeFor[T]()
	site := caller(1)
	lo := &loadOptions{}
	for _, o := range opts {
		if o == nil {
			panic(fmt.Sprintf("config.Load[%s]: an option is nil\n\tcalled at %s", t.Name(), site))
		}
		o.apply(lo)
	}
	c := &compiler{root: t.Name(), site: site}
	if t.Kind() != reflect.Struct {
		c.fail("the type is a %s; settings are a struct", t.Kind())
	}
	for _, d := range []struct {
		name string
		n    int
	}{{"Env", len(lo.env)}, {"Flags", len(lo.flags)}, {"SecretFiles", len(lo.secretFS)}, {"Logger", boolInt(lo.logger != nil)}} {
		if d.n > 1 {
			c.fail("%s was given twice; give one %s", d.name, d.name)
		}
	}
	if len(lo.env) == 1 {
		c.envP = lo.env[0].prefix
	}
	sc := c.compileStruct(t, "", "", nil)
	leaves := sc.leaves()
	c.checkKeys(leaves)
	if hasMethod(reflect.PointerTo(t), "Validate") {
		m, _ := reflect.PointerTo(t).MethodByName("Validate")
		if m.Type.NumIn() != 1 || m.Type.NumOut() != 1 || m.Type.Out(0) != reflect.TypeFor[error]() {
			c.fail("%s has a Validate method, but it must be func() error: rename the method if it means something else", t.Name())
		}
	}

	ld := &loader{c: c, lo: lo, sc: sc, leaves: leaves, root: reflect.ValueOf(&out).Elem(),
		origins: map[string]string{}, texts: map[string]string{}}
	if err := ld.run(); err != nil {
		var zero T
		return zero, err
	}
	if hasMethod(reflect.PointerTo(t), "Validate") {
		ld.validate(&out)
		if len(ld.probs) > 0 {
			var zero T
			return zero, &Error{Problems: ld.probs}
		}
	}
	ld.log()
	return out, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// flagName is the flag of a key: db.pool_max is db-pool-max.
func flagName(key string) string {
	return strings.NewReplacer(".", "-", "_", "-").Replace(key)
}

// envName is the variable of a key under prefix: db.pool_max is APP_DB_POOL_MAX.
func envName(prefix, key string) string {
	return prefix + "_" + strings.ToUpper(strings.NewReplacer(".", "_").Replace(key))
}

// checkKeys refuses two settings that no origin could tell apart, and a key the flag set owns.
func (c *compiler) checkKeys(leaves []*field) {
	seen := map[string]*field{}
	for _, f := range leaves {
		fn := strings.ToLower(flagName(f.key))
		if fn == "h" || fn == "help" {
			c.fail("key %s collides with the flag -%s; rename the field with WireNames", f.key, fn)
		}
		if prev, ok := seen[fn]; ok {
			norm := strings.ReplaceAll(f.key, ".", "_")
			forms := "-" + fn
			if c.envP != "" {
				forms = envName(c.envP, f.key) + ", " + forms
			}
			c.fail("%s and %s are both %s (%s); name one with WireNames", prev.goPath, f.goPath, norm, forms)
		}
		seen[fn] = f
	}
}
