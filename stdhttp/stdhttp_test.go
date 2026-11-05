package stdhttp_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/stdhttp"
)

func start(t *testing.T, register func(*bites.App)) *bitetest.Server {
	t.Helper()
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	register(app)
	return bitetest.Start(t, app)
}

func TestAMountedHandlerSeesTheMethodPathQueryHeadersHostRemoteAndBody(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s q=%s raw=%s uri=%s host=%s remote=%s x=%v len=%d body=%s ctx=%v proto=%s",
			r.Method, r.URL.Path, r.URL.Query().Get("a"), r.URL.EscapedPath(), r.RequestURI, r.Host, r.RemoteAddr,
			r.Header["X-Tag"], r.ContentLength, b, r.Context().Err(), r.Proto)
	})
	srv := start(t, func(app *bites.App) { stdhttp.Mount(app, "/mnt/", h) })
	resp := srv.Do(bitetest.Request{Method: "POST", Target: "/mnt/a%20b/c?a=1&b=2", Body: []byte("hello"),
		Header: map[string][]string{"x-tag": {"one"}}})
	want := "POST /mnt/a b/c q=1 raw=/mnt/a%20b/c uri=/mnt/a%20b/c?a=1&b=2 host=test remote=203.0.113.9:4000 x=[one] len=5 body=hello ctx=<nil> proto=HTTP/1.1"
	if resp.Status != 200 || resp.Text() != want {
		t.Fatalf("%d %q\nwant %q", resp.Status, resp.Text(), want)
	}
	if ct := resp.Get("content-type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("the sniffed content type is %q", ct)
	}
	if resp.Get("content-length") == "" {
		t.Fatalf("a buffered answer carries a Content-Length: %v", resp.Header)
	}
	if got := srv.Do(bitetest.Request{Method: "GET", Target: "/mnt/x"}).Text(); !strings.HasPrefix(got, "GET /mnt/x ") {
		t.Fatalf("GET: %q", got)
	}
	if got := srv.Do(bitetest.Request{Method: "TRACE", Target: "/mnt/x"}); got.Status != 405 {
		t.Fatalf("TRACE answered %d", got.Status)
	}
}

func TestTheStatusesAHandlerChoosesAreKeptWhateverTheirBody(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("/s/nf", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	h.HandleFunc("/s/nc", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	h.HandleFunc("/s/nm", func(w http.ResponseWriter, r *http.Request) { w.Header().Set("ETag", `"v1"`); w.WriteHeader(304) })
	h.HandleFunc("/s/rd", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) })
	h.HandleFunc("/s/ck", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "a", Value: "1"})
		w.Header().Add("X-Many", "1")
		w.Header().Add("X-Many", "2")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})
	h.HandleFunc("/s/none", func(w http.ResponseWriter, r *http.Request) {})
	h.HandleFunc("/s/body204", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
		if _, err := w.Write([]byte("x")); err != http.ErrBodyNotAllowed {
			t.Errorf("write on a 204: %v", err)
		}
	})
	srv := start(t, func(app *bites.App) { stdhttp.Mount(app, "/s/", h) })
	for _, tc := range []struct {
		path   string
		status int
		check  func(bitetest.Response) bool
	}{
		{"/s/nf", 404, func(r bitetest.Response) bool { return strings.Contains(r.Text(), "404 page not found") }},
		{"/s/nc", 204, func(r bitetest.Response) bool { return len(r.Body) == 0 }},
		{"/s/nm", 304, func(r bitetest.Response) bool { return r.Get("etag") == `"v1"` }},
		{"/s/rd", 302, func(r bitetest.Response) bool { return r.Get("location") == "/elsewhere" }},
		{"/s/ck", 200, func(r bitetest.Response) bool {
			return r.Get("set-cookie") == "a=1" && len(r.Header["x-many"]) == 2 && r.Get("content-type") == "application/json" && r.Text() == "{}"
		}},
		{"/s/none", 200, func(r bitetest.Response) bool { return len(r.Body) == 0 }},
		{"/s/body204", 204, func(r bitetest.Response) bool { return len(r.Body) == 0 }},
	} {
		r := srv.Do(bitetest.Request{Method: "GET", Target: tc.path})
		if r.Status != tc.status || !tc.check(r) {
			t.Errorf("%s: %d %v %q", tc.path, r.Status, r.Header, r.Body)
		}
	}
}

