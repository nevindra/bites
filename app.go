package bites

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"uuid"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/openapi"
	"github.com/nevindra/bites/internal/reqctx"
	"github.com/nevindra/bites/internal/router"
	"github.com/nevindra/bites/internal/servehook"
	"github.com/nevindra/bites/internal/testcodec"
	"github.com/nevindra/bites/internal/testlog"
	"github.com/nevindra/bites/internal/wire"
)

// ErrServerClosed is what Serve and Listen return after Shutdown or Close.
var ErrServerClosed = engine.ErrServerClosed

// Limits are the limits of ADR 0024 that the app has so far. A zero field takes the default in
// its comment. The per-route ones (MaxBody, BodyTimeout) are also options of a route or a group.
// The WebSocket limits are the last group (ADR 0037, ADR 0039, ADR 0046).
type Limits struct {
	MaxHead      int           // request head, 8 KiB; over it is a 431
	MaxFields    int           // header or Form fields, 100; over it is a 431 (413 for a Form)
	MaxBody      int64         // request body per route, 1 MiB; over it is a 413
	HeadTimeout  time.Duration // time to read a head, 10 s
	BodyTimeout  time.Duration // time to read a body per route, 30 s; over it is a 408
	IdleTimeout  time.Duration // idle keep-alive, 60 s
	WriteTimeout time.Duration // write, 30 s
	MaxConns     int           // connections, 90% of the file descriptor limit
	MaxDepth     int           // JSON nesting, 64; over it is a 400

	// HandshakeTimeout is the time a TLS handshake may take, 10 s; over it the connection is
	// closed.
	HandshakeTimeout time.Duration

	// MaxMessage is the most bytes of one WebSocket message, reassembled, 64 KiB; over it the
	// socket is closed with 1009. The MaxMessage option sets it for a route or a group.
	MaxMessage int
	// MessageTimeout is the time a WebSocket message that did not arrive whole has to finish,
	// 30 s; over it the socket is closed with 1008.
	MessageTimeout time.Duration
	// ReassemblyBudget is the memory all WebSockets together may hold for messages being
	// assembled, 64 MiB; the socket that would pass it is closed with 1013.
	ReassemblyBudget int64
	// SendQueue is the bytes one WebSocket's send queue holds, 1 MiB; the socket whose queue would
	// pass it is closed with 1008.
	SendQueue int64
	// SendBudget is the bytes queued to be sent on all WebSockets together, 64 MiB; the socket
	// that would pass it is closed with 1013.
	SendBudget int64
	// HeartbeatInterval is how often the server pings a WebSocket, 30 s.
	HeartbeatInterval time.Duration
	// MissedPongs is how many pings in a row may go unanswered before the socket is closed, 2.
	MissedPongs int

	// MaxFieldBytes is the most bytes of one text field of a Multipart form, 64 KiB; over it is a
	// 413 located at the field (ADR 0044).
	MaxFieldBytes int
	// MaxFormBytes is the most bytes of all the text fields of one Multipart form together, 1 MiB;
	// over it is a 413 located at the field that passed it.
	MaxFormBytes int
	// UploadMemory is the bytes of Multipart files all requests of the app keep in memory at once,
	// 32 MiB; a file that would pass it is written to a temporary file instead, with no answer to
	// the client. One request keeps at most 256 KiB of files in memory, fixed in code. Both figures
	// are judgements, not measurements of a workload (ADR 0044).
	UploadMemory int64
	// UploadDir is the directory a Multipart file too large to keep in memory is written to, as an
	// unlinked temporary file. Empty is os.TempDir(). It must exist and be writable, which New
	// checks; one on tmpfs holds the file in memory, which the start-up log warns about.
	UploadDir string
}

// check panics with a RegistrationError for a negative field (ADR 0024, ADR 0007): zero takes
// the default, and no value switches a limit off, so a -1 read from a config as "unlimited" is
// a mistake to stop at boot, not to replace silently.
func (l Limits) check() {
	neg := func(name string, bad bool, value any, unit, def string) {
		if bad {
			panic(&RegistrationError{msg: fmt.Sprintf("WithLimits: Limits.%s is %v, which is not a limit: a limit cannot be switched off, so give a positive %s, or zero for the default of %s\n\tregistered at %s", name, value, unit, def, callerWhere())})
		}
	}
	neg("MaxHead", l.MaxHead < 0, l.MaxHead, "number of bytes", "8 KiB")
	neg("MaxFields", l.MaxFields < 0, l.MaxFields, "number of fields", "100")
	neg("MaxBody", l.MaxBody < 0, l.MaxBody, "number of bytes", "1 MiB")
	neg("HeadTimeout", l.HeadTimeout < 0, l.HeadTimeout, "duration", "10 s")
	neg("BodyTimeout", l.BodyTimeout < 0, l.BodyTimeout, "duration", "30 s")
	neg("IdleTimeout", l.IdleTimeout < 0, l.IdleTimeout, "duration", "60 s")
	neg("WriteTimeout", l.WriteTimeout < 0, l.WriteTimeout, "duration", "30 s")
	neg("MaxConns", l.MaxConns < 0, l.MaxConns, "number of connections", "90% of the file descriptor limit")
	neg("MaxDepth", l.MaxDepth < 0, l.MaxDepth, "number of levels", "64")
	neg("HandshakeTimeout", l.HandshakeTimeout < 0, l.HandshakeTimeout, "duration", "10 s")
	neg("MaxMessage", l.MaxMessage < 0, l.MaxMessage, "number of bytes", "64 KiB")
	neg("MessageTimeout", l.MessageTimeout < 0, l.MessageTimeout, "duration", "30 s")
	neg("ReassemblyBudget", l.ReassemblyBudget < 0, l.ReassemblyBudget, "number of bytes", "64 MiB")
	neg("SendQueue", l.SendQueue < 0, l.SendQueue, "number of bytes", "1 MiB")
	neg("SendBudget", l.SendBudget < 0, l.SendBudget, "number of bytes", "64 MiB")
	neg("HeartbeatInterval", l.HeartbeatInterval < 0, l.HeartbeatInterval, "duration", "30 s")
	neg("MissedPongs", l.MissedPongs < 0, l.MissedPongs, "number of pongs", "2")
	neg("MaxFieldBytes", l.MaxFieldBytes < 0, l.MaxFieldBytes, "number of bytes", "64 KiB")
	neg("MaxFormBytes", l.MaxFormBytes < 0, l.MaxFormBytes, "number of bytes", "1 MiB")
	neg("UploadMemory", l.UploadMemory < 0, l.UploadMemory, "number of bytes", "32 MiB")
	if l.UploadDir != "" {
		if err := checkUploadDir(l.UploadDir); err != nil {
			panic(&RegistrationError{msg: fmt.Sprintf("WithLimits: Limits.UploadDir is %q, which cannot hold an upload: %v; it is where a file too large for memory waits while a handler runs, so give a directory that exists and the process can write, or leave it empty for os.TempDir()\n\tregistered at %s", l.UploadDir, err, callerWhere())})
		}
	}
}

