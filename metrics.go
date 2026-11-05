package bites

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/router"
)

// MetricsOptions configures NewMetrics.
type MetricsOptions struct {
	// Buckets are the upper bounds of the duration histogram, in increasing order. Nil is the
	// Prometheus default: 5, 10, 25, 50, 100, 250 and 500 ms, 1, 2.5, 5 and 10 s.
	Buckets []time.Duration
	// MaxSeries is the most label sets (method, route, code) the Metrics keeps; a request that
	// would add one more is counted in bites_metrics_dropped_total instead. Zero is 10,000. The
	// label set is bounded by the app's routes already (ADR 0060); this is the hard ceiling behind
	// that bound.
	MaxSeries int
}

// defaultBuckets are the default upper bounds of Prometheus' client libraries.
var defaultBuckets = []time.Duration{
	5 * time.Millisecond, 10 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond,
	100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond,
	time.Second, 2500 * time.Millisecond, 5 * time.Second, 10 * time.Second,
}

const defaultMaxSeries = 10000

// Metrics is an Observer (ADR 0051) that counts requests by method, route pattern and status
// code and measures their duration, and writes what it counted in the Prometheus text exposition
// format, version 0.0.4, with no dependency (ADR 0060). Give it to the app with WithObserver and
// serve it with App.ServeMetrics:
//
//	m := bites.NewMetrics()
//	app := bites.New(bites.WithObserver(bites.AccessLog(), m))
//	app.ServeMetrics("/metrics", m)
//
// The families are http_server_request_duration_seconds (a histogram, from the parsed head to
// the last byte), http_server_request_body_bytes_total and http_server_response_body_bytes_total
// (counters, the response's after gzip), all three labelled method, route and code;
// http_server_abandoned_requests_total, labelled method and route; http_server_requests_active, a
// gauge; and bites_metrics_dropped_total.
//
// Metrics is also a SocketObserver (ADR 0063) and counts WebSockets by route: the gauge
// bites_websocket_connections_open, the counters bites_websocket_connections_opened_total,
// bites_websocket_connections_closed_total (also labelled reason, one of the CloseCause strings:
// client, server, handler_error, queue_full, pong_timeout, shutdown, client_fault, lost) and
// bites_websocket_pongs_missed_total. The route label is a registered pattern, so these are
// bounded by the app, and they share MaxSeries with the request series. They cost nothing on the
// HTTP request path.
//
// The labels are bounded by the app, never by the client: route is a registered pattern, or ""
// for a 404, a 405 and an answer given before the match; method is one of the nine standard
// methods or "OTHER"; code is a status the app answered. A request abandoned before anything was
// sent has no status and is counted only in http_server_abandoned_requests_total.
//
// End costs a map lookup and a few atomic additions and allocates nothing once its label set
// exists; the first request of a label set copies the map of series. One Metrics may observe
// several apps; their series are added together.
type Metrics struct {
	bounds []int64  // the buckets' upper bounds, in nanoseconds
	les    []string // the same, as the le label writes them, in seconds
	max    int

	series  atomic.Pointer[map[seriesKey]*series] // copied on write, read without a lock
	mu      sync.Mutex                            // held to add a series
	active  atomic.Int64
	dropped atomic.Uint64

	socks atomic.Pointer[map[string]*sockSeries] // by route, copied on write like series
}

// sockSeries is one WebSocket route's counts (ADR 0063).
type sockSeries struct {
	open   atomic.Int64
	opened atomic.Uint64
	missed atomic.Uint64
	closed [wsCauses]atomic.Uint64 // by CloseCause - 1
}

// wsCauses is the number of CloseCause values.
const wsCauses = int(CauseLost)

type seriesKey struct {
	method, route string
	code          int32
}

type series struct {
	buckets   []atomic.Uint64 // one per bound, then +Inf; not cumulative
	sum       atomic.Int64    // nanoseconds
	read      atomic.Uint64
	written   atomic.Uint64
	abandoned atomic.Uint64
}