func TestAHandlerThatFlushesStreamsChunkedAndTheClientSeesEachChunkBeforeTheNext(t *testing.T) {
	step := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := range 3 {
			fmt.Fprintf(w, "data: %d\n\n", i)
			fl.Flush()
			<-step
		}
	})
	srv := start(t, func(app *bites.App) { stdhttp.Mount(app, "/live", h) })
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	c.Write([]byte("GET /live HTTP/1.1\r\nHost: t\r\n\r\n"))
	buf := make([]byte, 4096)
	read := func() string {
		c.SetReadDeadline(time.Time{})
		n, err := c.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		return string(buf[:n])
	}
	head := read()
	for !strings.Contains(head, "data: 0") {
		head += read()
	}
	if !strings.Contains(head, "Transfer-Encoding: chunked") || !strings.Contains(strings.ToLower(head), "content-type: text/event-stream") {
		t.Fatalf("the head: %q", head)
	}
	step <- struct{}{}
	if got := read(); !strings.Contains(got, "data: 1") {
		t.Fatalf("second chunk: %q", got)
	}
	step <- struct{}{}
	if got := read(); !strings.Contains(got, "data: 2") {
		t.Fatalf("third chunk: %q", got)
	}
	step <- struct{}{}
	rest := ""
	for !strings.HasSuffix(rest, "0\r\n\r\n") {
		rest += read()
	}
}

func TestABodyOver32KiBIsStreamedAndArrivesWhole(t *testing.T) {
	payload := strings.Repeat("0123456789abcdef", 16<<10) // 256 KiB
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		for i := 0; i < len(payload); i += 5000 {
			w.Write([]byte(payload[i:min(i+5000, len(payload))]))
		}
	})
	srv := start(t, func(app *bites.App) { stdhttp.Mount(app, "/big", h) })
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/big"})
	if r.Status != 200 || r.Text() != payload {
		t.Fatalf("%d, %d bytes of %d", r.Status, len(r.Body), len(payload))
	}
}

func TestAHEADRequestGetsTheHeadOfTheGETAnswerAndHandlerFlushesDoNotStreamIt(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen", r.Method)
		w.Write([]byte("hello"))
		w.(http.Flusher).Flush()
		w.Write([]byte(" world"))
	})
	srv := start(t, func(app *bites.App) { stdhttp.Mount(app, "/h", h) })
	r := srv.Do(bitetest.Request{Method: "HEAD", Target: "/h"})
	if r.Status != 200 || r.Get("x-seen") != "HEAD" || len(r.Body) != 0 || r.Get("content-length") != "11" {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Body)
	}
}

func TestAPanicBeforeTheFirstFlushIsA500AndAfterItTheConnectionIsCut(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("/p/early", func(w http.ResponseWriter, r *http.Request) { panic("boom") })
	h.HandleFunc("/p/late", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		panic("boom")
	})
	srv := start(t, func(app *bites.App) { stdhttp.Mount(app, "/p/", h) })
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/p/early"}); r.Status != 500 {
		t.Fatalf("early: %d", r.Status)
	}
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	c.Write([]byte("GET /p/late HTTP/1.1\r\nHost: t\r\n\r\n"))
	all, _ := io.ReadAll(c)
	if !strings.Contains(string(all), "partial") || strings.HasSuffix(string(all), "0\r\n\r\n") {
		t.Fatalf("a cut stream must not end cleanly: %q", all)
	}
}

func TestTheRequestContextIsCancelledWhenTheClientLeavesAndNoGoroutineIsLeftBehind(t *testing.T) {
	started := make(chan struct{})
	ended := make(chan error, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		ended <- r.Context().Err()
	})
	before := runtime.NumGoroutine()
	srv := start(t, func(app *bites.App) { stdhttp.Mount(app, "/wait", h) })
	c := srv.Dial("203.0.113.9:4000")
	c.Write([]byte("GET /wait HTTP/1.1\r\nHost: t\r\n\r\n"))
	<-started
	c.Close()
	select {
	case err := <-ended:
		if err == nil {
			t.Fatal("no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler's context was not cancelled")
	}
	srv.Close()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Fatalf("%d goroutines before, %d after", before, n)
	}
}

func TestAMiddlewareThatAnswersInsteadOfAStreamLeavesNoHandlerBlocked(t *testing.T) {
	released := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		w.Write([]byte("blocks until the stream is gone"))
		close(released)
	})
	srv := start(t, func(app *bites.App) {
		stdhttp.Mount(app, "/m", h, bites.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			resp := next.Call(ctx)
			if resp.Streaming() {
				return bites.Problem(503, "no"), nil
			}
			return resp, nil
		}))
	})
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/m"}); r.Status != 503 {
		t.Fatalf("%d", r.Status)
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler is still blocked after its stream was replaced")
	}
}

func TestStripPrefixAndAFileServerWork(t *testing.T) {
	h := http.StripPrefix("/files", http.FileServer(http.Dir("testdata")))
	srv := start(t, func(app *bites.App) { stdhttp.Mount(app, "/files/", h) })
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/files/hello.txt"})
	if r.Status != 200 || strings.TrimSpace(r.Text()) != "hello from a file" {
		t.Fatalf("%d %q", r.Status, r.Body)
	}
	if r.Get("last-modified") == "" || !strings.HasPrefix(r.Get("content-type"), "text/plain") {
		t.Fatalf("%v", r.Header)
	}
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/files/hello.txt", Header: map[string][]string{"Range": {"bytes=0-4"}}}); r.Status != 206 || r.Text() != "hello" {
		t.Fatalf("range: %d %q", r.Status, r.Body)
	}
}

