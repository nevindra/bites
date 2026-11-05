package bites_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// Uploads (ADR 0044): the body read part by part by the handler, through the in-memory
// connections, with the hostile clients of the ADR's evidence.

const bnd = "XyZ-boundary"

type partSum struct {
	Name string `json:"name"`
	File string `json:"file"`
	Type string `json:"type"`
	Data string `json:"data"`
}

type UpIn struct {
	Query struct{ Tag bites.Opt[string] }
	Body  bites.Upload
}

type UpOut struct {
	Tag   string    `json:"tag"`
	Parts []partSum `json:"parts"`
}

// collect reads every part whole and answers with what it saw.
func collect(_ context.Context, in UpIn) (UpOut, error) {
	out := UpOut{Tag: in.Query.Tag.Or("")}
	for {
		p, err := in.Body.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		b, err := io.ReadAll(p)
		if err != nil {
			return out, err
		}
		out.Parts = append(out.Parts, partSum{p.Name, p.FileName, p.ContentType, string(b)})
	}
}

func field(name, value string) string {
	return "--" + bnd + "\r\nContent-Disposition: form-data; name=\"" + name + "\"\r\n\r\n" + value + "\r\n"
}

func fileField(name, file, ctype, data string) string {
	return "--" + bnd + "\r\nContent-Disposition: form-data; name=\"" + name + "\"; filename=\"" + file + "\"\r\nContent-Type: " + ctype + "\r\n\r\n" + data + "\r\n"
}

func closing() string { return "--" + bnd + "--\r\n" }

func upHead(method, target string, extra string) string {
	return method + " " + target + " HTTP/1.1\r\nHost: x\r\nContent-Type: multipart/form-data; boundary=" + bnd + "\r\n" + extra
}

func upRequest(target, body string) string {
	return upHead("POST", target, "Content-Length: "+strconv.Itoa(len(body))+"\r\n\r\n") + body
}

func chunked(body string, size int) string {
	var b strings.Builder
	for len(body) > 0 {
		n := min(size, len(body))
		fmt.Fprintf(&b, "%x\r\n%s\r\n", n, body[:n])
		body = body[n:]
	}
	b.WriteString("0\r\n\r\n")
	return b.String()
}

func registerUpload(opts ...bites.RouteOption) func(*bites.App) {
	return func(a *bites.App) {
		a.Post("/up", collect, append([]bites.RouteOption{bites.MaxBody(1 << 20)}, opts...)...)
	}
}

func doRaw(t *testing.T, srv *bitetest.Server, raw string) bitetest.Response {
	t.Helper()
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	fmt.Fprint(c, raw)
	return srv.Read(c, false)
}

func TestAnUploadIsReadPartByPartWithItsNamesTypesAndBytes(t *testing.T) {
	srv := start(t, registerUpload())
	body := field("title", "hello") + fileField("doc", "a.txt", "text/plain", "line one\r\nline two\r\n--"+bnd+"x\r\nend") + field("empty", "") + closing()
	r := doRaw(t, srv, upRequest("/up?tag=t1", body))
	want := `{"tag":"t1","parts":[{"name":"title","file":"","type":"","data":"hello"},` +
		`{"name":"doc","file":"a.txt","type":"text/plain","data":"line one\r\nline two\r\n--` + bnd + `x\r\nend"},` +
		`{"name":"empty","file":"","type":"","data":""}]}`
	if r.Status != 200 || r.Text() != want {
		t.Fatalf("%d\n%s\nwant\n%s", r.Status, r.Text(), want)
	}
}

func TestAChunkedUploadIsReadTheSameAndTheConnectionServesTheNextRequest(t *testing.T) {
	srv := start(t, registerUpload())
	body := field("a", strings.Repeat("x", 10000)) + field("b", "tail") + closing()
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	for range 2 {
		fmt.Fprint(c, upHead("POST", "/up", "Transfer-Encoding: chunked\r\n\r\n")+chunked(body, 777))
		r := srv.Read(c, false)
		if r.Status != 200 || !strings.Contains(r.Text(), `"data":"tail"`) || strings.Count(r.Text(), "x") != 10000 {
			t.Fatalf("%d %.200s", r.Status, r.Text())
		}
	}
}