// NewMetrics returns a Metrics. It panics when more than one options value is given, a bucket is
// not positive or not above the one before, or MaxSeries is negative.
func NewMetrics(opts ...MetricsOptions) *Metrics {
	var o MetricsOptions
	switch len(opts) {
	case 0:
	case 1:
		o = opts[0]
	default:
		refuseMiddleware("NewMetrics", "%d options values were given; give one MetricsOptions", len(opts))
	}
	bs := o.Buckets
	if bs == nil {
		bs = defaultBuckets
	}
	if len(bs) == 0 {
		refuseMiddleware("NewMetrics", "Buckets is empty: give at least one upper bound, or nil for the default")
	}
	for i, b := range bs {
		if b <= 0 {
			refuseMiddleware("NewMetrics", "Buckets[%d] is %v, which is not an upper bound: give positive durations in increasing order", i, b)
		}
		if i > 0 && b <= bs[i-1] {
			refuseMiddleware("NewMetrics", "Buckets[%d] is %v, which is not above Buckets[%d] (%v): give the bounds in increasing order, each once", i, b, i-1, bs[i-1])
		}
	}
	if o.MaxSeries < 0 {
		refuseMiddleware("NewMetrics", "MaxSeries is %d, which is not a limit: give a positive number of series, or zero for the default of 10,000", o.MaxSeries)
	}
	m := &Metrics{max: cmp.Or(o.MaxSeries, defaultMaxSeries)}
	for _, b := range bs {
		m.bounds = append(m.bounds, int64(b))
		m.les = append(m.les, strconv.FormatFloat(b.Seconds(), 'f', -1, 64))
	}
	empty := map[seriesKey]*series{}
	m.series.Store(&empty)
	noSocks := map[string]*sockSeries{}
	m.socks.Store(&noSocks)
	return m
}

