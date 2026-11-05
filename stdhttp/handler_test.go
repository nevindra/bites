package stdhttp_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/stdhttp"
)

type userIn struct {
	Path struct{ ID int64 }
}

type user struct {
	ID   int64
	Name string
}

func quietApp() *bites.App {
	return bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
}

func getUser(_ context.Context, in userIn) (user, error) {
	return user{ID: in.Path.ID, Name: "Ada"}, nil
}

func TestAnAppAsAHandlerAnswersATypedRouteThroughAResponseRecorder(t *testing.T) {
	app := quietApp()
	app.Get("/users/{id}", getUser)
	h := stdhttp.Handler(app)
	t.Cleanup(func() { app.Close() })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/users/7", nil))
	if rec.Code != 200 || rec.Body.String() != `{"id":7,"name":"Ada"}` || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	if rec.Header().Get("Content-Length") != "21" || rec.Header().Get("Connection") != "" {
		t.Fatalf("the head is the app's, without its hop-by-hop fields: %v", rec.Header())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/users/x", nil))
	if rec.Code != 400 || rec.Header().Get("Content-Type") != "application/problem+json" || !strings.Contains(rec.Body.String(), `"location":"path.id"`) {
		t.Fatalf("a path param that is not a number is the app's 400 naming path.id: %d %q", rec.Code, rec.Body.String())
	}
}

func TestAnAppAsAHandlerSeesTheBodyQueryHeadersHostAndTheClientAddressOverARealServer(t *testing.T) {
	type echoIn struct {
		Query  struct{ Q string }
		Header struct{ XTag string }
		Body   struct{ Text string }
	}
	var remote, host string
	app := quietApp()
	app.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		remote, host = req.RemoteAddr().Addr().String(), req.Host()
		return next.Call(ctx), nil
	})
	app.Post("/echo", func(_ context.Context, in echoIn) (map[string]string, error) {
		return map[string]string{"q": in.Query.Q, "tag": in.Header.XTag, "text": in.Body.Text}, nil
	})
	srv := httptest.NewServer(stdhttp.Handler(app))
	t.Cleanup(func() { srv.Close(); app.Close() })

	req, _ := http.NewRequest("POST", srv.URL+"/echo?q=one", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tag", "t1")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(b) != `{"q":"one","tag":"t1","text":"hello"}` {
		t.Fatalf("%d %q", res.StatusCode, b)
	}
	if remote != "127.0.0.1" || host != strings.TrimPrefix(srv.URL, "http://") {
		t.Fatalf("the app saw remote %q and host %q", remote, host)
	}
}

func TestABodyOfUnknownLengthIsSentToTheAppChunkedAndArrivesWhole(t *testing.T) {
	app := quietApp()
	app.Post("/len", func(_ context.Context, in struct{ Body []byte }) (int, error) { return len(in.Body), nil })
	srv := httptest.NewServer(stdhttp.Handler(app))
	t.Cleanup(func() { srv.Close(); app.Close() })

	pr, pw := io.Pipe()
	go func() {
		for range 100 {
			pw.Write([]byte(strings.Repeat("x", 1000)))
		}
		pw.Close()
	}()
	req, _ := http.NewRequest("POST", srv.URL+"/len", pr)
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if req.ContentLength != 0 && req.ContentLength != -1 || res.StatusCode != 200 || string(b) != "100000" {
		t.Fatalf("%d %q", res.StatusCode, b)
	}
}

func TestAnAppBehindStripPrefixInAServeMuxServesItsRoutesBesideTheMuxsOwnHandlers(t *testing.T) {
	app := quietApp()
	app.Get("/users/{id}", getUser)
	mux := http.NewServeMux()
	mux.Handle("/v2/", http.StripPrefix("/v2", stdhttp.Handler(app)))
	mux.HandleFunc("/v1/users/{id}", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "old "+r.PathValue("id")) })
	t.Cleanup(func() { app.Close() })

	for target, want := range map[string]string{"/v2/users/7": `{"id":7,"name":"Ada"}`, "/v1/users/7": "old 7"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != 200 || rec.Body.String() != want {
			t.Errorf("%s: %d %q", target, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v2/nothing", nil))
	if rec.Code != 404 || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("a path the app does not have is the app's 404: %d %v", rec.Code, rec.Header())
	}
}

func TestAnSSEReachesTheClientEventByEventThroughTheHandler(t *testing.T) {
	release := make(chan struct{})
	app := quietApp()
	app.Get("/events", func(context.Context, struct{}) (bites.SSE, error) {
		return bites.SSE{Func: func(ctx context.Context, e bites.EventWriter) error {
			if err := e.Send(bites.Event{Data: "first"}); err != nil {
				return err
			}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return e.Send(bites.Event{Data: "second"})
		}}, nil
	})
	srv := httptest.NewServer(stdhttp.Handler(app))
	t.Cleanup(func() { srv.Close(); app.Close() })

	res, err := srv.Client().Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", res.Header.Get("Content-Type"))
	}
	br := bufio.NewReader(res.Body)
	line, err := br.ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("the first event arrived before the second was written: %q %v", line, err)
	}
	close(release)
	rest, _ := io.ReadAll(br)
	if !strings.Contains(string(rest), "data: second\n") {
		t.Fatalf("then the second: %q", rest)
	}
}