func TestTwoUploadsPipelinedOnOneConnectionBothAnswerAfterTheEpilogueIsSkipped(t *testing.T) {
	srv := start(t, registerUpload())
	body := field("a", "1") + "--" + bnd + "--\r\nan epilogue that nobody reads"
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	fmt.Fprint(c, upRequest("/up", body)+upRequest("/up", field("b", "2")+closing()))
	// bitetest.Read buffers, so a pipelined pair is read by hand.
	var got strings.Builder
	buf := make([]byte, 4096)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for strings.Count(got.String(), "HTTP/1.1 200") < 2 {
		n, err := c.Read(buf)
		got.Write(buf[:n])
		if err != nil {
			t.Fatalf("%v\n%s", err, got.String())
		}
	}
	if !strings.Contains(got.String(), `"data":"1"`) || !strings.Contains(got.String(), `"data":"2"`) {
		t.Fatal(got.String())
	}
}

func TestTheOtherSectionsAndValidateRunBeforeTheHandlerAndTheBodyIsNotReadWhenTheyFail(t *testing.T) {
	var ran int
	type in struct {
		Query struct{ Page int }
		Body  bites.Upload
	}
	srv := start(t, func(a *bites.App) {
		a.Post("/up", func(context.Context, in) (struct{}, error) { ran++; return struct{}{}, nil }, bites.MaxBody(1<<20))
	})
	r := doRaw(t, srv, upRequest("/up?page=x", field("a", "1")+closing()))
	if r.Status != 400 || !strings.Contains(r.Text(), `"location":"query.page"`) || ran != 0 {
		t.Fatalf("%d %s ran=%d", r.Status, r.Text(), ran)
	}
	// With no problem found the handler runs, though it never touches the body.
	r = doRaw(t, srv, upRequest("/up?page=1", field("a", "1")+closing()))
	if r.Status != 200 || ran != 1 {
		t.Fatalf("%d %s ran=%d", r.Status, r.Text(), ran)
	}
}

type validatedUp struct {
	Body bites.Upload
}

func (validatedUp) Validate() error { return bites.Invalid("body", "refused by Validate") }

func TestValidateOnAnUploadInputRunsBeforeTheHandler(t *testing.T) {
	called := false
	srv := start(t, func(a *bites.App) {
		a.Post("/up", func(context.Context, validatedUp) (struct{}, error) { called = true; return struct{}{}, nil }, bites.MaxBody(1<<10))
	})
	r := doRaw(t, srv, upRequest("/up", field("a", "1")+closing()))
	if r.Status != 400 || !strings.Contains(r.Text(), "refused by Validate") || called {
		t.Fatalf("%d %s called=%v", r.Status, r.Text(), called)
	}
}

