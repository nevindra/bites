package stdhttp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/internal/memconn"
	"github.com/nevindra/bites/internal/servehook"
)

// Handler returns app as an http.Handler, for a net/http server that serves it beside other
// handlers (one route at a time moved to bites behind an existing mux) and for a test that
// drives it with httptest (ADR 0019). It is the slow path the other way round from Mount: the
// app is not run by net/http, it is served on an in-memory listener as bitetest serves it
// (ADR 0027), and each request is written to it as HTTP/1.1 and its answer read back, so every
// behaviour of the app (limits, middleware, refusals, gzip, problem+json, streams and SSE)
// is the one it has on its own listener. A request costs a connection in memory and the
// engine's goroutine for it, outside the allocation budget of ADR 0003.
//
// Handler starts serving the app, as Listen does: a route registered after it panics, so call
// it after the last route. Shutdown and Close stop it with the app's other listeners (ADR 0068);
// a request after that is a 503 problem+json. It panics for a nil app, and with the error of a Service whose Start failed (ADR 0079).
//
//   - The app sees the request's method, the path and query of its URL (so http.StripPrefix in
//     front of it works), its Host, every header but the hop-by-hop ones, its body, streamed,
//     and its RemoteAddr as the peer, so TrustedProxies decide on the real client (ADR 0025).
//     When the client leaves, the app's context is cancelled (ADR 0016).
//   - The answer's status, headers and body reach the ResponseWriter as the app wrote them; a
//     body of unknown length (a Stream, an SSE) is flushed as each piece arrives. A body cut by
//     the app (a stream that failed) aborts the response with http.ErrAbortHandler, so the
//     client sees a cut answer, not a complete one.
//   - A WebSocket upgrade takes over the connection with Hijack and joins it to the app's, so a
//     WebSocket route works under an HTTP/1.1 net/http server (httptest.NewServer among them). A
//     ResponseWriter that cannot hijack (HTTP/2, httptest.ResponseRecorder) gets a 501.
//   - What it fails at: the app sees no TLS, so Request.Scheme is "http" unless a trusted proxy
//     says otherwise, even when the net/http server terminated TLS itself; trailers are dropped.
func Handler(app *bites.App) http.Handler {
	if app == nil {
		panic("stdhttp.Handler: the app is nil")
	}
	ln := memconn.Listen(app.Clock())
	serve, err := servehook.Start(app, ln)
	if err != nil {
		panic(err)
	}
	go serve()
	return &bridge{ln: ln}
}

// readers and buffers hold what reading an answer needs, kept between requests; neither reaches
// the handler or the client (ADR 0008).
var (
	readers = sync.Pool{New: func() any { return bufio.NewReaderSize(nil, 4096) }}
	buffers = sync.Pool{New: func() any { b := make([]byte, 32<<10); return &b }}
)

// bridge is the http.Handler of an app served on ln.
type bridge struct{ ln *memconn.Listener }

func (b *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isUpgrade(r) {
		b.upgrade(w, r)
		return
	}
	conn, err := b.ln.Dial(memconn.Addr{Name: r.RemoteAddr})
	if err != nil {
		problem(w, http.StatusServiceUnavailable, "the app is not serving")
		return
	}
	defer conn.Close()
	stop := context.AfterFunc(r.Context(), func() { conn.Close() })
	defer stop()

	head, chunked, err := writeHead(r, false)
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		if _, err := conn.Write(head); err != nil {
			return
		}
		sendBody(conn, r, chunked)
	}()
	defer func() {
		conn.Close()
		select {
		case <-sent:
		default:
			// The app answered before it read the whole body (a 413, a refusal): the copy may be
			// blocked reading a slow client, which only a read deadline unblocks.
			http.NewResponseController(w).SetReadDeadline(time.Now())
			<-sent
		}
	}()

	br := readers.Get().(*bufio.Reader)
	br.Reset(conn)
	defer func() { br.Reset(nil); readers.Put(br) }()
	res, err := http.ReadResponse(br, r)
	if err != nil {
		if r.Context().Err() != nil {
			return // the client left; there is nobody to answer
		}
		problem(w, http.StatusServiceUnavailable, "the app closed the connection without an answer: it is shutting down")
		return
	}
	defer res.Body.Close()
	h := w.Header()
	for name, values := range res.Header {
		if hopByHop(name) {
			continue
		}
		h[name] = append(h[name][:0:0], values...)
	}
	if res.ContentLength >= 0 && !bodyless(r.Method, res.StatusCode) {
		h.Set("Content-Length", strconv.FormatInt(res.ContentLength, 10))
	}
	w.WriteHeader(res.StatusCode)
	if err := copyBody(w, res.Body, res.ContentLength < 0); err != nil {
		if r.Context().Err() != nil {
			return
		}
		panic(http.ErrAbortHandler) // the app cut its answer: the client must not see a complete one
	}
}