func (l Limits) withDefaults() Limits {
	d := h1.DefaultLimits()
	if l.MaxHead <= 0 {
		l.MaxHead = d.MaxHead
	}
	if l.MaxFields <= 0 {
		l.MaxFields = d.MaxFields
	}
	if l.MaxBody <= 0 {
		l.MaxBody = engine.DefaultBodyLimit
	}
	if l.BodyTimeout <= 0 {
		l.BodyTimeout = engine.DefaultBodyTimeout
	}
	if l.HeadTimeout <= 0 {
		l.HeadTimeout = 10 * time.Second
	}
	if l.IdleTimeout <= 0 {
		l.IdleTimeout = 60 * time.Second
	}
	if l.WriteTimeout <= 0 {
		l.WriteTimeout = 30 * time.Second
	}
	if l.MaxConns <= 0 {
		l.MaxConns = fileLimit() / 10 * 9
		if l.MaxConns <= 0 {
			l.MaxConns = 10000
		}
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = 64
	}
	if l.HandshakeTimeout <= 0 {
		l.HandshakeTimeout = 10 * time.Second
	}
	if l.MaxFieldBytes <= 0 {
		l.MaxFieldBytes = 64 << 10
	}
	if l.MaxFormBytes <= 0 {
		l.MaxFormBytes = 1 << 20
	}
	if l.UploadMemory <= 0 {
		l.UploadMemory = 32 << 20
	}
	if l.MaxMessage <= 0 {
		l.MaxMessage = 64 << 10
	}
	if l.MessageTimeout <= 0 {
		l.MessageTimeout = 30 * time.Second
	}
	if l.ReassemblyBudget <= 0 {
		l.ReassemblyBudget = 64 << 20
	}
	if l.SendQueue <= 0 {
		l.SendQueue = 1 << 20
	}
	if l.SendBudget <= 0 {
		l.SendBudget = 64 << 20
	}
	if l.HeartbeatInterval <= 0 {
		l.HeartbeatInterval = 30 * time.Second
	}
	if l.MissedPongs <= 0 {
		l.MissedPongs = 2
	}
	return l
}

// Option configures an App (ADR 0026): everything is set on the instance, nothing is global.
type Option func(*App)

// WithLogger sets the logger for the start-up settings, handler errors and recovered panics.
// The default is slog.Default().
func WithLogger(l *slog.Logger) Option { return func(a *App) { a.log = l; a.custom["logger"] = true } }

// WithNaming sets the one naming policy of the app (ADR 0011). The default is SnakeCase.
func WithNaming(p NamingPolicy) Option {
	return func(a *App) { a.naming = p; a.custom["naming"] = true }
}

// WithTextFormat registers a type from outside this module, such as another uuid package's, as
// text with an OpenAPI format (ADR 0042). T must be a type whose pointer has UnmarshalText and
// whose value or pointer has MarshalText. The type is then read from a Path, Query, Header or
// Cookie, and from a JSON body, with its own UnmarshalText; it is written with its own MarshalText;
// the API document says `type: string` with the format given; and a value that does not decode is
// told the format's sentence ("expected a UUID" for "uuid") instead of "expected a valid value".
// A type the core already knows (time.Time, the standard library's uuid.UUID, Date) cannot be
// registered, and neither can a type twice with two formats. Anything wrong is a panic from New
// naming the line that registered the type.
func WithTextFormat[T any](format string) Option {
	where := callerWhere()
	t := reflect.TypeFor[T]()
	return func(a *App) {
		if _, ok := a.jsonSchemas[t]; ok {
			panic(&RegistrationError{msg: "WithTextFormat[" + typeLabel(t) + "]: it is already registered with WithJSONSchema, and a type is described one way\n\tregistered at " + where})
		}
		if err := checkTextFormat(t, format, a.formats); err != nil {
			panic(&RegistrationError{msg: "WithTextFormat[" + typeLabel(t) + "]: " + err.Error() + "\n\tregistered at " + where})
		}
		if a.formats == nil {
			a.formats = wire.Formats{}
		}
		a.formats[t] = format
		a.custom["textFormat"] = true
	}
}

// JSONSchema is the shape of a type that writes or reads its own JSON, declared once with
// WithJSONSchema (ADR 0076). Type is "string", "number", "integer", "boolean", "object" or
// "array", or "any" for a value that is free-form JSON and described as {}. Format is the
// OpenAPI format of a string, number or integer. Nullable says the type's own methods can write
// null, so the document allows it beside Type.
type JSONSchema = wire.JSONSchema

// WithJSONSchema registers the schema of a type that encodes or decodes itself through its own
// JSON methods, such as pgx's pgtype.Timestamptz, which the codec delegates to and the API
// document cannot look inside (ADR 0076). T, or *T, must have MarshalJSON, UnmarshalJSON or the
// json/v2 equivalents; a type with text methods alone is WithTextFormat's. The schema is then
// stated wherever T appears: an Input Body, an Output, a list, a map, an Opt or a Null. Without
// a registration such a field is refused when the route is registered, because the document
// would say {} and a generated client would say unknown. json.RawMessage and jsontext.Value
// are free-form by name and need none; any other type that is free-form says so with
// JSONSchema{Type: "any"}. Anything wrong is a panic from New naming the line that registered
// the type.
func WithJSONSchema[T any](s JSONSchema) Option {
	where := callerWhere()
	t := reflect.TypeFor[T]()
	return func(a *App) {
		if err := checkJSONSchema(t, s, a); err != nil {
			panic(&RegistrationError{msg: "WithJSONSchema[" + typeLabel(t) + "]: " + err.Error() + "\n\tregistered at " + where})
		}
		if a.jsonSchemas == nil {
			a.jsonSchemas = wire.JSONSchemas{}
		}
		a.jsonSchemas[t] = s
		a.custom["jsonSchema"] = true
	}
}

var builtinJSON = []reflect.Type{reflect.TypeFor[json.RawMessage](), reflect.TypeFor[jsontext.Value]()}

