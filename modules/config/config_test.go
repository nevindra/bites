package config_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/config"
)

type PoolSize int

func (PoolSize) Range() (min, max int64) { return 1, 100 }

type DBConfig struct {
	URL      config.Secret
	PoolMax  PoolSize
	ReadOnly bool
}

type Config struct {
	Listen  string
	Timeout time.Duration
	DB      DBConfig
	Peers   []string
	Trace   bites.Opt[string]
}

func (Config) Defaults() map[string]string {
	return map[string]string{"Listen": ":8080", "Timeout": "5s"}
}

func (Config) Descriptions() map[string]string {
	return map[string]string{"Listen": "the address to serve on", "DB.PoolMax": "connections kept open"}
}

// baseEnv sets every required setting of Config.
var baseEnv = []string{"APP_DB_URL=postgres://u:p@h/d", "APP_DB_POOL_MAX=20", "APP_DB_READ_ONLY=false", "APP_PEERS=a:1,b:2"}

func env(extra ...string) config.Option {
	return config.Env("APP", append(append([]string{}, baseEnv...), extra...))
}

func files(m map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for k, v := range m {
		fsys[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return fsys
}

func mustLoad[T any](t *testing.T, opts ...config.Option) T {
	t.Helper()
	v, err := config.Load[T](opts...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return v
}

func loadErr[T any](t *testing.T, opts ...config.Option) *config.Error {
	t.Helper()
	_, err := config.Load[T](opts...)
	var ce *config.Error
	if !errors.As(err, &ce) {
		t.Fatalf("Load error = %v, want a *config.Error", err)
	}
	return ce
}

func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Fatalf("no panic, want one containing %q", want)
		}
		s := fmt.Sprint(r)
		if !strings.Contains(s, want) {
			t.Fatalf("panic = %q\nwant it to contain %q", s, want)
		}
		if !strings.Contains(s, "called at ") || !strings.Contains(s, "config_test.go:") {
			t.Fatalf("panic = %q does not name where Load was called", s)
		}
	}()
	f()
}

func has(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("got:\n%s\nwant it to contain %q", got, want)
	}
}