// writeHead is the request's head as the app is sent it, and whether its body is chunked. A head
// for an upgrade keeps Connection and Upgrade; any other asks the app to close the connection
// after its answer, which is how the answer's end is known.
func writeHead(r *http.Request, upgrade bool) ([]byte, bool, error) {
	var b strings.Builder
	b.Grow(512)
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	b.WriteString(method)
	b.WriteByte(' ')
	b.WriteString(r.URL.RequestURI())
	b.WriteString(" HTTP/1.1\r\nHost: ")
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	b.WriteString(host)
	b.WriteString("\r\n")
	for name, values := range r.Header {
		if hopByHop(name) && !(upgrade && (strings.EqualFold(name, "Connection") || strings.EqualFold(name, "Upgrade"))) {
			continue
		}
		if strings.EqualFold(name, "Host") || strings.EqualFold(name, "Content-Length") || strings.EqualFold(name, "Expect") {
			continue
		}
		for _, v := range values {
			if !validField(name, v) {
				return nil, false, errors.New("the header " + strconv.Quote(name) + " has a value or name the app cannot be sent")
			}
			b.WriteString(name)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\r\n")
		}
	}
	chunked := false
	switch {
	case upgrade:
	case r.ContentLength > 0:
		b.WriteString("Content-Length: ")
		b.WriteString(strconv.FormatInt(r.ContentLength, 10))
		b.WriteString("\r\n")
	case r.ContentLength < 0 && r.Body != nil && r.Body != http.NoBody:
		b.WriteString("Transfer-Encoding: chunked\r\n")
		chunked = true
	}
	if !upgrade {
		b.WriteString("Connection: close\r\n")
	}
	b.WriteString("\r\n")
	return []byte(b.String()), chunked, nil
}

// sendBody copies the request's body to the app, chunked when its length is not known.
func sendBody(conn net.Conn, r *http.Request, chunked bool) {
	if r.Body == nil || r.Body == http.NoBody || (r.ContentLength == 0 && !chunked) {
		return
	}
	if !chunked {
		io.Copy(conn, io.LimitReader(r.Body, r.ContentLength))
		return
	}
	cw := httputil.NewChunkedWriter(conn)
	if _, err := io.Copy(cw, r.Body); err != nil {
		return
	}
	cw.Close()
	io.WriteString(conn, "\r\n") // the empty trailer section
}

// copyBody writes the answer's body to w, flushing each piece of a body of unknown length so a
// stream reaches the client as the app writes it.
func copyBody(w http.ResponseWriter, body io.Reader, flush bool) error {
	rc := http.NewResponseController(w)
	bp := buffers.Get().(*[]byte)
	defer buffers.Put(bp)
	buf := *bp
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if flush {
				rc.Flush()
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// upgrade joins a hijacked connection to one of the app's, for a WebSocket: the app answers the
// upgrade itself and every byte after it is copied both ways until either side closes.
func (b *bridge) upgrade(w http.ResponseWriter, r *http.Request) {
	head, _, err := writeHead(r, true)
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	client, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		problem(w, http.StatusNotImplemented, "a WebSocket through stdhttp.Handler needs an HTTP/1.1 connection the ResponseWriter can hijack")
		return
	}
	defer client.Close()
	conn, err := b.ln.Dial(memconn.Addr{Name: r.RemoteAddr})
	if err != nil {
		body := problemBody(http.StatusServiceUnavailable, "the app is not serving")
		io.WriteString(client, "HTTP/1.1 503 Service Unavailable\r\nContent-Type: application/problem+json\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\nConnection: close\r\n\r\n"+string(body))
		return
	}
	defer conn.Close()
	if _, err := conn.Write(head); err != nil {
		return
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(conn, rw.Reader) // what the client sent, the bytes net/http had buffered first
		if hc, ok := conn.(interface{ CloseWrite() error }); ok {
			hc.CloseWrite()
		}
	}()
	io.Copy(client, conn)
	client.Close()
	conn.Close()
	wg.Wait()
}

func isUpgrade(r *http.Request) bool {
	if r.Header.Get("Upgrade") == "" {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
				return true
			}
		}
	}
	return false
}

// hopByHop names the fields of RFC 9110 section 7.6.1 that belong to one connection and are not
// passed across the bridge in either direction.
func hopByHop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

// bodyless is true for an answer that has no body whatever its Content-Length says.
func bodyless(method string, status int) bool {
	return method == http.MethodHead || status == http.StatusNoContent || status == http.StatusNotModified || (status >= 100 && status < 200)
}

// validField refuses a name or value that would split the head: net/http checks what a client
// sent, but a request built in a test is not checked.
func validField(name, value string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c <= ' ' || c == ':' || c >= 0x7f {
			return false
		}
	}
	for i := 0; i < len(value); i++ {
		if c := value[i]; c == '\r' || c == '\n' || c == 0 {
			return false
		}
	}
	return true
}

// problem answers with a problem+json the app did not write, when the app cannot be reached or
// the request cannot be sent to it, in the app's own shape (ADR 0012).
func problem(w http.ResponseWriter, status int, detail string) {
	body := problemBody(status, detail)
	h := w.Header()
	h.Set("Content-Type", "application/problem+json")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

func problemBody(status int, detail string) []byte {
	title, _ := json.Marshal(http.StatusText(status))
	d, _ := json.Marshal(detail)
	return []byte(`{"title":` + string(title) + `,"status":` + strconv.Itoa(status) + `,"detail":` + string(d) + "}")
}