func checkJSONSchema(t reflect.Type, s JSONSchema, a *App) error {
	switch {
	case t.Kind() == reflect.Interface || t.Kind() == reflect.Pointer:
		return errors.New("it is a " + t.Kind().String() + ", and the schema belongs to the type a value has: register the concrete, non-pointer type")
	case slices.Contains(builtinTexts, t):
		return errors.New("the core already reads and describes it with a format of its own")
	case slices.Contains(builtinJSON, t):
		return errors.New("the core already describes it as free-form JSON, which needs no registration")
	case !jsoncodec.OwnJSON(t):
		return errors.New(typeLabel(t) + " has no JSON method, so it is described by its fields and a registration would hide them: if it has a text form, use WithTextFormat")
	case !slices.Contains(wire.JSONTypes, s.Type):
		return errors.New("the type is " + strconv.Quote(s.Type) + ", which is not a JSON type: give one of " + strings.Join(wire.JSONTypes, ", "))
	case s.Format != "" && s.Type != "string" && s.Type != "number" && s.Type != "integer":
		return errors.New("a format belongs to a string, number or integer, and the type is " + strconv.Quote(s.Type))
	case s.Type == "any" && s.Nullable:
		return errors.New("a free-form value already admits null: drop Nullable")
	}
	if _, ok := a.formats[t]; ok {
		return errors.New("it is already registered with WithTextFormat, and a type is described one way")
	}
	if prev, ok := a.jsonSchemas[t]; ok && prev != s {
		return errors.New("it is already registered with a different schema")
	}
	return nil
}

var (
	textMarshalerType = reflect.TypeFor[interface{ MarshalText() ([]byte, error) }]()
	builtinTexts      = []reflect.Type{reflect.TypeFor[time.Time](), reflect.TypeFor[uuid.UUID](), reflect.TypeFor[Date]()}
)

func checkTextFormat(t reflect.Type, format string, have wire.Formats) error {
	pt := reflect.PointerTo(t)
	switch {
	case format == "":
		return errors.New("the format is empty, which names no format: give the OpenAPI format, such as \"uuid\"")
	case t.Kind() == reflect.Interface || t.Kind() == reflect.Pointer:
		return errors.New("it is a " + t.Kind().String() + ", and the format belongs to the type a value has: register the concrete, non-pointer type")
	case slices.Contains(builtinTexts, t):
		return errors.New("the core already reads and describes it with a format of its own")
	case !pt.Implements(textUnmarshalerType):
		return errors.New("*" + typeLabel(t) + " has no UnmarshalText method, so a request could not be decoded into it")
	case !t.Implements(textMarshalerType) && !pt.Implements(textMarshalerType):
		return errors.New(typeLabel(t) + " has no MarshalText method, so a response would write it as the encoding of its fields and not as a string with this format")
	}
	if prev, ok := have[t]; ok && prev != format {
		return errors.New("it is already registered with the format " + strconv.Quote(prev))
	}
	return nil
}

// WithRejectUnknownQueryKeys makes every route answer a 400 for a query key its Query section
// does not name, located query.<key> (ADR 0015). Without it an unknown key is ignored, so a
// misspelled filter (?knid=government) returns every row and looks exactly like one that worked.
// A route without a Query section is held to it too: it takes no query at all. RejectUnknownQueryKeys
// and AcceptUnknownQueryKeys set it for a group or a route.
func WithRejectUnknownQueryKeys() Option {
	return func(a *App) { a.rejectUnknownQuery = true; a.custom["rejectUnknownQuery"] = true }
}

// WithLimits sets the limits of ADR 0024; a zero field keeps its default, and a negative one
// makes New panic, because no limit can be switched off.
func WithLimits(l Limits) Option { return func(a *App) { a.limits = l; a.custom["limits"] = true } }

// WithClock sets the clock the app measures its timeouts on, so a test moves time instead of
// waiting for it (ADR 0027).
func WithClock(c Clock) Option { return func(a *App) { a.clk = c; a.custom["clock"] = true } }

// Gzip configures response compression with gzip alone (ADR 0031): WithGzip(Gzip{MinSize}) is
// WithCompression(Compression{MinSize}). Compression is gzip at BestSpeed, with no level to
// choose, and it is done by the engine on the finished body; a middleware sees the uncompressed
// answer.
type Gzip struct {
	// MinSize is the smallest body that is compressed, in bytes. Zero is the default of 1 KiB;
	// a value below 1 KiB makes New panic, because under about 1.2 KiB an answer fits one TCP
	// segment and compressing it saves bytes and no packets.
	MinSize int
}

// WithGzip turns response compression on with gzip alone (ADR 0031): a body of at least MinSize
// is compressed when the request's Accept-Encoding accepts gzip. It is off without this option or
// WithCompression, because the reference workload usually sits behind a load balancer that
// compresses. Giving it twice, giving it with WithCompression, or a MinSize below 1 KiB makes
// New panic.
func WithGzip(g Gzip) Option {
	return func(a *App) {
		where := callerWhere()
		if a.compressBy != "" {
			a.refuseCompressTwice("WithGzip", where, "give one Gzip, because two could say different things")
		}
		if g.MinSize != 0 && g.MinSize < engine.CompressFloor {
			panic(&RegistrationError{msg: fmt.Sprintf("WithGzip: Gzip.MinSize is %d, which is below the floor of %d bytes: under about 1.2 KiB an answer fits one TCP segment, so compressing it saves bytes and no packets; give %d or more, or zero for the default of 1 KiB\n\tregistered at %s", g.MinSize, engine.CompressFloor, engine.CompressFloor, where)})
		}
		a.compressBy, a.compressWhere = "WithGzip", where
		a.compress = Compression{MinSize: g.MinSize, Encoders: []Encoder{GzipEncoder()}}
		a.custom["compression"] = true
	}
}

// WithoutParking keeps every idle connection on a goroutine of its own, as before parking
// (ADR 0005). By default a connection idle for 100 ms is parked on the engine's epoll and its
// goroutine ends, which takes about 2 KB off every idle connection; one that is woken pays a
// goroutine start and an epoll registration. Parking is used only where it can be (Linux, TCP),
// and this option is the way to compare against it, or to go back should it misbehave.
func WithoutParking() Option { return func(a *App) { a.noPark = true; a.custom["parking"] = true } }