func TestAnUnsupportedResponseControllerCallReturnsErrNotSupported(t *testing.T) {
	var hijack, deadline error
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		_, _, hijack = rc.Hijack()
		deadline = rc.SetWriteDeadline(time.Now())
		if err := rc.Flush(); err != nil {
			t.Errorf("flush: %v", err)
		}
	})
	srv := start(t, func(app *bites.App) { stdhttp.Mount(app, "/rc", h) })
	srv.Do(bitetest.Request{Method: "GET", Target: "/rc"})
	if hijack == nil || deadline == nil {
		t.Fatalf("hijack %v, deadline %v", hijack, deadline)
	}
}

func TestAMountedRouteIsRefusedAtRegistrationLikeAnyOtherWhenItsPatternHasAMethod(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	stdhttp.Mount(bites.New(), "GET /x", http.NotFoundHandler())
}

func TestAMountInAGroupTakesTheGroupsPrefix(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		stdhttp.MountGroup(app.Group("/ops"), "/debug/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, r.URL.Path)
		}))
	})
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/ops/debug/vars"}); r.Text() != "/ops/debug/vars" {
		t.Fatalf("%q", r.Body)
	}
}

// allocs drives request over one connection and returns the mallocs per request, the way the
// root package's budget tests do (the response must have the same length every time).
func allocs(t *testing.T, h http.Handler, method, request string) float64 {
	t.Helper()
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	stdhttp.MountMethods(app, []string{method}, "/a", h)
	srv := bitetest.Start(t, app)
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	req := []byte(request)
	buf := make([]byte, 8192)
	want := 0
	do := func() {
		c.Write(req)
		if want == 0 {
			want, _ = c.Read(buf)
			return
		}
		io.ReadFull(c, buf[:want])
	}
	do()
	for range 100 {
		do()
	}
	return testing.AllocsPerRun(1000, do)
}

func TestAMountedRequestAllocationsAreMeasuredAndStated(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	small := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"ok":true}`) })
	get := allocs(t, small, "GET", "GET /a HTTP/1.1\r\nHost: x\r\nAccept: */*\r\n\r\n")
	post := allocs(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, `{"ok":true}`)
	}), "POST", "POST /a HTTP/1.1\r\nHost: x\r\nContent-Type: text/plain\r\nContent-Length: 1024\r\n\r\n"+strings.Repeat("x", 1024))
	flushed := allocs(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for range 3 {
			io.WriteString(w, "data: x\n\n")
			w.(http.Flusher).Flush()
		}
	}), "GET", "GET /a HTTP/1.1\r\nHost: x\r\n\r\n")
	t.Logf("allocations a request: GET %.0f, POST 1 KiB %.0f, GET with three flushes %.0f", get, post, flushed)
	if get > allocsGET || post > allocsPOST || flushed > allocsFlushed {
		t.Fatalf("GET %.0f (documented %d), POST %.0f (%d), flushed %.0f (%d)", get, allocsGET, post, allocsPOST, flushed, allocsFlushed)
	}
}

func TestMountedRoutesAreServedButLeftOutOfTheAPIDocument(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/users", func(context.Context, struct{}) ([]string, error) { return nil, nil })
		stdhttp.Mount(app, "/debug/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
		stdhttp.MountGroup(app.Group("/legacy"), "/x", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	})
	doc := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()
	if !strings.Contains(doc, `"/users"`) || strings.Contains(doc, "/debug/") || strings.Contains(doc, "/legacy") {
		t.Fatalf("%s", doc)
	}
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/debug/vars"}); r.Status != 200 || r.Text() != "ok" {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAMountedHandlerIsAnOrdinaryRouteToUseEachWithMethodAndPatternAndNoOperationID(t *testing.T) {
	var seen []string
	srv := start(t, func(app *bites.App) {
		app.UseEach(func(r bites.RouteInfo) (bites.Middleware, error) {
			seen = append(seen, r.Method+" "+r.Pattern+" id="+r.OperationID)
			if r.Method != "GET" {
				return nil, fmt.Errorf("only GET is allowed")
			}
			return func(ctx context.Context, _ bites.Request, next bites.Next) (bites.Response, error) {
				res := next.Call(ctx)
				res.SetHeader("X-Audited", "yes")
				return res, nil
			}, nil
		})
		stdhttp.MountMethods(app, []string{"GET"}, "/mnt/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hi") }))
	})
	if resp := srv.Do(bitetest.Request{Method: "GET", Target: "/mnt/x"}); resp.Status != 200 || resp.Get("x-audited") != "yes" {
		t.Fatalf("%d %v", resp.Status, resp.Header)
	}
	if len(seen) != 1 || seen[0] != "GET /mnt/ id=" {
		t.Fatalf("seen %q", seen)
	}
}