func TestTheAppsContextIsCancelledWhenTheNetHTTPClientLeaves(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan error, 1)
	app := quietApp()
	app.Get("/slow", func(ctx context.Context, _ struct{}) (string, error) {
		close(entered)
		<-ctx.Done()
		cancelled <- bites.Cause(ctx)
		return "", ctx.Err()
	})
	srv := httptest.NewServer(stdhttp.Handler(app))
	t.Cleanup(func() { srv.Close(); app.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/slow", nil)
	go func() { <-entered; cancel() }()
	if _, err := srv.Client().Do(req); err == nil {
		t.Fatal("the request was answered")
	}
	select {
	case err := <-cancelled:
		if !errors.Is(err, bites.ErrClientGone) {
			t.Fatalf("the cause is %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler's context was not cancelled")
	}
}

func TestAWebSocketRouteWorksUnderAnHTTP1ServerAndA501UnderARecorder(t *testing.T) {
	app := quietApp()
	type msg struct{ Text string }
	app.WebSocket("/ws", func(_ context.Context, _ struct{}, s *bites.Socket[msg]) (bites.Inbox[msg], error) {
		return bites.Inbox[msg]{OnMessage: func(_ context.Context, m msg) error { return s.Send(msg{"echo " + m.Text}) }}, nil
	})
	h := stdhttp.Handler(app)
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); app.Close() })

	c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(c, "GET /ws HTTP/1.1\r\nHost: test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, nil)
	if err != nil || res.StatusCode != 101 || res.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("handshake: %v %+v", err, res)
	}
	c.Write(maskedText(`{"text":"hi"}`))
	op, payload := readFrame(t, br)
	if op != 1 || string(payload) != `{"text":"echo hi"}` {
		t.Fatalf("frame %d %q", op, payload)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	h.ServeHTTP(rec, req)
	if rec.Code != 501 || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("a recorder cannot hijack: %d %q", rec.Code, rec.Body.String())
	}
}

func maskedText(s string) []byte {
	var key [4]byte
	rand.Read(key[:])
	f := []byte{0x81, 0x80 | byte(len(s))}
	f = append(f, key[:]...)
	for i := range len(s) {
		f = append(f, s[i]^key[i%4])
	}
	return f
}

func readFrame(t *testing.T, br *bufio.Reader) (byte, []byte) {
	t.Helper()
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		t.Fatal(err)
	}
	n := int(h[1] & 0x7f)
	if n == 126 {
		var l [2]byte
		io.ReadFull(br, l[:])
		n = int(binary.BigEndian.Uint16(l[:]))
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(br, p); err != nil {
		t.Fatal(err)
	}
	return h[0] & 0x0f, p
}

func TestAfterShutdownTheHandlerAnswers503ProblemJSON(t *testing.T) {
	app := quietApp()
	app.Get("/users/{id}", getUser)
	h := stdhttp.Handler(app)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/users/7", nil))
	if rec.Code != 503 || rec.Header().Get("Content-Type") != "application/problem+json" || !strings.Contains(rec.Body.String(), `"status":503`) {
		t.Fatalf("%d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
}

func TestAHeaderThatWouldSplitTheHeadIsA400AndNeverReachesTheApp(t *testing.T) {
	reached := false
	app := quietApp()
	app.Get("/x", func(context.Context, struct{}) (string, error) { reached = true; return "", nil })
	h := stdhttp.Handler(app)
	t.Cleanup(func() { app.Close() })
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header["X-Bad"] = []string{"a\r\nX-Injected: 1"}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 || reached || !strings.Contains(rec.Body.String(), `X-Bad`) {
		t.Fatalf("%d reached=%v %q", rec.Code, reached, rec.Body.String())
	}
}

func TestAnAnswerBeforeTheBodyIsReadDoesNotWaitForASlowClient(t *testing.T) {
	app := quietApp()
	app.Post("/small", func(_ context.Context, in struct{ Body []byte }) (int, error) { return len(in.Body), nil }, bites.MaxBody(1024))
	srv := httptest.NewServer(stdhttp.Handler(app))
	t.Cleanup(func() { srv.Close(); app.Close() })

	pr, pw := io.Pipe()
	defer pw.Close()
	go pw.Write(make([]byte, 4096)) // then nothing more: a client that stalls
	req, _ := http.NewRequest("POST", srv.URL+"/small", pr)
	req.ContentLength = 1 << 20
	req.Header.Set("Content-Type", "application/octet-stream")
	done := make(chan *http.Response, 1)
	go func() {
		res, err := srv.Client().Do(req)
		if err != nil {
			done <- nil
			return
		}
		res.Body.Close()
		done <- res
	}()
	select {
	case res := <-done:
		if res == nil || res.StatusCode != 413 {
			t.Fatalf("%+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the 413 waited for the client's body")
	}
}

func TestARouteRegisteredAfterHandlerPanicsBecauseTheAppIsServing(t *testing.T) {
	app := quietApp()
	app.Get("/a/{id}", getUser)
	stdhttp.Handler(app)
	t.Cleanup(func() { app.Close() })
	defer func() {
		var re *bites.RegistrationError
		if r := recover(); r == nil {
			t.Fatal("no panic")
		} else if err, ok := r.(error); !ok || !errors.As(err, &re) {
			t.Fatalf("panic %v", r)
		}
	}()
	app.Get("/b/{id}", getUser)
}

func TestHandlerOfANilAppPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "nil") {
			t.Fatalf("panic %v", r)
		}
	}()
	stdhttp.Handler(nil)
}

func TestARequestThroughHandlerAllocationsAreMeasuredAndStated(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	app := quietApp()
	app.Get("/users/{id}", getUser)
	h := stdhttp.Handler(app)
	t.Cleanup(func() { app.Close() })
	req := httptest.NewRequest("GET", "/users/7", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	n := testing.AllocsPerRun(500, func() { h.ServeHTTP(httptest.NewRecorder(), req) })
	t.Logf("allocations a typed GET through Handler: %.0f", n)
	if n > allocsHandlerGET {
		t.Fatalf("%.0f allocations, documented %d", n, allocsHandlerGET)
	}
}