// App is a set of routes and the server that serves them. The zero value is not usable; call New.
type App struct {
	log          *slog.Logger
	naming       NamingPolicy
	formats      wire.Formats         // WithTextFormat (ADR 0042)
	jsonSchemas  wire.JSONSchemas     // WithJSONSchema (ADR 0076)
	memberPlans  sync.Map             // reflect.Type -> *memberPlan: how the members of a status error are written (ADR 0088)
	statedErrors map[statedError]bool // the (status, type) pairs a route states with ErrorStatus[T]
	testPlans    sync.Map             // reflect.Type -> *jsoncodec.Decoder: the plans bitetest reads answers with, nil for a type with none

	rejectUnknownQuery bool // WithRejectUnknownQueryKeys
	limits             Limits
	uploads            []uploadRoute // the Upload and Multipart routes, for the start-up log
	uploadMem          atomic.Int64  // file bytes every Multipart request holds in memory now (Limits.UploadMemory)
	given              Limits        // what the user set, before the defaults, so the log can say which are theirs
	clk                clock.Clock
	info               reqctx.App // the logger and the clock, as a request's context holds them
	formatless         sync.Map   // reflect.Type -> the first field of it: a uuid type read with no format (noteFormatless)
	custom             map[string]bool

	mu      sync.Mutex
	router  *router.Router[*route]
	methods map[string]bool
	allow   string // for OPTIONS *, built when serving starts
	srv     *engine.Server
	serving bool

	api       *openapi.Registry           // the schema generator, made when the first route is described
	routes    []RouteInfo                 // one per route, appended under mu (ADR 0048)
	routeList atomic.Pointer[[]RouteInfo] // routes as Routes reads it without mu, so a UseEach function may call it
	wires     []*wireRoute                // one record per typed route, for WriteWireTable (ADR 0011)
	docs      []*routeDoc                 // one record per typed route, kept at Registration (ADR 0020)
	docShapes map[string]string           // an OpenAPI path template -> the pattern that made it
	docAuth   []docScheme                 // the security schemes the routes' Providers use, named in order (ADR 0069)
	docOps    map[string]docOp            // the operationIds, by what a client generator keeps of them (ADR 0020)
	docInfo   *Info                       // set by OpenAPI
	docPath   string
	docWhere  string
	doc       []byte // the document, built once when the app starts serving

	mw []Middleware // App.Use, outermost first; read at request time (ADR 0048)
	// The slab room of a request served through the chain (slabRoom): routeRoom is the largest of
	// any route's, room is the app middleware's plus routeRoom, set when the app starts serving.
	routeRoom, room int
	runs            sync.Pool // *chainRun, for the requests that have middleware or Providers

	provOpts []RouteOption // App.Provide, in order: the enclosing scope of every route (ADR 0023)
	sealed   string        // the app's first route or group, and where; "" until then
	builtin  string        // the first OpenAPI or ServeMetrics route, and where; UseEach must come before it (ADR 0048)

	compressBy    string      // "WithGzip" or "WithCompression", whichever was given; "" for none
	compress      Compression // what it was given, its Encoders filled in (ADR 0031, ADR 0061)
	compressWhere string      // where, for the refusal of a second one
	noPark        bool        // WithoutParking was given

	statics []*staticRoute // App.Static and Group.Static, prepared when serving starts (ADR 0065)
	proxies *proxyPolicy   // WithTrustedProxies (ADR 0025); nil when no proxy is trusted

	origin      *originPolicy // WithAllowOrigins and WithAllowAnyOrigin: the Origins every WebSocket route allows besides its own host (ADR 0046)
	originWhere string        // where WithAllowOrigins was first given, for the refusals that name the app's list (ADR 0057)
	wsRoutes    int           // the WebSocket routes, for the start-up log

	obs      []Observer // WithObserver, in order (ADR 0051)
	obsWhere string     // where it was given, for the refusal of a second one

	health health // WithHealth, WithShutdownDelay and SetReady (ADR 0052)

	life lifecycle // App.Service and WithServiceTimeout (ADR 0079)

	goAwaySpread time.Duration // WithGoAwaySpread (ADR 0029); 0 for the engine's default
	goAwayWhere  string        // where it was given, for the refusal of a second one

	errMappers []func(error) *StatusError // WithErrorMapper, in order (ADR 0049)

	traceOn    bool // WithTraceContext (ADR 0059)
	traceBegin bool // TraceContext.Begin
	traceWhere string
}