func TestAFlagBeatsTheEnvironmentWhichBeatsAFileWhichBeatsADefault(t *testing.T) {
	file := files(map[string]string{"app.yaml": "listen: ':1'\ntimeout: 1s\ndb:\n  pool_max: 3\n"})
	opts := func(args ...string) []config.Option {
		return []config.Option{
			config.File(file, "app.yaml"),
			env("APP_LISTEN=:2", "APP_TIMEOUT=2s", "APP_DB_POOL_MAX=20"),
			config.Flags(args),
		}
	}
	c := mustLoad[Config](t, opts("-listen=:3")...)
	if c.Listen != ":3" || c.Timeout != 2*time.Second || c.DB.PoolMax != 20 {
		t.Fatalf("flag > env > file: %+v", c)
	}
	c = mustLoad[Config](t, config.File(file, "app.yaml"), config.Env("APP", append([]string{"APP_DB_URL=x", "APP_DB_READ_ONLY=0", "APP_PEERS=a"}, "APP_TIMEOUT=2s")))
	if c.Listen != ":1" || c.Timeout != 2*time.Second || c.DB.PoolMax != 3 {
		t.Fatalf("env > file: %+v", c)
	}
	c = mustLoad[Config](t, env())
	if c.Listen != ":8080" || c.Timeout != 5*time.Second {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestTheOrderOfOriginsDoesNotDependOnTheOrderOfTheOptions(t *testing.T) {
	file := files(map[string]string{"app.json": `{"listen": ":1"}`})
	a := mustLoad[Config](t, config.File(file, "app.json"), env("APP_LISTEN=:2"), config.Flags([]string{"-listen=:3"}))
	b := mustLoad[Config](t, config.Flags([]string{"-listen=:3"}), env("APP_LISTEN=:2"), config.File(file, "app.json"))
	if a.Listen != ":3" || b.Listen != ":3" {
		t.Fatalf("a = %q, b = %q", a.Listen, b.Listen)
	}
}

func TestALaterFileBeatsAnEarlierOne(t *testing.T) {
	fsys := files(map[string]string{"base.yaml": "listen: ':1'\ntimeout: 1s\n", "local.yaml": "listen: ':2'\n"})
	c := mustLoad[Config](t, config.File(fsys, "base.yaml"), config.File(fsys, "local.yaml"), env())
	if c.Listen != ":2" || c.Timeout != time.Second {
		t.Fatalf("%+v", c)
	}
}

func TestAMissingFileIsAnErrorNamingItAndAnOptionalFileIsNot(t *testing.T) {
	fsys := files(map[string]string{})
	e := loadErr[Config](t, config.File(fsys, "app.yaml"), env())
	if len(e.Problems) != 1 || e.Problems[0].Key != "app.yaml" || e.Problems[0].Message != "the file does not exist" {
		t.Fatalf("%v", e)
	}
	mustLoad[Config](t, config.OptionalFile(fsys, "app.yaml"), env())
	bad := files(map[string]string{"app.yaml": "listen: [\n"})
	if _, err := config.Load[Config](config.OptionalFile(bad, "app.yaml"), env()); err == nil {
		t.Fatal("an optional file that exists and is wrong must still be an error")
	}
}

type Naming struct {
	DBUrl   string
	HTTPApi struct{ ReadTimeoutMS int }
}

func TestAFieldNameBecomesAFileKeyAnEnvironmentVariableAndAFlagByTheNamingPolicy(t *testing.T) {
	e := loadErr[Naming](t, config.Env("APP", nil), config.Flags(nil), config.File(files(map[string]string{"a.json": "{}"}), "a.json"))
	got := e.Error()
	has(t, got, `db_url: missing; set -db-url, APP_DB_URL or "db_url" in a.json`)
	has(t, got, `http_api.read_timeout_ms: missing; set -http-api-read-timeout-ms, APP_HTTP_API_READ_TIMEOUT_MS or "http_api.read_timeout_ms" in a.json`)
	c := mustLoad[Naming](t, config.Env("APP", []string{"APP_DB_URL=x", "APP_HTTP_API_READ_TIMEOUT_MS=5"}))
	if c.DBUrl != "x" || c.HTTPApi.ReadTimeoutMS != 5 {
		t.Fatalf("%+v", c)
	}
}

type Worded struct {
	Name string
	Sub  struct{ Nick string }
}

func (Worded) WireNames() map[string]string { return map[string]string{"Name": "label"} }

func TestWireNamesChangesTheWordInAllThreeForms(t *testing.T) {
	file := files(map[string]string{"a.yaml": "label: f\nsub:\n  nick: n\n"})
	c := mustLoad[Worded](t, config.File(file, "a.yaml"))
	if c.Name != "f" {
		t.Fatalf("file: %+v", c)
	}
	c = mustLoad[Worded](t, config.Env("APP", []string{"APP_LABEL=e", "APP_SUB_NICK=n"}))
	if c.Name != "e" {
		t.Fatalf("env: %+v", c)
	}
	c = mustLoad[Worded](t, config.Flags([]string{"-label=g", "-sub-nick=n"}))
	if c.Name != "g" {
		t.Fatalf("flag: %+v", c)
	}
	e := loadErr[Worded](t, config.Env("APP", []string{"APP_NAME=x"}))
	has(t, e.Error(), "APP_NAME: unknown setting")
	type Typo struct{ A string }
	mustPanic(t, "WireNames names B", func() { config.Load[badWire]() })
}

type badWire struct{ A string }

func (badWire) WireNames() map[string]string { return map[string]string{"B": "x"} }

type twoKeys struct {
	DBUrl string
	DB    struct{ URL string }
}

func TestTwoFieldsWithOneKeyArePanicsAtLoadNamingBoth(t *testing.T) {
	mustPanic(t, "config.Load[twoKeys]: DBUrl and DB.URL are both db_url (APP_DB_URL, -db-url); name one with WireNames", func() {
		config.Load[twoKeys](config.Env("APP", nil))
	})
}

type Scalars struct {
	D  time.Duration
	N  int
	U  uint8
	F  float64
	B  bool
	At time.Time
	On bites.Date
}

func TestADurationAnIntegerAndABoolDecodeFromTextAsAQueryParameterDoes(t *testing.T) {
	c := mustLoad[Scalars](t, config.Env("APP", []string{"APP_D=1m30s", "APP_N=-4", "APP_U=200", "APP_F=1.5", "APP_B=1", "APP_AT=2026-01-02T03:04:05Z", "APP_ON=2026-02-03"}))
	if c.D != 90*time.Second || c.N != -4 || c.U != 200 || c.F != 1.5 || !c.B || c.At.Year() != 2026 || c.On.Day() != 3 {
		t.Fatalf("%+v", c)
	}
	e := loadErr[Scalars](t, config.Env("APP", []string{"APP_D=x", "APP_N=1.5", "APP_U=-1", "APP_F=abc", "APP_B=yes", "APP_AT=2026-01-02", "APP_ON=02/03/2026"}))
	for _, want := range []string{
		"d: expected a duration such as 5s or 1m30s (env APP_D)",
		"n: expected an integer (env APP_N)",
		"u: expected a non-negative integer (env APP_U)",
		"f: expected a number (env APP_F)",
		"b: expected true or false (env APP_B)",
		"at: expected an RFC 3339 time with an offset (env APP_AT)",
		"on: expected a date as YYYY-MM-DD (env APP_ON)",
	} {
		has(t, e.Error(), want)
	}
}

func TestADurationWithoutAUnitIsRefusedNamingTheKey(t *testing.T) {
	e := loadErr[Config](t, env("APP_TIMEOUT=30"))
	has(t, e.Error(), "timeout: expected a duration such as 5s or 1m30s (env APP_TIMEOUT)")
	if strings.Contains(e.Error(), "30") && strings.Contains(strings.ReplaceAll(e.Error(), "1m30s", ""), "30") {
		t.Fatalf("the value is in the message: %v", e)
	}
}

type YAMLWords struct {
	Country string
	Flag    bool
	Port    string
}

func TestAYAMLScalarIsTheTextWrittenSoNoIsNotFalse(t *testing.T) {
	c := mustLoad[struct{ Country, Port string }](t, config.File(files(map[string]string{"a.yaml": "country: no\nport: 0o17\n"}), "a.yaml"))
	if c.Country != "no" || c.Port != "0o17" {
		t.Fatalf("%+v", c)
	}
	e := loadErr[struct{ Flag bool }](t, config.File(files(map[string]string{"a.yml": "flag: yes\n"}), "a.yml"))
	has(t, e.Error(), "flag: expected true or false (file a.yml, line 1)")
}

func TestAJSONNumberIsTheDigitsWritten(t *testing.T) {
	c := mustLoad[struct{ Port string }](t, config.File(files(map[string]string{"a.json": `{"port": 1e3}`}), "a.json"))
	if c.Port != "1e3" {
		t.Fatalf("%+v", c)
	}
	e := loadErr[struct{ Port int }](t, config.File(files(map[string]string{"a.json": `{"port": 1e3}`}), "a.json"))
	has(t, e.Error(), "port: expected an integer (file a.json, line 1)")
	c2 := mustLoad[struct{ Port int }](t, config.File(files(map[string]string{"a.json": `{"port": 8080}`}), "a.json"))
	if c2.Port != 8080 {
		t.Fatal(c2)
	}
}

func TestADuplicateKeyInAFileIsAnErrorNamingBothLines(t *testing.T) {
	for name, src := range map[string]string{
		"a.yaml": "port: 1\nname: x\nport: 2\n",
		"a.json": "{\"port\": 1,\n\"name\": \"x\",\n\"port\": 2}",
	} {
		e := loadErr[struct{ Port int }](t, config.File(files(map[string]string{name: src}), name))
		has(t, e.Error(), "port: duplicate key; first written at line 1 (file "+name+", line 3)")
	}
}

func TestNullInAFileIsRefused(t *testing.T) {
	for name, src := range map[string]string{"a.yaml": "port:\nname: x\n", "a.json": "{\"port\": null,\n\"name\": \"x\"}"} {
		e := loadErr[struct{ Port int }](t, config.File(files(map[string]string{name: src}), name))
		if len(e.Problems) < 1 {
			t.Fatal("no problem")
		}
		has(t, e.Error(), "port: null is not a value, remove the key (file "+name+", line 1)")
	}
}

func TestAnAnchorATagAndAMergeKeyInYAMLAreRefusedWithTheLine(t *testing.T) {
	src := "base: &b\n  x: 1\nport: !!int 5\nother:\n  <<: *b\n"
	e := loadErr[struct{ Port, Other string }](t, config.File(files(map[string]string{"a.yaml": src}), "a.yaml"))
	has(t, e.Error(), "a tag, an anchor or a merge key is a second way to write a value; write the value (file a.yaml, line 1)")
	has(t, e.Error(), "port: a tag, an anchor or a merge key")
	has(t, e.Error(), "(file a.yaml, line 3)")
	has(t, e.Error(), "other.<<: a tag, an anchor or a merge key")
	has(t, e.Error(), "(file a.yaml, line 5)")
}

func TestAPlainFieldIsRequiredAndTheErrorNamesEveryWayToSetIt(t *testing.T) {
	e := loadErr[Config](t, config.Env("APP", nil), config.Flags(nil), config.File(files(map[string]string{"app.yaml": "{}"}), "app.yaml"))
	has(t, e.Error(), `db.url: missing; set APP_DB_URL, APP_DB_URL_FILE or "db.url" in app.yaml`)
	has(t, e.Error(), `db.pool_max: missing; set -db-pool-max, APP_DB_POOL_MAX or "db.pool_max" in app.yaml`)
	e = loadErr[Config](t)
	has(t, e.Error(), "db.url: missing; no origin was given to Load, and it has no default")
}

func TestAnOptFieldMayBeAbsentAndSaysSoThroughGetAndOr(t *testing.T) {
	c := mustLoad[Config](t, env())
	if _, ok := c.Trace.Get(); ok || c.Trace.Or("off") != "off" {
		t.Fatalf("absent: %v", c.Trace)
	}
	c = mustLoad[Config](t, env("APP_TRACE="))
	if v, ok := c.Trace.Get(); !ok || v != "" {
		t.Fatalf("an empty variable is present: %v", c.Trace)
	}
	c = mustLoad[Config](t, env("APP_TRACE=full"))
	if c.Trace.Or("off") != "full" {
		t.Fatal(c.Trace)
	}
}

type withDefaults struct {
	Pool PoolSize
	Name string
	Opt  struct{ Inner string }
}

func (withDefaults) Defaults() map[string]string {
	return map[string]string{"Pool": "7", "Name": "x", "Opt.Inner": "outer"}
}

type inner struct{ Inner string }

func (inner) Defaults() map[string]string { return map[string]string{"Inner": "own"} }

type nestedDefaults struct{ In inner }
type nestedWins struct{ In inner }

func (nestedWins) Defaults() map[string]string { return map[string]string{"In.Inner": "outer"} }

func TestDefaultsFillWhatNoOriginSetAndAreCheckedByTheTypesRules(t *testing.T) {
	c := mustLoad[withDefaults](t)
	if c.Pool != 7 || c.Name != "x" || c.Opt.Inner != "outer" {
		t.Fatalf("%+v", c)
	}
	if got := mustLoad[nestedDefaults](t); got.In.Inner != "own" {
		t.Fatalf("a struct states its own defaults: %+v", got)
	}
	if got := mustLoad[nestedWins](t); got.In.Inner != "outer" {
		t.Fatalf("the outer entry wins: %+v", got)
	}
	mustPanic(t, `the default of Pool, "500", is refused: expected an integer between 1 and 100`, func() { config.Load[badRangeDefault]() })
}

type badRangeDefault struct{ Pool PoolSize }

func (badRangeDefault) Defaults() map[string]string { return map[string]string{"Pool": "500"} }

func TestAValueSentBadlyIsAnErrorAndNeverFallsBackToTheDefault(t *testing.T) {
	e := loadErr[Config](t, env("APP_TIMEOUT=abc"))
	has(t, e.Error(), "timeout: expected a duration such as 5s or 1m30s (env APP_TIMEOUT)")
}

type badDuration struct{ Timeout time.Duration }

func (badDuration) Defaults() map[string]string { return map[string]string{"Timeout": "5 seconds"} }

type typoDefault struct{ Timeout time.Duration }

func (typoDefault) Defaults() map[string]string { return map[string]string{"Timout": "5s"} }

type optDefault struct{ A bites.Opt[string] }

func (optDefault) Defaults() map[string]string { return map[string]string{"A": "x"} }

type listDefault struct {
	Origins []string
	Ports   []int
}

func (listDefault) Defaults() map[string]string {
	return map[string]string{"Origins": "https://a.example, https://b.example", "Ports": "80,443"}
}

type badListDefault struct{ Ports []int }

func (badListDefault) Defaults() map[string]string { return map[string]string{"Ports": "80,http"} }

type mapDefault struct{ Tags map[string]string }

func (mapDefault) Defaults() map[string]string { return map[string]string{"Tags": "a=b"} }

func TestAListDefaultIsTheCommaSeparatedTextTheEnvironmentTakesAndTheEnvironmentReplacesIt(t *testing.T) {
	c, err := config.Load[listDefault](config.Env("APP", nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Origins, " ") != "https://a.example https://b.example" || len(c.Ports) != 2 || c.Ports[1] != 443 {
		t.Fatalf("the defaults filled %q and %v", c.Origins, c.Ports)
	}
	c, err = config.Load[listDefault](config.Env("APP", []string{"APP_ORIGINS=https://c.example"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Origins, " ") != "https://c.example" || len(c.Ports) != 2 {
		t.Fatalf("the environment did not replace the default list: %q %v", c.Origins, c.Ports)
	}
}

func TestADefaultItsTypeRefusesIsAPanicNamingTheFieldAndTheText(t *testing.T) {
	mustPanic(t, `config.Load[badDuration]: the default of Timeout, "5 seconds", is not a duration: expected a duration such as 5s or 1m30s`, func() { config.Load[badDuration]() })
	mustPanic(t, "config.Load[typoDefault]: Defaults names Timout, which is not a field of typoDefault; did you mean Timeout?", func() { config.Load[typoDefault]() })
	mustPanic(t, "Defaults names A, which is an Opt", func() { config.Load[optDefault]() })
	mustPanic(t, `config.Load[badListDefault]: the default of Ports, "80,http", has element 1, "http", which is refused: expected an integer`, func() { config.Load[badListDefault]() })
	mustPanic(t, "config.Load[mapDefault]: Defaults names Tags, which is a map or a list of structs; those are read from a file only, so write its default in a file", func() { config.Load[mapDefault]() })
}

type Name3 string

func (Name3) Length() (min, max int) { return 3, 8 }

type Mode string

func (Mode) Values() []Mode { return []Mode{"fast", "slow"} }

type Even int

func (e Even) Check() error {
	if e%2 != 0 {
		return errors.New("must be even")
	}
	return nil
}

type Ratio float64

func (Ratio) Range() (min, max float64) { return 0.5, 2 }

type Rules struct {
	Name  Name3
	Mode  Mode
	N     Even
	R     Ratio
	Pool  PoolSize
	Floor PoolFloor
}

type PoolFloor int

func (PoolFloor) Range() (min, max int64) { return 10, 1<<63 - 1 }

func TestLengthRangeValuesAndCheckOfANamedTypeAreCheckedWithTheSentencesOfAnInput(t *testing.T) {
	good := []string{"APP_NAME=abc", "APP_MODE=fast", "APP_N=4", "APP_R=1", "APP_POOL=5", "APP_FLOOR=10"}
	mustLoad[Rules](t, config.Env("APP", good))
	e := loadErr[Rules](t, config.Env("APP", []string{"APP_NAME=ab", "APP_MODE=medium", "APP_N=3", "APP_R=3", "APP_POOL=0", "APP_FLOOR=9"}))
	for _, want := range []string{
		"name: must be between 3 and 8 characters (env APP_NAME)",
		"mode: expected one of: fast, slow (env APP_MODE)",
		"n: must be even (env APP_N)",
		"r: expected a number between 0.5 and 2 (env APP_R)",
		"pool: expected an integer between 1 and 100 (env APP_POOL)",
		"floor: expected an integer of at least 10 (env APP_FLOOR)",
	} {
		has(t, e.Error(), want)
	}
}

type queryRules struct {
	Query struct {
		Name  Name3
		Mode  Mode
		R     Ratio
		Pool  PoolSize
		Floor PoolFloor
		N     int
		D     bool
		Count uint8
		F     float64
	}
}

func TestTheSentencesOfLengthAndRangeEqualTheOnesTheCoreAnswersInAQuery(t *testing.T) {
	app := bites.New()
	app.Get("/q", func(_ context.Context, in queryRules) (struct{}, error) { return struct{}{}, nil })
	srv := bitetest.Start(t, app)
	cases := []struct {
		param, value, env string
		cfg               func(env []string) error
	}{
		{"name", "ab", "APP_NAME", func(e []string) error { _, err := config.Load[Rules](config.Env("APP", e)); return err }},
		{"mode", "medium", "APP_MODE", nil},
		{"r", "3", "APP_R", nil},
		{"pool", "0", "APP_POOL", nil},
		{"floor", "9", "APP_FLOOR", nil},
	}
	for _, c := range cases {
		r := srv.Do(bitetest.Request{Method: "GET", Target: "/q?" + c.param + "=" + c.value})
		var body struct {
			Errors []struct{ Location, Message string }
		}
		if err := r.JSON(&body); err != nil || r.Status != 400 {
			t.Fatalf("%s: %d %s", c.param, r.Status, r.Body)
		}
		var core string
		for _, e := range body.Errors {
			if strings.HasSuffix(e.Location, "."+c.param) {
				core = e.Message
			}
		}
		_, err := config.Load[Rules](config.Env("APP", []string{c.env + "=" + c.value}))
		if core == "" || !strings.Contains(fmt.Sprint(err), c.param+": "+core+" (env "+c.env+")") {
			t.Errorf("%s: the core says %q, the module says:\n%v", c.param, core, err)
		}
	}
	for _, c := range []struct{ param, value, env string }{{"n", "1.5", "APP_N"}, {"count", "-1", "APP_COUNT"}, {"d", "maybe", "APP_D"}, {"f", "abc", "APP_F"}} {
		r := srv.Do(bitetest.Request{Method: "GET", Target: "/q?" + c.param + "=" + c.value})
		_ = r
	}
	for _, c := range []struct{ param, value, env, want string }{
		{"n", "1.5", "APP_N", "expected an integer"},
		{"count", "-1", "APP_COUNT", "expected a non-negative integer"},
		{"d", "maybe", "APP_D", "expected true or false"},
		{"f", "abc", "APP_F", "expected a number"},
	} {
		r := srv.Do(bitetest.Request{Method: "GET", Target: "/q?" + c.param + "=" + c.value})
		if !strings.Contains(string(r.Body), c.want) {
			t.Errorf("core %s: %s", c.param, r.Body)
		}
	}
	e := loadErr[struct {
		N     int
		Count uint8
		D     bool
		F     float64
	}](t, config.Env("APP", []string{"APP_N=1.5", "APP_COUNT=-1", "APP_D=maybe", "APP_F=abc"}))
	for _, want := range []string{"n: expected an integer", "count: expected a non-negative integer", "d: expected true or false", "f: expected a number"} {
		has(t, e.Error(), want)
	}
}

type withItems struct{ Peers peerList }
type peerList []string

func (peerList) Items() (min, max int) { return 1, 3 }

type fmtString string

func (fmtString) Format() string { return "email" }

type withFormat struct{ Mail fmtString }

type patString string

func (patString) Pattern() string { return "^a" }

type withPattern struct{ P patString }

func TestItemsFormatAndPatternInAConfigTypeAreRefusedAtLoad(t *testing.T) {
	mustPanic(t, "config.Load[withItems]: field Peers has an Items rule, which settings do not check; put the rule in a Check method", func() { config.Load[withItems]() })
	mustPanic(t, "field Mail has a Format rule, which settings do not check; put the rule in a Check method", func() { config.Load[withFormat]() })
	mustPanic(t, "field P has a Pattern rule, which settings do not check; put the rule in a Check method", func() { config.Load[withPattern]() })
}

type (
	uniqueList []string
	stepFloat  float64
	stepInt    int
	bagMap     map[string]string
)

func (uniqueList) Unique() bool         { return true }
func (stepFloat) ExclusiveMin() float64 { return 0 }
func (stepInt) MultipleOf() int64       { return 5 }
func (bagMap) Members() (min, max int)  { return 1, 2 }

type withUnique struct{ L uniqueList }
type withExclusive struct{ F stepFloat }
type withMultiple struct{ N stepInt }
type withMembers struct{ M bagMap }

func TestUniqueMembersMultipleOfAndExclusiveBoundsInAConfigTypeAreRefusedAtLoad(t *testing.T) {
	mustPanic(t, "field L has a Unique rule, which settings do not check; put the rule in a Check method", func() { config.Load[withUnique]() })
	mustPanic(t, "field F has an ExclusiveMin rule, which settings do not check; put the rule in a Check method", func() { config.Load[withExclusive]() })
	mustPanic(t, "field N has a MultipleOf rule, which settings do not check; put the rule in a Check method", func() { config.Load[withMultiple]() })
	mustPanic(t, "field M has a Members rule, which settings do not check; put the rule in a Check method", func() { config.Load[withMembers]() })
}

type validated struct {
	Pool, Max int
}

func (v validated) Validate() error {
	if v.Pool > v.Max {
		return errors.Join(bites.Invalid("pool", "must not exceed max"), bites.Invalid("max", "is below pool"))
	}
	return nil
}

func TestValidateOnTheRootRunsAfterDecodingAndItsInvalidIsListedWithTheOthers(t *testing.T) {
	e := loadErr[validated](t, config.Env("APP", []string{"APP_POOL=9", "APP_MAX=3"}))
	if len(e.Problems) != 2 || e.Problems[0].Key != "pool" || e.Problems[1].Message != "is below pool" {
		t.Fatalf("%v", e)
	}
	mustLoad[validated](t, config.Env("APP", []string{"APP_POOL=1", "APP_MAX=3"}))
	e = loadErr[validated](t, config.Env("APP", []string{"APP_POOL=x", "APP_MAX=3"}))
	if len(e.Problems) != 1 {
		t.Fatalf("Validate must not run while a setting is wrong: %v", e)
	}
}

type nestedValidate struct{ DB nestedV }
type nestedV struct{ X string }

func (nestedV) Validate() error { return nil }

func TestAValidateOnANestedStructIsRefusedAtLoad(t *testing.T) {
	mustPanic(t, "config.Load[nestedValidate]: field DB has a Validate method, which is never called; declare it on nestedValidate", func() { config.Load[nestedValidate]() })
}

func TestSeveralWrongSettingsAreListedAtOnceInFieldOrderEachNamingKeyAndOrigin(t *testing.T) {
	fsys := files(map[string]string{"app.yaml": "db:\n  pool_max: 500\n  oops: 1\n"})
	_, err := config.Load[Config](
		config.File(fsys, "app.yaml"),
		config.Env("APP", []string{"APP_TIMEOUT=abc", "APP_DB_UrL=x", "APP_PEERS=a"}),
		config.Flags([]string{"-nope=1"}),
	)
	want := `config: 6 settings are wrong
  timeout: expected a duration such as 5s or 1m30s (env APP_TIMEOUT)
  db.url: missing; set APP_DB_URL, APP_DB_URL_FILE or "db.url" in app.yaml
  db.pool_max: expected an integer between 1 and 100 (file app.yaml, line 2)
  db.read_only: missing; set -db-read-only, APP_DB_READ_ONLY or "db.read_only" in app.yaml
  db.oops: unknown setting (file app.yaml, line 3)
  APP_DB_UrL: unknown setting (env); did you mean APP_DB_URL?`
	got := fmt.Sprint(err)
	if !strings.HasPrefix(got, "config: 7 settings are wrong") {
		t.Fatalf("got:\n%s", got)
	}
	for _, l := range strings.Split(want, "\n")[1:] {
		has(t, got, l)
	}
	has(t, got, "-nope: unknown setting (flag)")
	iTime, iURL, iPool := strings.Index(got, "timeout:"), strings.Index(got, "db.url: missing"), strings.Index(got, "db.pool_max:")
	if !(iTime < iURL && iURL < iPool) || strings.Index(got, "db.oops") < iPool || strings.Index(got, "APP_DB_UrL") < strings.Index(got, "db.oops") || strings.Index(got, "-nope") < strings.Index(got, "APP_DB_UrL") {
		t.Fatalf("order:\n%s", got)
	}
}

func TestMoreThanTwentyProblemsAreCutWithAnAndNMoreLine(t *testing.T) {
	var vars []string
	for i := 0; i < 25; i++ {
		vars = append(vars, fmt.Sprintf("APP_ZZ%02d=1", i))
	}
	e := loadErr[struct{ A string }](t, config.Env("APP", vars))
	if len(e.Problems) != 26 {
		t.Fatalf("problems = %d", len(e.Problems))
	}
	lines := strings.Split(e.Error(), "\n")
	if lines[0] != "config: 26 settings are wrong" || len(lines) != 1+20+1 || lines[21] != "  and 6 more" {
		t.Fatalf("%d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

func TestTheErrorIsFoundWithErrorsAsAndListsProblemsWithKeyOriginAndMessage(t *testing.T) {
	_, err := config.Load[Config](env("APP_TIMEOUT=abc"))
	var ce *config.Error
	if !errors.As(fmt.Errorf("start: %w", err), &ce) {
		t.Fatal("errors.As")
	}
	p := ce.Problems[0]
	if p.Key != "timeout" || p.Origin != "env APP_TIMEOUT" || p.Message != "expected a duration such as 5s or 1m30s" {
		t.Fatalf("%+v", p)
	}
	one := ce.Error()
	if !strings.HasPrefix(one, "config: 1 setting is wrong\n  timeout:") {
		t.Fatal(one)
	}
}

func TestNoMessageContainsASettingsValue(t *testing.T) {
	const secret = "s3cr3t-value-xyz"
	fsys := files(map[string]string{"a.yaml": "n: " + secret + "\n"})
	_, err := config.Load[Rules](
		config.File(fsys, "a.yaml"),
		config.Env("APP", []string{"APP_NAME=" + secret, "APP_MODE=" + secret, "APP_N=" + secret, "APP_R=" + secret, "APP_POOL=" + secret, "APP_FLOOR=" + secret}),
		config.Flags([]string{"-pool=" + secret}),
	)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("a value is in the error: %v", err)
	}
}

func TestAPasswordPastedIntoANumbersVariableNeverReachesTheError(t *testing.T) {
	e := loadErr[Config](t, env("APP_DB_POOL_MAX=hunter2"))
	has(t, e.Error(), "db.pool_max: expected an integer (env APP_DB_POOL_MAX)")
	if strings.Contains(e.Error(), "hunter2") {
		t.Fatal(e)
	}
}

func TestAnUnknownKeyInAFileIsRefusedWithItsLineAndANearOneIsSuggested(t *testing.T) {
	fsys := files(map[string]string{"app.yaml": "listen: ':1'\ndb_ur: x\nzzz: 1\n"})
	e := loadErr[Config](t, config.File(fsys, "app.yaml"), env())
	has(t, e.Error(), "db_ur: unknown setting (file app.yaml, line 2)")
	e = loadErr[struct{ DB struct{ URL string } }](t, config.File(files(map[string]string{"a.json": `{"db": {"ur": "x", "url": "y"}}`}), "a.json"))
	has(t, e.Error(), "db.ur: unknown setting (file a.json, line 1); did you mean db.url?")
}

func TestAnUnknownVariableUnderThePrefixIsRefusedAndOneTheApplicationNamedIsNot(t *testing.T) {
	e := loadErr[Config](t, config.Env("APP", append(append([]string{}, baseEnv...), "APP_DB_UrL=x", "APP_CONFIG=/etc/app.yaml", "PATH=/bin", "OTHER_X=1")))
	if len(e.Problems) != 2 {
		t.Fatalf("%v", e)
	}
	has(t, e.Error(), "APP_DB_UrL: unknown setting (env); did you mean APP_DB_URL?")
	has(t, e.Error(), "APP_CONFIG: unknown setting (env)")
	mustLoad[Config](t, config.Env("APP", append(append([]string{}, baseEnv...), "APP_CONFIG=/etc/app.yaml", "APP_PORT=tcp://10.0.0.1:80"), "APP_CONFIG", "APP_PORT"))
}

func TestAKubernetesServiceLinkVariableIsRefusedNamingTheCauseAndTheTwoWaysOut(t *testing.T) {
	e := loadErr[Config](t, config.Env("APP", append(append([]string{}, baseEnv...),
		"APP_PORT=tcp://10.0.0.1:80", "APP_PORT_80_TCP_ADDR=10.0.0.1", "APP_SERVICE_HOST=10.0.0.1", "APP_SERVICE_PORT=80")))
	if len(e.Problems) != 4 {
		t.Fatalf("%v", e)
	}
	want := "APP_PORT: unknown setting (env); Kubernetes injects APP_SERVICE_HOST, APP_PORT and APP_PORT_<n>_TCP... for a Service named app: set enableServiceLinks: false on the pod, or list APP_PORT among the non-settings of Env"
	has(t, e.Error(), want)
	has(t, e.Error(), "APP_PORT_80_TCP_ADDR: unknown setting (env); Kubernetes injects")
	if strings.Contains(e.Error(), "10.0.0.1") {
		t.Fatal("a value is in the message")
	}
}

func TestAnUnknownFlagIsRefusedInTheSameListAsTheOthers(t *testing.T) {
	e := loadErr[Config](t, env("APP_TIMEOUT=abc"), config.Flags([]string{"-listn=:1", "-timeout=2s"}))
	has(t, e.Error(), "-listn: unknown setting (flag); did you mean -listen?")
	if e.Problems[len(e.Problems)-1].Key != "-listn" {
		t.Fatalf("%v", e)
	}
	e = loadErr[Config](t, env(), config.Flags([]string{"stray"}))
	has(t, e.Error(), "args: 1 argument(s) are not flags")
}

func TestEnvWithAnEmptyPrefixIsAPanic(t *testing.T) {
	mustPanic(t, `config.Env: the prefix is empty, which would read PATH and HOME; give the application's name, such as "APP"`, func() { config.Env("", nil) })
	mustPanic(t, "Env was given twice; give one Env", func() { config.Load[Config](config.Env("A", nil), config.Env("B", nil)) })
	mustPanic(t, "Flags was given twice; give one Flags", func() { config.Load[Config](config.Flags(nil), config.Flags(nil)) })
}

type Lists struct {
	Peers []string
	Ports []int
}

func TestASliceIsAnArrayInAFileAndCommaSeparatedInTheEnvironmentAndAFlag(t *testing.T) {
	c := mustLoad[Lists](t, config.File(files(map[string]string{"a.yaml": "peers: [a:1, 'b:2']\nports: [1, 2]\n"}), "a.yaml"))
	if !reflect.DeepEqual(c.Peers, []string{"a:1", "b:2"}) || !reflect.DeepEqual(c.Ports, []int{1, 2}) {
		t.Fatalf("file: %+v", c)
	}
	c = mustLoad[Lists](t, config.Env("APP", []string{"APP_PEERS=a:1, b:2 ,c", "APP_PORTS=3,4"}))
	if !reflect.DeepEqual(c.Peers, []string{"a:1", "b:2", "c"}) || !reflect.DeepEqual(c.Ports, []int{3, 4}) {
		t.Fatalf("env: %+v", c)
	}
	c = mustLoad[Lists](t, config.Flags([]string{"-peers", "x,y", "-ports=5"}))
	if !reflect.DeepEqual(c.Peers, []string{"x", "y"}) || !reflect.DeepEqual(c.Ports, []int{5}) {
		t.Fatalf("flag: %+v", c)
	}
	e := loadErr[Lists](t, config.Env("APP", []string{"APP_PEERS=a", "APP_PORTS=1,x,3"}))
	has(t, e.Error(), "ports[1]: expected an integer; an element cannot contain a comma here, a list of such values is set in a file (env APP_PORTS)")
}

func TestAFlagGivenTwiceForOneKeyIsRefused(t *testing.T) {
	e := loadErr[Lists](t, config.Env("APP", []string{"APP_PEERS=a", "APP_PORTS=1"}), config.Flags([]string{"-peers=a", "-peers=b"}))
	has(t, e.Error(), "peers: given twice; give it once (flag -peers)")
}

type Complex struct {
	Labels map[string]string
	Hosts  []Host
}
type Host struct {
	Name string
	Port bites.Opt[int]
}

func TestAMapAndAListOfStructsAreReadFromAFileOnlyAndTheEnvironmentSaysSo(t *testing.T) {
	src := "labels:\n  team: core\n  tier: '1'\nhosts:\n  - name: a\n    port: 80\n  - name: b\n"
	c := mustLoad[Complex](t, config.File(files(map[string]string{"a.yaml": src}), "a.yaml"))
	if c.Labels["team"] != "core" || len(c.Hosts) != 2 || c.Hosts[0].Port.Or(0) != 80 || c.Hosts[1].Name != "b" {
		t.Fatalf("%+v", c)
	}
	e := loadErr[Complex](t, config.File(files(map[string]string{"a.yaml": src}), "a.yaml"), config.Env("APP", []string{"APP_LABELS=a=b"}))
	has(t, e.Error(), "APP_LABELS: unknown setting (env); a map or a list of structs is read from a file only, set labels in the file")
	e = loadErr[Complex](t, config.File(files(map[string]string{"a.yaml": "labels: {a: b}\nhosts:\n  - port: x\n    nme: q\n"}), "a.yaml"))
	has(t, e.Error(), "hosts[0].name: missing (file a.yaml, line 3)")
	has(t, e.Error(), "hosts[0].port: expected an integer (file a.yaml, line 3)")
	has(t, e.Error(), "hosts[0].nme: unknown setting (file a.yaml, line 4); did you mean hosts[0].name?")
}

type withPtr struct{ Trace *string }
type withConn struct{ DB struct{ Conn fmt.Stringer } }
type withUnexported struct {
	Name  string
	cache string
}
type withChan struct{ C chan int }
type withNull struct{ N bites.Null[string] }
type holdsHelp struct{ Help string }

func TestAPointerAChannelAndAnUnexportedFieldArePanicsAtLoad(t *testing.T) {
	mustPanic(t, "config.Load[withPtr]: field Trace is a *string; use bites.Opt[string] for a setting that may be missing", func() { config.Load[withPtr]() })
	mustPanic(t, "config.Load[withConn]: field DB.Conn has type fmt.Stringer, which cannot be read from text; use a string and open it in main", func() { config.Load[withConn]() })
	mustPanic(t, "config.Load[withUnexported]: field cache is unexported, so no origin can fill it; export it or move it out of withUnexported", func() { config.Load[withUnexported]() })
	mustPanic(t, "field C has type chan int, which cannot be read from text", func() { config.Load[withChan]() })
	mustPanic(t, "field N has type bites.Null[string], which cannot be read from text", func() { config.Load[withNull]() })
	mustPanic(t, "config.Load[holdsHelp]: key help collides with the flag -help; rename the field with WireNames", func() { config.Load[holdsHelp]() })
}

type secrets struct {
	Name string
	Key  config.Secret
}

func TestASecretPrintsRedactedInPrintfSlogAndJSONAndRevealsOnlyThroughReveal(t *testing.T) {
	c := mustLoad[secrets](t, config.Env("APP", []string{"APP_NAME=n", "APP_KEY=hunter2"}))
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("cfg", "cfg", c, "key", c.Key)
	j, _ := json.Marshal(c)
	all := fmt.Sprintf("%v|%+v|%#v|%s|%q|%d", c, c, c, c.Key, c.Key, c.Key) + buf.String() + string(j)
	if strings.Contains(all, "hunter2") || !strings.Contains(all, "[redacted]") {
		t.Fatal(all)
	}
	if c.Key.Reveal() != "hunter2" {
		t.Fatal("Reveal")
	}
}

func TestASecretHasNoFlagAndSettingOneByFlagIsRefusedWithoutItsValue(t *testing.T) {
	file := files(map[string]string{"app.yaml": "{}"})
	e := loadErr[Config](t, config.File(file, "app.yaml"), env(), config.Flags([]string{"-db-url=postgres://u:p@h/d"}))
	has(t, e.Error(), "db.url: a Secret cannot be set by flag (the command line is visible to every user of the machine); use APP_DB_URL, APP_DB_URL_FILE or \"db.url\" in a file (flag -db-url)")
	if strings.Contains(e.Error(), "postgres") {
		t.Fatal(e)
	}
	_, err := config.Load[Config](env(), config.Flags([]string{"-h"}))
	if strings.Contains(fmt.Sprint(err), "-db-url") {
		t.Fatalf("a Secret has no flag in the usage: %v", err)
	}
}

func TestASecretIsReadFromAFileNamedByTheFileVariableWithItsNewlineTrimmed(t *testing.T) {
	sec := files(map[string]string{"run/secrets/db_url": "postgres://x\n"})
	c := mustLoad[Config](t, config.Env("APP", []string{"APP_DB_URL_FILE=/run/secrets/db_url", "APP_DB_POOL_MAX=1", "APP_DB_READ_ONLY=0", "APP_PEERS=a"}), config.SecretFiles(sec))
	if c.DB.URL.Reveal() != "postgres://x" {
		t.Fatalf("%q", c.DB.URL.Reveal())
	}
	e := loadErr[Config](t, config.Env("APP", []string{"APP_DB_URL_FILE=/run/secrets/db_url"}))
	has(t, e.Error(), "db.url: the variable names a file, but Load was given no SecretFiles option to read it from")
	e = loadErr[Config](t, config.Env("APP", []string{"APP_DB_URL_FILE=/run/secrets/none"}), config.SecretFiles(sec))
	has(t, e.Error(), "db.url: the file does not exist (env APP_DB_URL_FILE)")
	if strings.Contains(e.Error(), "run/secrets") {
		t.Fatal("the path is a value")
	}
}

func TestASecretSetByBothTheVariableAndItsFileVariableIsRefused(t *testing.T) {
	e := loadErr[Config](t, config.Env("APP", []string{"APP_DB_URL=x", "APP_DB_URL_FILE=/y"}), config.SecretFiles(files(nil)))
	has(t, e.Error(), "db.url: APP_DB_URL and APP_DB_URL_FILE are both set; set one (env APP_DB_URL_FILE)")
}

func TestAFileVariableForAKeyThatIsNotASecretIsAnUnknownSetting(t *testing.T) {
	e := loadErr[Config](t, env("APP_LISTEN_FILE=/x"))
	has(t, e.Error(), "APP_LISTEN_FILE: unknown setting (env); only a Secret is read from a file named by a _FILE variable, and listen is not one")
}

func TestHelpReturnsAnErrorForWhichErrorsIsFlagErrHelpHoldsAndListsEveryKeyItsOriginsAndItsDefault(t *testing.T) {
	for _, h := range []string{"-h", "-help", "--help"} {
		_, err := config.Load[Config](config.File(files(map[string]string{"a.yaml": "{}"}), "a.yaml"), env(), config.Flags([]string{h}))
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("%s: %v", h, err)
		}
		u := err.Error()
		for _, want := range []string{
			`listen: -listen, APP_LISTEN, "listen" in a file (string, default :8080) the address to serve on`,
			`db.url: APP_DB_URL or APP_DB_URL_FILE, "db.url" in a file (Secret, required)`,
			"db.pool_max: -db-pool-max, APP_DB_POOL_MAX",
			"(PoolSize, required) connections kept open",
			"trace: -trace, APP_TRACE",
			"optional)",
			"peers: -peers, APP_PEERS",
			"list of string, required",
			"timeout: -timeout, APP_TIMEOUT",
			"duration, default 5s",
		} {
			has(t, u, want)
		}
	}
}

func TestTheLoggerWritesOneRecordPerSettingWithItsOriginAndTheSecretRedacted(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}}))
	fsys := files(map[string]string{"app.yaml": "\n\ndb:\n  pool_max: 20\n"})
	mustLoad[Config](t,
		config.File(fsys, "app.yaml"),
		config.Env("APP", []string{"APP_DB_URL=hunter2", "APP_DB_READ_ONLY=true", "APP_PEERS=a"}),
		config.Flags([]string{"-listen=:9000"}),
		config.Logger(log),
	)
	out := buf.String()
	for _, want := range []string{
		`msg="config: setting" key=db.pool_max value=20 origin="file app.yaml, line 4"`,
		`key=listen value=:9000 origin="flag -listen"`,
		`key=db.url value=[redacted] origin="env APP_DB_URL"`,
		`key=timeout value=5s origin=default`,
		`key=trace value=<absent> origin=absent`,
	} {
		has(t, out, want)
	}
	if strings.Contains(out, "hunter2") || strings.Count(out, "config: setting") != 7 {
		t.Fatalf("%d records:\n%s", strings.Count(out, "config: setting"), out)
	}
}

func TestWithoutALoggerNothingIsWritten(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)
	mustLoad[Config](t, env())
	if buf.Len() != 0 {
		t.Fatal(buf.String())
	}
}

func TestLoadReadsNoEnvironmentVariableAndNoProcessArguments(t *testing.T) {
	t.Setenv("APP_LISTEN", ":7")
	t.Setenv("APP_DB_URL", "x")
	c := mustLoad[Config](t, env())
	if c.Listen != ":8080" {
		t.Fatalf("the process environment was read: %q", c.Listen)
	}
	if c.DB.URL.Reveal() != "postgres://u:p@h/d" {
		t.Fatal("the process environment was read")
	}
}

func TestTwoLoadsInParallelDoNotShareState(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := fmt.Sprintf(":%d", 9000+i)
			c, err := config.Load[Config](env("APP_LISTEN="+v), config.Flags(nil))
			if err != nil || c.Listen != v {
				t.Errorf("%d: %q %v", i, c.Listen, err)
			}
		}()
	}
	wg.Wait()
}