// sock finds the counts of a WebSocket route, adding them when there is room; nil when MaxSeries is
// reached, in which case the event is counted in bites_metrics_dropped_total.
func (m *Metrics) sock(route string) *sockSeries {
	if s := (*m.socks.Load())[route]; s != nil {
		return s
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old := *m.socks.Load()
	if s := old[route]; s != nil {
		return s
	}
	if len(old) >= m.max {
		m.dropped.Add(1)
		return nil
	}
	s := &sockSeries{}
	next := make(map[string]*sockSeries, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	next[route] = s
	m.socks.Store(&next)
	return s
}

// SocketOpened counts the socket as open.
func (m *Metrics) SocketOpened(route string) {
	if s := m.sock(route); s != nil {
		s.open.Add(1)
		s.opened.Add(1)
	}
}

// SocketClosed counts the close under its cause, and the socket as no longer open.
func (m *Metrics) SocketClosed(route string, info CloseInfo) {
	if s := m.sock(route); s != nil {
		s.open.Add(-1)
		if i := int(info.Cause) - 1; i >= 0 && i < wsCauses {
			s.closed[i].Add(1)
		}
	}
}

// SocketPongMissed counts a ping the client did not answer.
func (m *Metrics) SocketPongMissed(route string) {
	if s := m.sock(route); s != nil {
		s.missed.Add(1)
	}
}

// Start counts the request as active.
func (m *Metrics) Start(ctx context.Context, _ Request) context.Context {
	m.active.Add(1)
	return ctx
}

// End counts the request in its series.
func (m *Metrics) End(_ context.Context, req Request, out Outcome) {
	m.active.Add(-1)
	s := m.lookup(seriesKey{method: metricMethod(req.Method()), route: out.Route, code: int32(out.Status)})
	if s == nil {
		m.dropped.Add(1)
		return
	}
	if out.Abandoned {
		s.abandoned.Add(1)
	}
	if out.Status == 0 {
		return // nothing was answered: no code to count it under
	}
	d := int64(out.Duration)
	i := 0
	for i < len(m.bounds) && d > m.bounds[i] {
		i++
	}
	s.buckets[i].Add(1)
	s.sum.Add(d)
	s.read.Add(uint64(max(out.Read, 0)))
	s.written.Add(uint64(max(out.Written, 0)))
}

// lookup finds the series of k, adding it when there is room; nil when MaxSeries is reached.
func (m *Metrics) lookup(k seriesKey) *series {
	if s := (*m.series.Load())[k]; s != nil {
		return s
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old := *m.series.Load()
	if s := old[k]; s != nil {
		return s
	}
	if len(old) >= m.max {
		return nil
	}
	s := &series{buckets: make([]atomic.Uint64, len(m.bounds)+1)}
	next := make(map[seriesKey]*series, len(old)+1)
	for kk, v := range old {
		next[kk] = v
	}
	next[k] = s
	m.series.Store(&next)
	return s
}

// metricMethod bounds the method label: a client may send any token as a method.
func metricMethod(m string) string {
	switch m {
	case "GET", "HEAD", "POST", "PUT", "DELETE", "CONNECT", "OPTIONS", "TRACE", "PATCH":
		return m
	}
	return "OTHER"
}

// MetricsContentType is the Content-Type of the Prometheus text exposition format, version 0.0.4.
const MetricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// WriteTo writes the metrics in the Prometheus text exposition format, version 0.0.4, to w: the
// series sorted by method, route and code, so two scrapes of the same counts are the same bytes.
func (m *Metrics) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(m.appendText(nil))
	return int64(n), err
}

func (m *Metrics) appendText(b []byte) []byte {
	snap := *m.series.Load()
	keys := make([]seriesKey, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b seriesKey) int {
		return cmp.Or(cmp.Compare(a.method, b.method), cmp.Compare(a.route, b.route), cmp.Compare(a.code, b.code))
	})
	labels := func(b []byte, k seriesKey, withCode bool) []byte {
		b = append(b, `method="`...)
		b = appendLabelValue(b, k.method)
		b = append(b, `",route="`...)
		b = appendLabelValue(b, k.route)
		b = append(b, '"')
		if withCode {
			b = append(b, `,code="`...)
			b = strconv.AppendInt(b, int64(k.code), 10)
			b = append(b, '"')
		}
		return b
	}

	b = append(b, "# HELP http_server_requests_active Requests being served: their head was parsed and their answer has not ended.\n# TYPE http_server_requests_active gauge\nhttp_server_requests_active "...)
	b = strconv.AppendInt(b, m.active.Load(), 10)
	b = append(b, '\n')

	const dur = "http_server_request_duration_seconds"
	b = append(b, "# HELP "+dur+" Time from the parsed head to the last byte of the answer.\n# TYPE "+dur+" histogram\n"...)
	for _, k := range keys {
		if k.code == 0 {
			continue
		}
		s := snap[k]
		var cum uint64
		for i := range s.buckets {
			cum += s.buckets[i].Load()
			b = append(b, dur+"_bucket{"...)
			b = labels(b, k, true)
			b = append(b, `,le="`...)
			if i < len(m.les) {
				b = append(b, m.les[i]...)
			} else {
				b = append(b, "+Inf"...)
			}
			b = append(b, `"} `...)
			b = strconv.AppendUint(b, cum, 10)
			b = append(b, '\n')
		}
		b = append(b, dur+"_sum{"...)
		b = labels(b, k, true)
		b = append(b, "} "...)
		b = strconv.AppendFloat(b, float64(s.sum.Load())/1e9, 'f', -1, 64)
		b = append(b, '\n')
		b = append(b, dur+"_count{"...)
		b = labels(b, k, true)
		b = append(b, "} "...)
		b = strconv.AppendUint(b, cum, 10)
		b = append(b, '\n')
	}

	for _, c := range []struct {
		name, help string
		get        func(*series) uint64
	}{
		{"http_server_request_body_bytes_total", "Body bytes read from the client.", func(s *series) uint64 { return s.read.Load() }},
		{"http_server_response_body_bytes_total", "Body bytes written to the client, after gzip.", func(s *series) uint64 { return s.written.Load() }},
	} {
		b = append(b, "# HELP "+c.name+" "+c.help+"\n# TYPE "+c.name+" counter\n"...)
		for _, k := range keys {
			if k.code == 0 {
				continue
			}
			b = append(b, c.name+"{"...)
			b = labels(b, k, true)
			b = append(b, "} "...)
			b = strconv.AppendUint(b, c.get(snap[k]), 10)
			b = append(b, '\n')
		}
	}

	const ab = "http_server_abandoned_requests_total"
	b = append(b, "# HELP "+ab+" Requests whose client left, or stopped reading, before the answer reached it whole.\n# TYPE "+ab+" counter\n"...)
	for i := 0; i < len(keys); {
		k := keys[i]
		var n uint64
		for ; i < len(keys) && keys[i].method == k.method && keys[i].route == k.route; i++ {
			n += snap[keys[i]].abandoned.Load()
		}
		b = append(b, ab+"{"...)
		b = labels(b, k, false)
		b = append(b, "} "...)
		b = strconv.AppendUint(b, n, 10)
		b = append(b, '\n')
	}

	b = m.appendSockets(b)

	b = append(b, "# HELP bites_metrics_dropped_total Requests not counted because MaxSeries was reached.\n# TYPE bites_metrics_dropped_total counter\nbites_metrics_dropped_total "...)
	b = strconv.AppendUint(b, m.dropped.Load(), 10)
	return append(b, '\n')
}

// appendLabelValue escapes a label value as the text format requires: a backslash, a double
// quote and a line feed.
func appendLabelValue(b []byte, v string) []byte {
	if !strings.ContainsAny(v, "\\\"\n") {
		return append(b, v...)
	}
	for i := 0; i < len(v); i++ {
		switch c := v[i]; c {
		case '\\':
			b = append(b, `\\`...)
		case '"':
			b = append(b, `\"`...)
		case '\n':
			b = append(b, `\n`...)
		default:
			b = append(b, c)
		}
	}
	return b
}

// ServeMetrics answers GET path with what m counted, in the Prometheus text exposition format
// (ADR 0060). Like the API document of OpenAPI it is a route of the app: app middleware runs
// before it (put an auth check there, or serve it from a second App on an internal listener,
// since one Metrics can be given to one app and served by another), it is observed like any
// route, and it is not in the API document. It panics when m is nil, the path does not start
// with a slash or has a wildcard, query or fragment, or a route is already at the path.
func (a *App) ServeMetrics(path string, m *Metrics) {
	where := callerWhere()
	label := "ServeMetrics " + path
	fail := func(format string, args ...any) {
		panic(&RegistrationError{msg: label + ": " + fmt.Sprintf(format, args...) + "\n\tregistered at " + where})
	}
	switch {
	case m == nil:
		fail("the Metrics is nil: make one with NewMetrics and give the same one to WithObserver")
	case !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "{}?#"):
		fail("the path must start with a slash and have no wildcard, query or fragment")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.serving {
		panic(&RegistrationError{msg: label + " registered at " + where + " after the app started serving: routes can be added only before it does"})
	}
	rt := a.addRoute("GET", path, label, where)
	a.methods["GET"] = true
	a.eachBuiltin(rt, BuiltinMetrics, path, label, where)
	rt.serve = func(_ *App, ex *engine.Exchange, _ *router.Params, _ context.Context, _ *Request) {
		r := ex.Respond(200)
		r.Header("Content-Type", MetricsContentType)
		b := r.Body()
		*b = m.appendText(*b)
	}
}

// appendSockets writes the WebSocket families (ADR 0063), the routes sorted so that two scrapes of
// the same counts are the same bytes.
func (m *Metrics) appendSockets(b []byte) []byte {
	snap := *m.socks.Load()
	routes := make([]string, 0, len(snap))
	for r := range snap {
		routes = append(routes, r)
	}
	slices.Sort(routes)
	route := func(b []byte, r string) []byte {
		b = append(b, `route="`...)
		b = appendLabelValue(b, r)
		return append(b, '"')
	}
	family := func(b []byte, name, help, kind string, get func(*sockSeries) []byte) []byte {
		b = append(b, "# HELP "+name+" "+help+"\n# TYPE "+name+" "+kind+"\n"...)
		for _, r := range routes {
			b = append(b, name+"{"...)
			b = route(b, r)
			b = append(b, "} "...)
			b = append(b, get(snap[r])...)
			b = append(b, '\n')
		}
		return b
	}
	b = family(b, "bites_websocket_connections_open", "WebSockets open: their 101 was written and they have not ended.", "gauge",
		func(s *sockSeries) []byte { return strconv.AppendInt(nil, s.open.Load(), 10) })
	b = family(b, "bites_websocket_connections_opened_total", "WebSockets opened.", "counter",
		func(s *sockSeries) []byte { return strconv.AppendUint(nil, s.opened.Load(), 10) })
	const cl = "bites_websocket_connections_closed_total"
	b = append(b, "# HELP "+cl+" WebSockets closed, by why: client, server, handler_error, queue_full, pong_timeout, shutdown, client_fault or lost.\n# TYPE "+cl+" counter\n"...)
	for _, r := range routes {
		for i := range snap[r].closed {
			b = append(b, cl+"{"...)
			b = route(b, r)
			b = append(b, `,reason="`...)
			b = append(b, CloseCause(i+1).String()...)
			b = append(b, `"} `...)
			b = strconv.AppendUint(b, snap[r].closed[i].Load(), 10)
			b = append(b, '\n')
		}
	}
	return family(b, "bites_websocket_pongs_missed_total", "Pings a WebSocket did not answer before the next heartbeat tick.", "counter",
		func(s *sockSeries) []byte { return strconv.AppendUint(nil, s.missed.Load(), 10) })
}