func TestAProviderStillFillsItsFieldOnAnUploadRoute(t *testing.T) {
	type who struct{ Name string }
	type in struct {
		Who  who
		Body bites.Upload
	}
	srv := start(t, func(a *bites.App) {
		a.Provide(func(context.Context, bites.Request, struct{}) (who, error) { return who{"ada"}, nil })
		a.Post("/up", func(_ context.Context, i in) (bites.Text, error) { return bites.Text{Body: i.Who.Name}, nil }, bites.MaxBody(1<<10))
	})
	if r := doRaw(t, srv, upRequest("/up", field("a", "1")+closing())); r.Text() != "ada" {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAPartOrUploadKeptPastTheHandlerFailsAndAPartKeptPastNextFails(t *testing.T) {
	var up bites.Upload
	var first, second bites.Part
	var stale, afterNext error
	srv := start(t, func(a *bites.App) {
		a.Post("/up", func(_ context.Context, in UpIn) (struct{}, error) {
			up = in.Body
			first, _ = in.Body.Next()
			second, _ = in.Body.Next()
			_, afterNext = first.Read(make([]byte, 4))
			_, _ = io.Copy(io.Discard, second)
			return struct{}{}, nil
		}, bites.MaxBody(1<<20))
	})
	if r := doRaw(t, srv, upRequest("/up", field("a", "one")+field("b", "two")+closing())); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if !errors.Is(afterNext, bites.ErrPartClosed) {
		t.Fatalf("a read of the first part after Next = %v, want ErrPartClosed", afterNext)
	}
	_, stale = second.Read(make([]byte, 4))
	if !errors.Is(stale, bites.ErrUploadClosed) {
		t.Fatalf("a read of a part after the handler returned = %v, want ErrUploadClosed", stale)
	}
	if _, err := up.Next(); !errors.Is(err, bites.ErrUploadClosed) {
		t.Fatalf("Next after the handler returned = %v, want ErrUploadClosed", err)
	}
	var zero bites.Upload
	if _, err := zero.Next(); !errors.Is(err, bites.ErrUploadClosed) {
		t.Fatalf("Next on a zero Upload = %v", err)
	}
}

func TestAKeptPartCannotReadTheNextRequestsBytes(t *testing.T) {
	var kept bites.Part
	var leaked string
	n := 0
	srv := start(t, func(a *bites.App) {
		a.Post("/up", func(_ context.Context, in UpIn) (struct{}, error) {
			n++
			if n == 1 {
				kept, _ = in.Body.Next()
				for {
					if _, err := in.Body.Next(); err != nil {
						return struct{}{}, nil
					}
				}
			}
			b := make([]byte, 64)
			k, _ := kept.Read(b)
			leaked = string(b[:k])
			for {
				if _, err := in.Body.Next(); err != nil {
					return struct{}{}, nil
				}
			}
		}, bites.MaxBody(1<<20))
	})
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	fmt.Fprint(c, upRequest("/up", field("a", "FIRST-SECRET")+closing()))
	srv.Read(c, false)
	fmt.Fprint(c, upRequest("/up", field("b", "SECOND-SECRET")+closing()))
	srv.Read(c, false)
	if leaked != "" {
		t.Fatalf("a part kept from the first request read %q in the second", leaked)
	}
}

func TestARequestThatIsNotMultipartOrHasNoUsableBoundaryOrNoBodyIsRefusedBeforeTheHandler(t *testing.T) {
	called := false
	srv := start(t, func(a *bites.App) {
		a.Post("/up", func(context.Context, UpIn) (UpOut, error) { called = true; return UpOut{}, nil }, bites.MaxBody(1<<20))
	})
	body := field("a", "1") + closing()
	for _, tc := range []struct {
		name, raw string
		status    int
		detail    string
	}{
		{"json sent", "POST /up HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}", 415, "the body must be sent as multipart/form-data"},
		{"no content type", "POST /up HTTP/1.1\r\nHost: x\r\nContent-Length: 2\r\n\r\n{}", 415, "the body must be sent as multipart/form-data"},
		{"no boundary", "POST /up HTTP/1.1\r\nHost: x\r\nContent-Type: multipart/form-data\r\nContent-Length: 2\r\n\r\n{}", 400, "the multipart boundary is missing"},
		{"a boundary of 71 bytes", "POST /up HTTP/1.1\r\nHost: x\r\nContent-Type: multipart/form-data; boundary=" + strings.Repeat("b", 71) + "\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body, 400, "longer than 70 bytes"},
	} {
		r := doRaw(t, srv, tc.raw)
		if r.Status != tc.status || !strings.Contains(r.Text(), tc.detail) || r.Get("content-type") != "application/problem+json" {
			t.Errorf("%s: %d %s", tc.name, r.Status, r.Text())
		}
	}
	r := doRaw(t, srv, "POST /up HTTP/1.1\r\nHost: x\r\nContent-Type: multipart/form-data; boundary=a\r\nContent-Length: 0\r\n\r\n")
	if r.Status != 400 || !strings.Contains(r.Text(), `{"location":"body","message":"required","rule":"required"}`) {
		t.Errorf("no body: %d %s", r.Status, r.Text())
	}
	if called {
		t.Fatal("the handler ran for a request that was refused")
	}
}

func TestAnUploadWithMoreBoundaryLengthThan70IsRefusedAtTheLimitAndExactly70Passes(t *testing.T) {
	srv := start(t, registerUpload())
	long := strings.Repeat("b", 70)
	body := "--" + long + "\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\nx\r\n--" + long + "--\r\n"
	r := doRaw(t, srv, "POST /up HTTP/1.1\r\nHost: x\r\nContent-Type: multipart/form-data; boundary="+long+"\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"+body)
	if r.Status != 200 || !strings.Contains(r.Text(), `"data":"x"`) {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestMorePartsThanTheLimitIsA413AndTheLimitCanBeChanged(t *testing.T) {
	var b strings.Builder
	for i := range 6 {
		b.WriteString(field("f"+strconv.Itoa(i), "v"))
	}
	b.WriteString(closing())
	srv := start(t, func(a *bites.App) {
		a.Post("/few", collect, bites.MaxBody(1<<20), bites.MaxParts(5))
		a.Post("/many", collect, bites.MaxBody(1<<20), bites.MaxParts(6))
	})
	r := doRaw(t, srv, upRequest("/few", b.String()))
	if r.Status != 413 || !strings.Contains(r.Text(), "more parts than this route allows") {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := doRaw(t, srv, upRequest("/many", b.String())); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	// 129 parts against the default limit of 128.
	b.Reset()
	for range 129 {
		b.WriteString(field("f", "v"))
	}
	b.WriteString(closing())
	srv = start(t, registerUpload())
	if r := doRaw(t, srv, upRequest("/up", b.String())); r.Status != 413 {
		t.Fatalf("129 parts: %d", r.Status)
	}
}

func TestAPartHeaderOverTheLimitIsRefusedWhetherItEndsOrNot(t *testing.T) {
	srv := start(t, registerUpload())
	pad := strings.Repeat("a", 9000)
	ends := "--" + bnd + "\r\nContent-Disposition: form-data; name=\"a\"\r\nX-Pad: " + pad + "\r\n\r\nx\r\n" + closing()
	r := doRaw(t, srv, upRequest("/up", ends))
	if r.Status != 431 || !strings.Contains(r.Text(), "header block is over 8192 bytes") {
		t.Fatalf("a long header: %d %s", r.Status, r.Text())
	}
	never := "--" + bnd + "\r\nContent-Disposition: form-data; name=\"a\"\r\nX-Pad: " + strings.Repeat("a", 200000)
	r = doRaw(t, srv, upRequest("/up", never))
	if r.Status != 431 {
		t.Fatalf("a header that never ends: %d %s", r.Status, r.Text())
	}
	fields := "--" + bnd + "\r\nContent-Disposition: form-data; name=\"a\"\r\n" + strings.Repeat("X-F: 1\r\n", 16) + "\r\nx\r\n" + closing()
	if r = doRaw(t, srv, upRequest("/up", fields)); r.Status != 431 || !strings.Contains(r.Text(), "more than 16 header fields") {
		t.Fatalf("17 fields: %d %s", r.Status, r.Text())
	}
}

func TestABodyThatStopsInTheMiddleOfAPartIsA400AndTheConnectionCloses(t *testing.T) {
	srv := start(t, registerUpload())
	body := "--" + bnd + "\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\nhalf a val"
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	// The client declares more than it sends and closes its side.
	fmt.Fprint(c, upHead("POST", "/up", "Content-Length: "+strconv.Itoa(len(body)+500)+"\r\n\r\n")+body)
	c.(interface{ CloseWrite() error }).CloseWrite()
	r := srv.Read(c, false)
	if r.Status != 400 || !strings.Contains(r.Text(), "request body ended early") {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	// A complete Content-Length whose multipart body is cut before the closing delimiter.
	r = doRaw(t, srv, upRequest("/up", field("a", "1")))
	if r.Status != 400 || !strings.Contains(r.Text(), "ended inside a part") {
		t.Fatalf("no closing delimiter: %d %s", r.Status, r.Text())
	}
}

func TestAClientThatTricklesItsUploadIsA408WhenTheBodyTimeoutPasses(t *testing.T) {
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk), bites.WithLogger(quietLog()))
	app.Post("/up", collect, bites.MaxBody(1<<20), bites.BodyTimeout(10*time.Second))
	srv := bitetest.Start(t, app)
	c := srv.Dial("203.0.113.9:1")
	defer c.Close()
	fmt.Fprint(c, upHead("POST", "/up", "Content-Length: 5000\r\n\r\n")+"--"+bnd+"\r\nContent-Disposition: form-data; na")
	deadline := clk.Now().Add(10 * time.Second)
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if n, ok := clk.Next(); ok && n.Equal(deadline) {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the upload never armed its body deadline")
		}
	}
	clk.Advance(11 * time.Second)
	r := srv.Read(c, false)
	if r.Status != 408 || !strings.Contains(r.Text(), "request body too slow") {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestTheDefaultBodyTimeoutOfAnUploadRouteIsDerivedFromMaxBodyAndLogged(t *testing.T) {
	srv, lb := startLogged(t, func(a *bites.App) {
		a.Post("/big", collect, bites.MaxBody(32<<20))
		a.Post("/small", collect, bites.MaxBody(1<<20))
		a.Post("/set", collect, bites.MaxBody(32<<20), bites.BodyTimeout(90*time.Second), bites.MaxInFlight(3))
	})
	srv.Do(bitetest.Request{Method: "GET", Target: "/nothing"}) // the settings are logged when serving starts
	log := lb.String()
	for _, want := range []string{
		`setting="upload POST /big: body timeout" value=4m16s source=default`,
		`setting="upload POST /small: body timeout" value=30s source=default`,
		`setting="upload POST /set: body timeout" value=1m30s source=custom`,
		`setting="upload POST /big: in flight" value=unlimited source=default`,
		`setting="upload POST /set: in flight" value=3 source=custom`,
		`setting="upload POST /big: max body bytes" value=33554432 source=custom`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the start-up log lacks %s\n%s", want, log)
		}
	}
}

func TestADeclaredLengthOverMaxBodyIsA413BeforeContinueAndAChunkedBodyOverItIsA413AsItArrives(t *testing.T) {
	srv := start(t, func(a *bites.App) { a.Post("/up", collect, bites.MaxBody(2000)) })
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	fmt.Fprint(c, upHead("POST", "/up", "Expect: 100-continue\r\nContent-Length: 3000\r\n\r\n"))
	r := srv.Read(c, false)
	if r.Status != 413 {
		t.Fatalf("declared 3000 with a limit of 2000: %d %s (a 100 Continue would have been read as status 100)", r.Status, r.Text())
	}
	body := field("a", strings.Repeat("x", 3000)) + closing()
	r = doRaw(t, srv, upHead("POST", "/up", "Transfer-Encoding: chunked\r\n\r\n")+chunked(body, 500))
	if r.Status != 413 || !strings.Contains(r.Text(), "request body too large") {
		t.Fatalf("chunked over the limit: %d %s", r.Status, r.Text())
	}
}

func TestContinueIsSentOnlyWhenTheHandlerFirstAsksForAPart(t *testing.T) {
	srv := start(t, registerUpload())
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	body := field("a", "1") + closing()
	fmt.Fprint(c, upHead("POST", "/up", "Expect: 100-continue\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"))
	if r := srv.Read(c, false); r.Status != 100 {
		t.Fatalf("first response %d, want 100", r.Status)
	}
	fmt.Fprint(c, body)
	if r := srv.Read(c, false); r.Status != 200 {
		t.Fatalf("%d", r.Status)
	}
	// A request the Input refuses never invites the body.
	type in struct {
		Query struct{ N int }
		Body  bites.Upload
	}
	srv = start(t, func(a *bites.App) {
		a.Post("/up", func(context.Context, in) (struct{}, error) { return struct{}{}, nil }, bites.MaxBody(1<<20))
	})
	c2 := srv.Dial("203.0.113.9:4001")
	defer c2.Close()
	fmt.Fprint(c2, upHead("POST", "/up?n=x", "Expect: 100-continue\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"))
	if r := srv.Read(c2, false); r.Status != 400 {
		t.Fatalf("a bad query with Expect: %d, want the 400 and no 100", r.Status)
	}
}

func TestAFileNameWithDotDotIsDataTheFrameworkNeverOpens(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(filepath.Join(dir, "store"))
	if err != nil {
		if err = os.Mkdir(filepath.Join(dir, "store"), 0o755); err != nil {
			t.Fatal(err)
		}
		root, err = os.OpenRoot(filepath.Join(dir, "store"))
		if err != nil {
			t.Fatal(err)
		}
	}
	defer root.Close()
	var seen string
	var createErr error
	srv := start(t, func(a *bites.App) {
		a.Post("/up", func(_ context.Context, in UpIn) (struct{}, error) {
			p, err := in.Body.Next()
			if err != nil {
				return struct{}{}, err
			}
			seen = p.FileName
			// A handler that trusts the name: the os.Root refuses what leaves the directory.
			_, createErr = root.Create(p.FileName)
			// A handler that chooses its own name is fine.
			f, err := root.Create("upload-1")
			if err != nil {
				return struct{}{}, err
			}
			defer f.Close()
			_, err = io.Copy(f, p)
			return struct{}{}, err
		}, bites.MaxBody(1<<20))
	})
	r := doRaw(t, srv, upRequest("/up", fileField("f", "../../evil.txt", "text/plain", "payload")+closing()))
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if seen != "../../evil.txt" {
		t.Fatalf("the handler saw the file name %q, want it verbatim", seen)
	}
	if createErr == nil {
		t.Fatal("the os.Root created a file for a name with ..")
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err == nil {
		t.Fatal("a file was written outside the directory")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "store", "upload-1")); !bytes.Equal(b, []byte("payload")) {
		t.Fatalf("stored %q", b)
	}
}

func TestAHandlerThatReturnsBeforeTheLastPartAnswersAndTheConnectionCloses(t *testing.T) {
	srv := start(t, func(a *bites.App) {
		a.Post("/up", func(_ context.Context, in UpIn) (bites.Text, error) {
			p, _ := in.Body.Next()
			return bites.Text{Body: "first=" + p.Name}, nil
		}, bites.MaxBody(1<<20))
	})
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	fmt.Fprint(c, upRequest("/up", field("a", "1")+field("b", "2")+closing()))
	r := srv.Read(c, false)
	if r.Status != 200 || r.Text() != "first=a" || r.Get("connection") != "close" {
		t.Fatalf("%d %q %v", r.Status, r.Text(), r.Header)
	}
}

func TestAnUploadRouteBoundedByMaxInFlightAnswers503WithRetryAfterBeforeTheBody(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	srv := start(t, func(a *bites.App) {
		a.Post("/up", func(_ context.Context, in UpIn) (bites.Text, error) {
			entered <- struct{}{}
			<-release
			return bites.Text{Body: "done"}, nil
		}, bites.MaxBody(1<<20), bites.MaxInFlight(2))
	})
	body := field("a", "1") + closing()
	var conns []net.Conn
	for i := range 2 {
		c := srv.Dial("203.0.113.9:" + strconv.Itoa(5000+i))
		conns = append(conns, c)
		fmt.Fprint(c, upRequest("/up", body))
		<-entered
	}
	c3 := srv.Dial("203.0.113.9:5002")
	defer c3.Close()
	fmt.Fprint(c3, upHead("POST", "/up", "Expect: 100-continue\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"))
	r := srv.Read(c3, false)
	if r.Status != 503 || r.Get("retry-after") != "5" || r.Get("connection") != "close" || r.Get("content-type") != "application/problem+json" {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Text())
	}
	close(release)
	for _, c := range conns {
		if r := srv.Read(c, false); r.Status != 200 || r.Text() != "done" {
			t.Fatalf("%d %s", r.Status, r.Text())
		}
		c.Close()
	}
	// The slots are free again.
	c4 := srv.Dial("203.0.113.9:5003")
	defer c4.Close()
	fmt.Fprint(c4, upRequest("/up", body))
	if r := srv.Read(c4, false); r.Status != 200 {
		t.Fatalf("after the uploads finished: %d", r.Status)
	}
}

func TestTheInFlightSlotIsFreedWhenTheHandlerPanicsOrTheInputIsRefused(t *testing.T) {
	type in struct {
		Query struct{ N int }
		Body  bites.Upload
	}
	srv := start(t, func(a *bites.App) {
		a.Post("/up", func(_ context.Context, i in) (struct{}, error) {
			if i.Query.N == 2 {
				panic("boom")
			}
			return struct{}{}, nil
		}, bites.MaxBody(1<<20), bites.MaxInFlight(1))
	})
	body := field("a", "1") + closing()
	for i, n := range []string{"1", "x", "2", "x", "2", "1"} {
		r := doRaw(t, srv, upRequest("/up?n="+n, body))
		want := 200
		switch n {
		case "x":
			want = 400
		case "2":
			want = 500
		}
		if r.Status != want {
			t.Fatalf("request %d (n=%s): %d, want %d", i, n, r.Status, want)
		}
	}
}

func TestAnUploadReadFromAnotherGoroutineWhileTheHandlerReturnsDoesNotRaceOrHang(t *testing.T) {
	var wg sync.WaitGroup
	srv := start(t, func(a *bites.App) {
		a.Post("/up", func(_ context.Context, in UpIn) (struct{}, error) {
			p, err := in.Body.Next()
			if err != nil {
				return struct{}{}, err
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				io.Copy(io.Discard, p)
			}()
			return struct{}{}, nil
		}, bites.MaxBody(1<<20))
	})
	for range 20 {
		doRaw(t, srv, upRequest("/up", field("a", strings.Repeat("z", 5000))+field("b", "2")+closing()))
	}
	wg.Wait()
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestAGroupsMaxBodySatisfiesAnUploadRouteInIt(t *testing.T) {
	srv := start(t, func(a *bites.App) {
		g := a.Group("/g", bites.MaxBody(1<<20))
		g.Post("/up", collect)
	})
	if r := doRaw(t, srv, upRequest("/g/up", field("a", "1")+closing())); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

// sumIn and sums hash each part as it streams, so a body of megabytes is never held whole.
type sumIn struct{ Body bites.Upload }

func sums(_ context.Context, in sumIn) (bites.Text, error) {
	var b strings.Builder
	for {
		p, err := in.Body.Next()
		if err == io.EOF {
			return bites.Text{Body: b.String()}, nil
		}
		if err != nil {
			return bites.Text{}, err
		}
		h := sha256.New()
		n, err := io.Copy(h, p)
		if err != nil {
			return bites.Text{}, err
		}
		fmt.Fprintf(&b, "%s %d %x\n", p.Name, n, h.Sum(nil))
	}
}

// readHTTP reads one answer with a Content-Length from r: its status and body.
func readHTTP(t *testing.T, r *bufio.Reader) (int, string) {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	status, _ := strconv.Atoi(strings.Fields(line)[1])
	n := 0
	for {
		h, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if h == "\r\n" {
			break
		}
		if k, v, ok := strings.Cut(h, ":"); ok && strings.EqualFold(k, "content-length") {
			n, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatal(err)
	}
	return status, string(body)
}

// dribble writes s in uneven pieces, so a boundary and a part header fall across reads.
func dribble(t *testing.T, c net.Conn, s string) {
	t.Helper()
	sizes := []int{1, 7, 61, 1500, 4093, 65536, 3}
	for i := 0; len(s) > 0; i++ {
		n := min(sizes[i%len(sizes)], len(s))
		if _, err := io.WriteString(c, s[:n]); err != nil {
			t.Fatal(err)
		}
		s = s[n:]
	}
}

func TestAnUploadOfMegabytesOverLoopbackTCPIsReadWholeWithContinueLengthAndChunked(t *testing.T) {
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Post("/sum", sums, bites.MaxBody(8<<20))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	go app.Serve(ln)
	t.Cleanup(func() { app.Close() })
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	r := bufio.NewReader(c)

	big := make([]byte, 3<<20)
	for i := range big {
		big[i] = byte(i * 7 % 251)
	}
	// The data holds what looks like a delimiter but is not one: CRLF, two dashes and the
	// boundary are a delimiter only when followed by CRLF or "--" (RFC 2046), not by "x".
	big = append(big, []byte("\r\n--"+bnd+"x\r\nend")...)
	small := "hello"
	body := fileField("big", "big.bin", "application/octet-stream", string(big)) + field("small", small) + closing()
	want := fmt.Sprintf("big %d %x\nsmall %d %x\n", len(big), sha256.Sum256(big), len(small), sha256.Sum256([]byte(small)))

	// Content-Length with Expect: 100-continue, the body sent only once the 100 came.
	io.WriteString(c, upHead("POST", "/sum", "Expect: 100-continue\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"))
	if st, _ := readHTTP(t, r); st != 100 {
		t.Fatalf("first answer %d, want 100", st)
	}
	dribble(t, c, body)
	if st, got := readHTTP(t, r); st != 200 || got != want {
		t.Fatalf("Content-Length: %d\n%s\nwant\n%s", st, got, want)
	}
	// The same body chunked, on the same connection.
	dribble(t, c, upHead("POST", "/sum", "Transfer-Encoding: chunked\r\n\r\n")+chunked(body, 40000))
	if st, got := readHTTP(t, r); st != 200 || got != want {
		t.Fatalf("chunked: %d\n%s\nwant\n%s", st, got, want)
	}
}

// eachIn and collectEach read the parts through Each, keeping the data of those not named skip,
// so a part passed by unread is seen to be skipped.
type eachIn struct {
	Query struct{ Skip bites.Opt[string] }
	Body  bites.Upload
}

func collectEach(_ context.Context, in eachIn) (UpOut, error) {
	var out UpOut
	err := in.Body.Each(func(p bites.Part) error {
		if p.Name == in.Query.Skip.Or("") {
			out.Parts = append(out.Parts, partSum{Name: p.Name})
			return nil
		}
		b, err := io.ReadAll(p)
		out.Parts = append(out.Parts, partSum{p.Name, p.FileName, p.ContentType, string(b)})
		return err
	})
	return out, err
}

func TestEachGivesEveryPartInOrderAndAPartItsFunctionLeavesUnreadIsSkipped(t *testing.T) {
	srv := start(t, func(a *bites.App) { a.Post("/up", collectEach, bites.MaxBody(1<<20)) })
	body := field("title", "hello") + fileField("doc", "a.txt", "text/plain", strings.Repeat("d", 40000)) + field("tail", "end") + closing()
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	for _, chunk := range []bool{false, true} {
		raw := upRequest("/up?skip=doc", body)
		if chunk {
			raw = upHead("POST", "/up?skip=doc", "Transfer-Encoding: chunked\r\n\r\n") + chunked(body, 777)
		}
		fmt.Fprint(c, raw)
		r := srv.Read(c, false)
		want := `{"tag":"","parts":[{"name":"title","file":"","type":"","data":"hello"},{"name":"doc","file":"","type":"","data":""},{"name":"tail","file":"","type":"","data":"end"}]}`
		if r.Status != 200 || r.Text() != want || r.Get("connection") == "close" {
			t.Fatalf("chunked %v: %d %v\n%s\nwant\n%s", chunk, r.Status, r.Header, r.Text(), want)
		}
	}
}

func TestAnErrorFromEachsFunctionStopsItIsReturnedAsItIsAndTheConnectionCloses(t *testing.T) {
	var seen []string
	srv := start(t, func(a *bites.App) {
		a.Post("/up", func(_ context.Context, in UpIn) (bites.NoContent, error) {
			return bites.NoContent{}, in.Body.Each(func(p bites.Part) error {
				seen = append(seen, p.Name)
				if p.Name == "b" {
					return bites.Forbidden("no b")
				}
				return nil
			})
		}, bites.MaxBody(1<<20))
	})
	r := doRaw(t, srv, upRequest("/up", field("a", "1")+field("b", "2")+field("c", "3")+closing()))
	if r.Status != 403 || !strings.Contains(r.Text(), "no b") || r.Get("connection") != "close" {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Text())
	}
	if strings.Join(seen, ",") != "a,b" {
		t.Fatalf("Each visited %v after its function's error, want a,b", seen)
	}
}

func TestARefusalWhileEachReadsIsReturnedAsItsStatusError(t *testing.T) {
	var b strings.Builder
	for i := range 3 {
		b.WriteString(field("f"+strconv.Itoa(i), "v"))
	}
	b.WriteString(closing())
	srv := start(t, func(a *bites.App) { a.Post("/up", collectEach, bites.MaxBody(1<<20), bites.MaxParts(2)) })
	if r := doRaw(t, srv, upRequest("/up", b.String())); r.Status != 413 || !strings.Contains(r.Text(), "more parts than this route allows") {
		t.Fatalf("too many parts: %d %s", r.Status, r.Text())
	}
	cut := field("a", "1") + "--" + bnd + "\r\nContent-Disposition: form-data; name=\"b\"\r\n\r\nno end"
	if r := doRaw(t, srv, upRequest("/up", cut)); r.Status != 400 {
		t.Fatalf("a body cut inside a part: %d %s", r.Status, r.Text())
	}
}

func TestEachAfterTheHandlerReturnedAndAPartKeptFromItFailWithoutReading(t *testing.T) {
	var up bites.Upload
	var kept bites.Part
	srv := start(t, func(a *bites.App) {
		a.Post("/up", func(_ context.Context, in UpIn) (struct{}, error) {
			up = in.Body
			return struct{}{}, in.Body.Each(func(p bites.Part) error { kept = p; return nil })
		}, bites.MaxBody(1<<20))
	})
	if r := doRaw(t, srv, upRequest("/up", field("a", "secret")+closing())); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if _, err := kept.Read(make([]byte, 8)); !errors.Is(err, bites.ErrUploadClosed) {
		t.Fatalf("a part kept from Each read after the handler = %v, want ErrUploadClosed", err)
	}
	called := false
	if err := up.Each(func(bites.Part) error { called = true; return nil }); !errors.Is(err, bites.ErrUploadClosed) || called {
		t.Fatalf("Each after the handler returned = %v, called %v; want ErrUploadClosed and no call", err, called)
	}
}