// New returns an App with no routes.
func New(opts ...Option) *App {
	a := &App{
		naming:  SnakeCase,
		custom:  map[string]bool{},
		router:  router.New[*route](),
		methods: map[string]bool{},
	}
	for _, o := range opts {
		o(a)
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	if a.clk == nil {
		a.clk = clock.Real()
	}
	a.info = reqctx.App{Log: a.log, Clock: a.clk}
	a.runs.New = newRun(a)
	a.limits.check()
	a.given = a.limits
	a.limits = a.limits.withDefaults()
	return a
}

func init() {
	servehook.Start = func(app any, ln net.Listener) (func() error, error) {
		a := app.(*App)
		srv, err := a.bind(ln, false, false)
		if err != nil {
			return nil, err
		}
		return func() error { return a.serveOn(srv, ln, nil, nil) }, nil
	}
	testcodec.Encode = func(app any, v any) ([]byte, error) {
		a := app.(*App)
		rv := reflect.ValueOf(v)
		if !rv.IsValid() {
			return []byte("null"), nil
		}
		e, err := jsoncodec.CompileEncoder(rv.Type(), jsoncodec.Options{Names: a.naming, Formats: a.formats, JSONSchemas: a.jsonSchemas})
		if err != nil {
			return nil, err
		}
		p := reflect.New(rv.Type())
		p.Elem().Set(rv)
		return e.Append(nil, p.UnsafePointer())
	}
	testcodec.Decode = func(app any, b []byte, v any) (bool, error) {
		a := app.(*App)
		rv := reflect.ValueOf(v)
		if rv.Kind() != reflect.Pointer || rv.IsNil() {
			return false, nil
		}
		t := rv.Type().Elem()
		var dec *jsoncodec.Decoder
		if c, ok := a.testPlans.Load(t); ok {
			dec, _ = c.(*jsoncodec.Decoder)
		} else {
			// An answer is not a request: a member the type has no field for is skipped, and a
			// member the answer left out is the zero value, as a client's own types read it.
			dec, _ = jsoncodec.CompileDecoder(t, jsoncodec.Options{Names: a.naming, Formats: a.formats, JSONSchemas: a.jsonSchemas, AllowUnknown: true, Undescribed: true})
			a.testPlans.Store(t, dec) // a nil plan is kept too: the type has none
		}
		if dec == nil {
			return false, nil
		}
		return true, decodePayload(dec, bytes.Clone(b), rv.UnsafePointer())
	}
	testlog.Set = func(app any, l *slog.Logger) bool {
		a := app.(*App)
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.custom["logger"] {
			return false
		}
		a.log = l
		a.info.Log = l
		return true
	}
}

// Clock is the clock the app was given, or the real one.
func (a *App) Clock() Clock { return a.clk }

// Now is the time on the clock of the app that ctx's request belongs to (the one WithClock gave,
// else the real one), so a Provider, a middleware or a handler that checks a time against the
// request (a session's absolute lifetime, a token's expiry) moves with the clock a test moves. It
// works on a context derived from the request's and on one kept after the request ended. On a
// context that is a Service's life (the ctx of its Start), it is the app's clock too. On any other
// context, or a nil one, it is time.Now().
func Now(ctx context.Context) time.Time {
	if clk := ctxClock(ctx); clk != nil {
		return clk.Now()
	}
	return time.Now()
}

// ctxClock is the clock of ctx's app, or nil when ctx is not a request's.
func ctxClock(ctx context.Context) Clock {
	if ctx == nil {
		return nil
	}
	if c := reqctx.Of(ctx); c != nil {
		return c.Clock()
	}
	if a := serviceApp(ctx); a != nil {
		return a.Clock
	}
	return nil
}

// route is one entry of the route table: the closure the generic registration made, which
// already holds the compiled plans of its Input and Output (ADR 0007).
type route struct {
	serve   func(a *App, ex *engine.Exchange, ps *router.Params, ctx context.Context, req *Request)
	mw      []Middleware                    // the groups' then the route's own, outermost first
	chain   bool                            // it has middleware or Providers, so it is served through the chain
	room    int                             // the slab room its middleware write into (slabRoom), when no app middleware runs
	pattern string                          // the path pattern with its group's prefix, for Outcome.Route (ADR 0051)
	origins *[len(stdMethods)]reqctx.Origin // the pattern with each standard method, what a request's Ctx points at (newRoute)
}

// newRoute returns a route of pattern with its origins made.
func newRoute(pattern string) *route {
	rt := &route{pattern: pattern, origins: new([len(stdMethods)]reqctx.Origin)}
	for i, m := range stdMethods {
		rt.origins[i] = reqctx.Origin{Method: m, Route: pattern}
	}
	return rt
}

// stdMethods are the methods internMethod answers with a constant, in the order of the origins
// tables.
var stdMethods = [...]string{"GET", "HEAD", "POST", "PUT", "DELETE", "CONNECT", "OPTIONS", "TRACE", "PATCH"}

// unrouted is the Origin of a request not matched to a route yet, by standard method.
var unrouted = func() (o [len(stdMethods)]reqctx.Origin) {
	for i, m := range stdMethods {
		o[i].Method = m
	}
	return o
}()

func methodIndex(m string) int {
	switch m {
	case "GET":
		return 0
	case "HEAD":
		return 1
	case "POST":
		return 2
	case "PUT":
		return 3
	case "DELETE":
		return 4
	case "CONNECT":
		return 5
	case "OPTIONS":
		return 6
	case "TRACE":
		return 7
	case "PATCH":
		return 8
	}
	return -1
}

// originFor is the Origin of a request with method matched to rt, or to no route when rt is nil.
// A method outside stdMethods (an extension method, rare) gets one of its own.
func (rt *route) originFor(method string) *reqctx.Origin {
	i := methodIndex(method)
	switch {
	case rt == nil && i >= 0:
		return &unrouted[i]
	case rt == nil:
		return &reqctx.Origin{Method: method}
	case i >= 0:
		return &rt.origins[i]
	}
	return &reqctx.Origin{Method: method, Route: rt.pattern}
}

// handler adapts the App to the engine without putting an internal type on App's method set.
type handler struct{ a *App }

// Serve is the engine's entry for every request, in the order of ADR 0048: the app's middleware,
// the match (404, 405 and 400 for a bad path are answered here), the group and route middleware,
// then the route's closure decodes the Input and calls the handler. A request with no middleware
// at all takes the first branch of each choice and allocates nothing for it.
func (h handler) Serve(ex *engine.Exchange) {
	a := h.a
	head := ex.Head()
	if a.health.on && a.answerHealth(ex, head.Method, head.Path) {
		return
	}
	if head.Asterisk {
		r := ex.Respond(204)
		r.Header("Allow", a.allow)
		return
	}
	if len(a.mw) > 0 || len(a.obs) > 0 {
		a.serveChain(ex, nil, nil)
		return
	}
	var ps router.Params
	rt, res, allow := a.router.Lookup(head.Method, head.Path, &ps)
	if res != router.Found {
		a.unmatched(ex, res, allow)
		return
	}
	if !rt.chain {
		if a.traceOn {
			a.beginTrace(ex)
		}
		rc := ex.Context()
		rc.SetRequest(&a.info, rt.originFor(internMethod(head.Method, nil)))
		rt.serve(a, ex, noescape(&ps), rc, nil)
		return
	}
	a.serveChain(ex, rt, &ps)
}

// unmatched answers a request the router did not find a route for.
func (a *App) unmatched(ex *engine.Exchange, res router.Result, allow string) {
	switch res {
	case router.MethodNotAllowed:
		problem(ex, 405, "").Header("Allow", allow)
	case router.BadPath:
		problem(ex, 400, "the path is not normalised")
	default:
		problem(ex, 404, "")
	}
}

// Use adds app middleware (ADR 0048): it runs before the route is matched, so it sees every
// request including a 404, a 405 and a CORS preflight. Calls add to the end of the chain, so the
// first one given is outermost. Unlike a group's Use it may come after routes, because it is
// read when a request arrives and a late call cannot skip a route. A nil middleware panics, and
// so does a call after the app started serving.
func (a *App) Use(mw ...Middleware) {
	where := callerWhere()
	for i, m := range mw {
		if m == nil {
			panic(&RegistrationError{msg: fmt.Sprintf("app: Use: middleware %d is nil\n\tregistered at %s", i+1, where)})
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.serving {
		panic(&RegistrationError{msg: "app: Use registered at " + where + " after the app started serving: middleware can be added only before it does"})
	}
	if err := a.bindMiddleware(mw); err != nil {
		panic(&RegistrationError{msg: "app: Use: " + err.Error() + "\n\tregistered at " + where})
	}
	a.mw = append(a.mw, mw...)
}

// UseEach adds a function that builds a middleware from each route's RouteInfo, for every route
// the app registers after it (ADR 0048): build is called once for each, at Registration. The
// Middleware it returns runs inside every Use middleware of the route, next to the handler; a nil
// Middleware means none for this route; an error refuses the route and the app panics, which is
// what makes default-deny a start-up failure. Unlike Use it must come before the app's first
// route and first group, because it cannot reach a route that exists already: after one it
// panics, naming the line that sealed the app. A nil function panics. build runs with the
// Registration lock held, so it must not register anything.
func (a *App) UseEach(build func(r RouteInfo) (Middleware, error)) {
	where := callerWhere()
	opt := useEachOption(build, where)
	var scratch routeConfig
	if err := applyOptions(&scratch, []RouteOption{opt}); err != nil {
		panic(registrationFailure("app", err, where))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.serving {
		panic(&RegistrationError{msg: "app: UseEach registered at " + where + " after the app started serving: it can be added only before it does"})
	}
	if a.sealed != "" {
		panic(&RegistrationError{msg: "app: UseEach is called after " + a.sealed + " was registered, so it would not reach it: move UseEach above it\n\tregistered at " + where})
	}
	if a.builtin != "" {
		panic(&RegistrationError{msg: "app: UseEach is called after " + a.builtin + " was registered, so it would not reach it: move UseEach above it\n\tregistered at " + where})
	}
	a.provOpts = append(a.provOpts[:len(a.provOpts):len(a.provOpts)], opt)
}

// start freezes the route table and builds the server, once. After it, adding a route panics
// (ADR 0048).
func (a *App) start() *engine.Server {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.srv != nil {
		return a.srv
	}
	a.router.Freeze()
	a.serving = true
	a.room = slabRoom(a.mw) + a.routeRoom
	for _, st := range a.statics {
		st.prepare(a)
	}
	a.buildDocument()
	ms := make([]string, 0, len(a.methods)+2)
	for m := range a.methods {
		ms = append(ms, m)
	}
	if a.methods["GET"] && !a.methods["HEAD"] {
		ms = append(ms, "HEAD")
	}
	if !a.methods["OPTIONS"] {
		ms = append(ms, "OPTIONS")
	}
	slices.Sort(ms)
	a.allow = strings.Join(ms, ", ")
	a.srv = engine.New(handler{a}, engine.Options{
		Limits:           h1.Limits{MaxHead: a.limits.MaxHead, MaxFields: a.limits.MaxFields},
		HeadTimeout:      a.limits.HeadTimeout,
		IdleTimeout:      a.limits.IdleTimeout,
		WriteTimeout:     a.limits.WriteTimeout,
		HandshakeTimeout: a.limits.HandshakeTimeout,
		MaxConns:         a.limits.MaxConns,
		Clock:            a.clk,
		Logger:           a.log,
		Compress:         a.compressConfig(),
		NoPark:           a.noPark,
		WS: engine.WSConfig{
			MessageTimeout:    a.limits.MessageTimeout,
			HeartbeatInterval: a.limits.HeartbeatInterval,
			MissedPongs:       a.limits.MissedPongs,
			GoAwaySpread:      a.goAwaySpread,
			ReassemblyBudget:  a.limits.ReassemblyBudget,
			SendQueue:         a.limits.SendQueue,
			SendBudget:        a.limits.SendBudget,
		},
	})
	a.logSettings()
	return a.srv
}

// logSettings is the start-up line per setting, marked default or custom (ADR 0026). Every limit
// of ADR 0024 that the app has is listed, each marked on its own: a limit is custom when the
// user gave it a value, even if another limit was left at its default.
func (a *App) logSettings() {
	mark := func(custom bool) string {
		if custom {
			return "custom"
		}
		return "default"
	}
	l, g := a.limits, a.given
	log := func(setting string, value any, custom bool) {
		a.log.Info("bites: settings", "setting", setting, "value", value, "source", mark(custom))
	}
	a.log.Info("bites: settings", "setting", "naming", "source", mark(a.custom["naming"]))
	log("max head bytes", l.MaxHead, g.MaxHead > 0)
	log("max header fields", l.MaxFields, g.MaxFields > 0)
	log("max body bytes", l.MaxBody, g.MaxBody > 0)
	log("head timeout", l.HeadTimeout, g.HeadTimeout > 0)
	log("body timeout", l.BodyTimeout, g.BodyTimeout > 0)
	log("idle timeout", l.IdleTimeout, g.IdleTimeout > 0)
	log("write timeout", l.WriteTimeout, g.WriteTimeout > 0)
	log("tls handshake timeout", l.HandshakeTimeout, g.HandshakeTimeout > 0)
	log("max connections", l.MaxConns, g.MaxConns > 0)
	log("json depth", l.MaxDepth, g.MaxDepth > 0)
	log("errors listed in a 400", maxErrors, false)
	a.formatless.Range(func(k, v any) bool {
		t := k.(reflect.Type)
		a.log.Info("bites: a uuid type is read as text with no format: a value that does not decode is told \"expected a valid value\" and the API document says type string; give the app bites.WithTextFormat["+t.String()+"](\"uuid\") for \"expected a UUID\" and format: uuid", "type", t.PkgPath()+"."+t.Name(), "first", v.(string))
		return true
	})
	if len(a.life.services) > 0 {
		names := make([]string, len(a.life.services))
		for i, e := range a.life.services {
			names[i] = e.Name
		}
		log("services", strings.Join(names, ", "), true)
		log("service timeout", a.life.wait(), a.custom["service timeout"])
	}
	parking := "on"
	if a.noPark {
		parking = "off"
	}
	log("idle connections park", parking, a.noPark)
	unknownQuery := "ignored"
	if a.rejectUnknownQuery {
		unknownQuery = "rejected"
	}
	log("unknown query keys", unknownQuery, a.rejectUnknownQuery)
	if p := a.proxies; p != nil {
		log("trusted proxies", fmt.Sprintf("%v, unix sockets %v, header %s, forwarded host %v, forwarded prefix %v", p.prefixes, p.unix, p.header, p.host, p.prefix), true)
	} else {
		log("trusted proxies", "none", false)
	}
	if a.compressBy != "" {
		log("compression", a.codings(), true)
		log("compression min size bytes", max(a.compress.MinSize, engine.CompressDefaultMinSize), a.compress.MinSize > 0)
	}
	if a.wsRoutes > 0 {
		log("websocket max message bytes", l.MaxMessage, g.MaxMessage > 0)
		log("websocket message timeout", l.MessageTimeout, g.MessageTimeout > 0)
		log("websocket reassembly budget bytes", l.ReassemblyBudget, g.ReassemblyBudget > 0)
		log("websocket send queue bytes", l.SendQueue, g.SendQueue > 0)
		log("websocket send budget bytes", l.SendBudget, g.SendBudget > 0)
		log("websocket heartbeat interval", l.HeartbeatInterval, g.HeartbeatInterval > 0)
		log("websocket missed pongs before close", l.MissedPongs, g.MissedPongs > 0)
		spread := 3 * time.Second
		if a.goAwaySpread > 0 {
			spread = a.goAwaySpread
		}
		log("websocket shutdown close spread, at most", spread, a.goAwaySpread > 0)
	}
	for _, u := range a.uploads {
		if u.plain {
			log("route "+u.label+": max body bytes", u.maxBody, true)
			log("route "+u.label+": body timeout, derived from max body bytes", u.timeout, false)
			continue
		}
		log("upload "+u.label+": max body bytes", u.maxBody, true)
		log("upload "+u.label+": body timeout", u.timeout, u.timeoutExplicit)
		if u.maxInFlight > 0 {
			log("upload "+u.label+": in flight", u.maxInFlight, true)
		} else {
			log("upload "+u.label+": in flight", "unlimited", false)
		}
		if u.multipart {
			worst := fmt.Sprintf("%d bytes of disk and %d bytes of memory for each request in flight", u.maxBody, uploadRequestMemory)
			if u.maxInFlight > 0 {
				worst = fmt.Sprintf("%d bytes of disk and %d bytes of memory (%d in flight)", u.maxBody*int64(u.maxInFlight), uploadRequestMemory*int64(u.maxInFlight), u.maxInFlight)
			} else if _, mem := a.uploadDirIsMemory(); mem {
				worst = "unbounded: the upload directory is on tmpfs, so a file written to it is memory, and the route has no MaxInFlight"
			}
			log("upload "+u.label+": worst case", worst, false)
		}
	}
	if a.multiparts() {
		log("upload files in memory, bytes", l.UploadMemory, g.UploadMemory > 0)
		log("upload field bytes", l.MaxFieldBytes, g.MaxFieldBytes > 0)
		log("upload text bytes", l.MaxFormBytes, g.MaxFormBytes > 0)
		dir, mem := a.uploadDirIsMemory()
		log("upload directory", dir, g.UploadDir != "")
		if mem {
			a.log.Warn("bites: the upload directory is on tmpfs, so a file written there is held in memory and the limit on memory does not bound it; set Limits.UploadDir to a directory on disk", "dir", dir)
		}
	}
}

// Listen serves on a TCP address until Shutdown or Close, and returns ErrServerClosed then.
// ProxyProtocol among opts makes the listener read a PROXY header from a trusted balancer first
// (ADR 0058).
func (a *App) Listen(addr string, opts ...ListenOption) error {
	px := a.proxyFor("Listen", false, opts)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return a.serveWith(ln, nil, px)
}

// ListenUnix serves on a Unix socket at path until Shutdown or Close, and returns
// ErrServerClosed then (ADR 0068): a proxy on the same machine (nginx, a sidecar) reaches the app
// without a TCP port. The socket file is made with the permission bits perm (0o660 lets the
// group of the process connect; connecting needs write permission) and removed when the listener
// closes. A file left at path by a process that is gone (a socket nothing answers on) is removed
// first; anything else at path, a socket another process listens on or a file that is not a
// socket, is an error and is left alone. A perm of zero or with bits beyond 0o777 panics.
//
// A Unix connection has no peer address: Request.RemoteAddr is the zero netip.AddrPort, and its
// peer is a trusted proxy only with TrustedProxies.UnixSockets. ProxyProtocol among opts makes
// the socket read a PROXY header first, whose client becomes the peer (ADR 0058).
func (a *App) ListenUnix(path string, perm fs.FileMode, opts ...ListenOption) error {
	if perm == 0 || perm&^0o777 != 0 {
		panic(&RegistrationError{msg: fmt.Sprintf("ListenUnix: the permission %v is not a set of permission bits: give the bits of the socket file, such as 0o660 for the owner and its group, which is who may connect\n\tregistered at %s", perm, callerWhere())})
	}
	px := a.proxyFor("ListenUnix", true, opts)
	if err := removeStaleSocket(path); err != nil {
		return err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, perm); err != nil {
		ln.Close()
		return err
	}
	return a.serveWith(ln, nil, px)
}

// removeStaleSocket removes a socket file at path that no process answers on, as a server killed
// without closing its listener leaves behind. Nothing else is removed.
func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode().Type() != fs.ModeSocket {
		return nil // nothing there, or not a socket: net.Listen reports it
	}
	c, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		c.Close()
		return fmt.Errorf("bites: ListenUnix: another process is listening on %s", path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return nil
	}
	return os.Remove(path)
}

// Serve serves on ln until Shutdown or Close. The listener may be any net.Listener, which is
// how a test serves the app over memory (ADR 0027). More than one Serve may run on one App, each
// from its own goroutine, plain and TLS, TCP and Unix together: Shutdown and Close reach every
// one of them (ADR 0068). Each logs the address it serves on as it starts. ProxyProtocol among
// opts makes the listener read a PROXY header from a trusted balancer first (ADR 0058).
func (a *App) Serve(ln net.Listener, opts ...ListenOption) error {
	return a.serveWith(ln, nil, a.proxyFor("Serve", isUnix(ln), opts))
}

// serveWith serves one listener, TLS when cfg is set (already checked), with the PROXY protocol
// when px is set.
func (a *App) serveWith(ln net.Listener, cfg *tls.Config, px *engine.ProxyProtocol) error {
	srv, err := a.bind(ln, cfg != nil, px != nil)
	if err != nil {
		return err
	}
	return a.serveOn(srv, ln, cfg, px)
}

// bind freezes the app, logs the listener, and runs the services' start phase (ADR 0079): the
// listener is bound already, and no connection is accepted until every Start has returned. When
// a Start failed the listener is closed and the wrapped error returned; when Shutdown or Close
// came first the listener is closed and ErrServerClosed returned once the services have stopped.
func (a *App) bind(ln net.Listener, tls, proxy bool) (*engine.Server, error) {
	srv := a.start()
	a.logListener(ln, tls, proxy)
	if err := a.beginServices(); err != nil {
		ln.Close()
		if err == ErrServerClosed {
			a.waitStopped()
		}
		return nil, err
	}
	return srv, nil
}

// serveOn accepts on ln until the server closes, then waits for the services to stop.
func (a *App) serveOn(srv *engine.Server, ln net.Listener, cfg *tls.Config, px *engine.ProxyProtocol) error {
	err := srv.ServeWith(ln, cfg, px)
	if errors.Is(err, engine.ErrServerClosed) {
		a.waitStopped()
		return ErrServerClosed
	}
	return err
}

// ListenTLS is Listen for TLS: it serves HTTPS on a TCP address until Shutdown or Close, and
// returns ErrServerClosed then. cfg is checked first and a mistake in it panics (see ServeTLS);
// an error from listening is returned.
func (a *App) ListenTLS(addr string, cfg *tls.Config, opts ...ListenOption) error {
	a.checkTLS("ListenTLS", cfg)
	px := a.proxyFor("ListenTLS", false, opts)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return a.serveWith(ln, cfg, px)
}

// ServeTLS is Serve for TLS (ADR 0017): each connection handshakes on its own goroutine within
// Limits.HandshakeTimeout before its first request, and only HTTP/1.1 is negotiated for traffic.
// The config is refused with a panic, before anything is served, when it is nil, has no
// certificate source (Certificates, GetCertificate or GetConfigForClient), offers an application
// protocol other than "http/1.1" and "acme-tls/1" in NextProtos (h2 above all: the engine would
// speak HTTP/1 to a client that agreed to h2), offers "acme-tls/1" with only static Certificates,
// or verifies client certificates (ClientAuth) with no ClientCAs (ADR 0066). A tls-alpn-01
// challenge on "acme-tls/1" is answered by the handshake and its connection closed. Loading
// certificate files is the caller's: tls.LoadX509KeyPair returns its error there, and a Certs
// reloads them while serving (ADR 0067). ProxyProtocol among opts makes the listener read a
// PROXY header before each handshake (ADR 0058).
func (a *App) ServeTLS(ln net.Listener, cfg *tls.Config, opts ...ListenOption) error {
	a.checkTLS("ServeTLS", cfg)
	return a.serveWith(ln, cfg, a.proxyFor("ServeTLS", isUnix(ln), opts))
}

// logListener is the start-up line of one listener: the network, the address, whether it is
// TLS and whether it reads the PROXY protocol, so an app on several listeners says what each one
// is (ADR 0068, ADR 0058).
func (a *App) logListener(ln net.Listener, tls, proxy bool) {
	addr := ln.Addr()
	a.log.Info("bites: listening", "network", addr.Network(), "addr", addr.String(), "tls", tls, "proxy_protocol", proxy)
}

// checkTLS panics with a RegistrationError for a TLS setup that cannot work (ADR 0007): these are
// programming mistakes found before the first connection, not I/O.
func (a *App) checkTLS(fn string, cfg *tls.Config) {
	where := callerWhere()
	fail := func(format string, args ...any) {
		panic(&RegistrationError{msg: fn + ": " + fmt.Sprintf(format, args...) + "\n\tregistered at " + where})
	}
	if cfg == nil {
		fail("the TLS config is nil; give a *tls.Config with a certificate")
	}
	if len(cfg.Certificates) == 0 && cfg.GetCertificate == nil && cfg.GetConfigForClient == nil {
		fail("the TLS config has no certificate: set Certificates, GetCertificate or GetConfigForClient")
	}
	acme := false
	for _, p := range cfg.NextProtos {
		switch p {
		case "http/1.1":
		case acmeALPN:
			acme = true
		case "h2":
			fail(`the TLS config offers "h2" in NextProtos, but bites speaks HTTP/1.1 only until its HTTP/2 engine exists (ADR 0017); a client that negotiated h2 would be sent HTTP/1.1 it cannot read. Remove "h2", or leave NextProtos empty`)
		default:
			fail(`the TLS config offers %q in NextProtos, but bites negotiates only "http/1.1", and "acme-tls/1" for a certificate challenge (ADR 0017); remove it, or leave NextProtos empty`, p)
		}
	}
	if acme && cfg.GetCertificate == nil && cfg.GetConfigForClient == nil {
		fail(`the TLS config offers "acme-tls/1" in NextProtos but has only static Certificates, which cannot answer a tls-alpn-01 challenge: set GetCertificate to the ACME client's (autocert's Manager.GetCertificate), or remove "acme-tls/1" (ADR 0017)`)
	}
	if cfg.ClientAuth >= tls.VerifyClientCertIfGiven && cfg.ClientCAs == nil && cfg.GetConfigForClient == nil {
		fail("the TLS config verifies client certificates (ClientAuth %v) but has no ClientCAs, so crypto/tls would accept any certificate the system's roots trust, which is any public CA's: set ClientCAs to the CAs that issue your clients' certificates (ADR 0066)", cfg.ClientAuth)
	}
}

// acmeALPN is the protocol a CA's tls-alpn-01 validation offers (RFC 8737).
const acmeALPN = "acme-tls/1"

// Shutdown stops accepting, closes idle connections and waits for requests in flight; when ctx
// ends first the rest are closed and their contexts cancelled (ADR 0029).
//
// Readiness (WithHealth) fails from the moment Shutdown is called. With WithShutdownDelay, Shutdown
// first keeps serving for the delay, the listener open, and only then stops accepting (ADR 0052).
//
// With Services (ADR 0079) it first runs their BeforeShutdown functions, in reverse order, and
// after the drain their Stop functions in reverse order, with ctx. It returns the errors of all
// of them joined with the server's; Listen and Serve return only after the Stop functions have
// returned, so "go app.Shutdown(ctx)" followed by Listen is safe.
func (a *App) Shutdown(ctx context.Context) error {
	a.health.draining.Store(true)
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		return nil
	}
	errs := []error{a.beforeShutdown(ctx)}
	if a.health.delay > 0 {
		a.waitDelay(ctx)
	}
	errs = append(errs, srv.Shutdown(ctx))
	errs = append(errs, a.stopServices(ctx))
	return joinErrors(errs...)
}

// Close stops accepting and closes every connection at once. A shutdown delay in progress ends.
// Services are stopped with a context that is already done and no BeforeShutdown is called
// (ADR 0079); Close returns after their Stop functions have returned or been abandoned.
func (a *App) Close() error {
	a.health.closeDelay()
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		return nil
	}
	if len(a.life.services) == 0 {
		return srv.Close()
	}
	a.life.markClosed()
	wctx, cancel := context.WithTimeout(context.Background(), a.life.wait())
	a.life.waitStart(wctx)
	cancel()
	err := srv.Close()
	a.life.beforeOnce.Do(func() {})
	ctx, done := context.WithCancelCause(context.Background())
	done(ErrServerClosed)
	return joinErrors(err, a.stopServices(ctx))
}

// joinErrors is errors.Join, except that a single error is returned as it is, so a Shutdown whose
// only failure is the engine's returns ctx.Err() itself.
func joinErrors(errs ...error) error {
	var one error
	n := 0
	for _, e := range errs {
		if e != nil {
			one = e
			n++
		}
	}
	if n <= 1 {
		return one
	}
	return errors.Join(errs...)
}