func TestNoImportOfTheCoresInternalPackagesIsMade(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Skip(err)
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.Contains(p, "bites/internal") {
			t.Errorf("the module imports %s", p)
		}
	}
}

func TestAnOptIsFilledThroughSetAndTheCoreReadsItBack(t *testing.T) {
	var o bites.Opt[int]
	reflect.ValueOf(&o).MethodByName("Set").Call([]reflect.Value{reflect.ValueOf(0)})
	if v, ok := o.Get(); !ok || v != 0 {
		t.Fatalf("%v %v", v, ok)
	}
	c := mustLoad[struct{ N bites.Opt[int] }](t, config.Env("APP", []string{"APP_N=0"}))
	if v, ok := c.N.Get(); !ok || v != 0 {
		t.Fatalf("%v %v", v, ok)
	}
}

func TestAFileWithAnExtensionOtherThanJSONOrYAMLIsAPanicNamingTheCall(t *testing.T) {
	mustPanic(t, `config.File: the file "app.toml" does not end in .json, .yaml or .yml`, func() { config.File(files(nil), "app.toml") })
}

func TestASyntaxErrorInAFileIsOneErrorNamingTheFileAndTheLine(t *testing.T) {
	for name, src := range map[string]string{"a.json": "{\n\"a\": 1,\n x}", "a.yaml": "a: 1\nb: [\n"} {
		e := loadErr[struct{ A int }](t, config.File(files(map[string]string{name: src}), name))
		first := e.Problems[len(e.Problems)-1]
		if first.Key != name || !strings.HasPrefix(first.Origin, "file "+name+", line ") || !strings.Contains(first.Message, "not valid") {
			t.Fatalf("%+v", first)
		}
	}
}
