package bites_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// BodyReader (ADR 0044): the raw counterpart of Upload, through the in-memory connections.

type putIn struct {
	Path struct{ Name string }
	Body bites.BodyReader
}

// sumBody reads the whole body through Read in small steps and answers its length and SHA-256.
func sumBody(_ context.Context, in putIn) (bites.Text, error) {
	h := sha256.New()
	buf := make([]byte, 1000)
	n := 0
	for {
		k, err := in.Body.Read(buf)
		h.Write(buf[:k])
		n += k
		if err == io.EOF {
			break
		}
		if err != nil {
			return bites.Text{}, err
		}
	}
	return bites.Text{Body: strconv.Itoa(n) + " " + hex.EncodeToString(h.Sum(nil))[:16]}, nil
}

func putHead(target, extra string) string {
	return "PUT " + target + " HTTP/1.1\r\nHost: x\r\nContent-Type: application/pdf\r\n" + extra
}

func bodySum(b string) string {
	s := sha256.Sum256([]byte(b))
	return strconv.Itoa(len(b)) + " " + hex.EncodeToString(s[:])[:16]
}

// waitForTimer waits until the clock holds a timer for the instant, the proof that a deadline was armed.
func waitForTimer(t *testing.T, clk *bitetest.Clock, at time.Time) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if n, ok := clk.Next(); ok && n.Equal(at) {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("no deadline was armed for %v", at)
		}
	}
}

func TestABodyReaderStreamsABodyLargerThanTheReadWindowWhateverItsMediaType(t *testing.T) {
	srv := start(t, func(a *bites.App) { a.Put("/files/{name}", sumBody, bites.MaxBody(4<<20)) })
	body := strings.Repeat("0123456789abcdef", 40000) // 640 000 bytes, 20 windows
	r := doRaw(t, srv, putHead("/files/a.pdf", "Content-Length: "+strconv.Itoa(len(body))+"\r\n\r\n")+body)
	if r.Status != 200 || r.Text() != bodySum(body) {
		t.Fatalf("%d %q, want %q", r.Status, r.Text(), bodySum(body))
	}
}

func TestAChunkedBodyReaderBodyIsReadTheSameAndTheConnectionServesTheNextRequest(t *testing.T) {
	srv := start(t, func(a *bites.App) { a.Put("/files/{name}", sumBody, bites.MaxBody(4<<20)) })
	body := strings.Repeat("xyz", 30000)
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	for range 2 {
		fmt.Fprint(c, putHead("/files/a", "Transfer-Encoding: chunked\r\n\r\n")+chunked(body, 777))
		if r := srv.Read(c, false); r.Status != 200 || r.Text() != bodySum(body) {
			t.Fatalf("%d %q", r.Status, r.Text())
		}
	}
}

func TestABodyReaderRequestWithNoBodyReadsAsEmpty(t *testing.T) {
	srv := start(t, func(a *bites.App) { a.Put("/files/{name}", sumBody, bites.MaxBody(1<<20)) })
	r := doRaw(t, srv, putHead("/files/a", "Content-Length: 0\r\n\r\n"))
	if r.Status != 200 || r.Text() != bodySum("") {
		t.Fatalf("%d %q", r.Status, r.Text())
	}
}

func TestABodyReaderBodyOverMaxBodyIsA413ProblemJSONAsItArrivesAndBeforeContinueWhenDeclared(t *testing.T) {
	srv := start(t, func(a *bites.App) { a.Put("/files/{name}", sumBody, bites.MaxBody(2000)) })
	// Declared: refused from the head, before the handler runs and before 100 Continue.
	r := doRaw(t, srv, putHead("/files/a", "Expect: 100-continue\r\nContent-Length: 5000\r\n\r\n"))
	if r.Status != 413 || r.Get("content-type") != "application/problem+json" {
		t.Fatalf("declared: %d %v %s", r.Status, r.Header, r.Text())
	}
	// Chunked: the handler's Read returns the *StatusError at the MaxBody+1th byte.
	r = doRaw(t, srv, putHead("/files/a", "Transfer-Encoding: chunked\r\n\r\n")+chunked(strings.Repeat("a", 5000), 500))
	if r.Status != 413 || r.Get("content-type") != "application/problem+json" || r.Get("connection") != "close" {
		t.Fatalf("chunked: %d %v %s", r.Status, r.Header, r.Text())
	}
}

func TestABodyReaderClientThatTricklesIsA408WhenTheBodyTimeoutPasses(t *testing.T) {
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk), bites.WithLogger(quietLog()))
	app.Put("/files/{name}", sumBody, bites.MaxBody(1<<20), bites.BodyTimeout(10*time.Second))
	srv := bitetest.Start(t, app)
	c := srv.Dial("203.0.113.9:1")
	defer c.Close()
	fmt.Fprint(c, putHead("/files/a", "Content-Length: 5000\r\n\r\n")+"only a little")
	waitForTimer(t, clk, clk.Now().Add(10*time.Second))
	clk.Advance(11 * time.Second)
	r := srv.Read(c, false)
	if r.Status != 408 || !strings.Contains(r.Text(), "request body too slow") {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestABodyReaderBodyCutShortIsA400AndTheConnectionCloses(t *testing.T) {
	srv := start(t, func(a *bites.App) { a.Put("/files/{name}", sumBody, bites.MaxBody(1<<20)) })
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	fmt.Fprint(c, putHead("/files/a", "Content-Length: 5000\r\n\r\n")+"half")
	c.(interface{ CloseWrite() error }).CloseWrite()
	r := srv.Read(c, false)
	if r.Status != 400 || !strings.Contains(r.Text(), "request body ended early") || r.Get("connection") != "close" {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Text())
	}
}

func TestABodyReaderKeptPastTheHandlerFailsWithErrUploadClosedAndTheZeroValueToo(t *testing.T) {
	var kept bites.BodyReader
	srv := start(t, func(a *bites.App) {
		a.Put("/files/{name}", func(_ context.Context, in putIn) (bites.Text, error) {
			kept = in.Body
			_, _ = io.Copy(io.Discard, in.Body)
			return bites.Text{Body: "ok"}, nil
		}, bites.MaxBody(1<<20))
	})
	if r := doRaw(t, srv, putHead("/files/a", "Content-Length: 3\r\n\r\nabc")); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if _, err := kept.Read(make([]byte, 4)); !errors.Is(err, bites.ErrUploadClosed) {
		t.Fatalf("a read after the handler returned = %v, want ErrUploadClosed", err)
	}
	var zero bites.BodyReader
	if _, err := zero.Read(make([]byte, 4)); !errors.Is(err, bites.ErrUploadClosed) {
		t.Fatalf("a read on a zero BodyReader = %v, want ErrUploadClosed", err)
	}
}

func TestAKeptBodyReaderCannotReadTheNextRequestsBytes(t *testing.T) {
	var kept bites.BodyReader
	var leaked string
	n := 0
	srv := start(t, func(a *bites.App) {
		a.Put("/files/{name}", func(_ context.Context, in putIn) (bites.Text, error) {
			n++
			if n == 1 {
				kept = in.Body
			} else {
				b := make([]byte, 16)
				k, _ := kept.Read(b)
				leaked = string(b[:k])
			}
			_, _ = io.Copy(io.Discard, in.Body)
			return bites.Text{Body: "ok"}, nil
		}, bites.MaxBody(1<<20))
	})
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	for _, body := range []string{"first-secret", "second-secret"} {
		fmt.Fprint(c, putHead("/files/a", "Content-Length: "+strconv.Itoa(len(body))+"\r\n\r\n")+body)
		if r := srv.Read(c, false); r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Text())
		}
	}
	if leaked != "" {
		t.Fatalf("a kept BodyReader read %q of another request", leaked)
	}
}

func TestAHandlerThatStopsReadingABodyReaderAnswersAndTheConnectionCloses(t *testing.T) {
	srv := start(t, func(a *bites.App) {
		a.Put("/files/{name}", func(_ context.Context, in putIn) (bites.Text, error) {
			_, _ = in.Body.Read(make([]byte, 4))
			return bites.Text{Body: "partial"}, nil
		}, bites.MaxBody(1<<20))
	})
	r := doRaw(t, srv, putHead("/files/a", "Content-Length: 100000\r\n\r\n")+strings.Repeat("a", 100000))
	if r.Status != 200 || r.Text() != "partial" || r.Get("connection") != "close" {
		t.Fatalf("%d %q %v", r.Status, r.Text(), r.Header)
	}
}

func TestABodyReaderRouteBoundedByMaxInFlightAnswers503WithRetryAfterBeforeTheBody(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	srv := start(t, func(a *bites.App) {
		a.Put("/files/{name}", func(_ context.Context, in putIn) (bites.Text, error) {
			entered <- struct{}{}
			<-release
			return bites.Text{Body: "done"}, nil
		}, bites.MaxBody(1<<20), bites.MaxInFlight(1))
	})
	c1 := srv.Dial("203.0.113.9:5000")
	fmt.Fprint(c1, putHead("/files/a", "Content-Length: 1\r\n\r\nx"))
	<-entered
	c2 := srv.Dial("203.0.113.9:5001")
	defer c2.Close()
	fmt.Fprint(c2, putHead("/files/a", "Expect: 100-continue\r\nContent-Length: 1\r\n\r\n"))
	r := srv.Read(c2, false)
	if r.Status != 503 || r.Get("retry-after") != "5" || r.Get("connection") != "close" {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Text())
	}
	close(release)
	if r := srv.Read(c1, false); r.Status != 200 {
		t.Fatalf("%d", r.Status)
	}
	c1.Close()
	c3 := srv.Dial("203.0.113.9:5002")
	defer c3.Close()
	fmt.Fprint(c3, putHead("/files/a", "Content-Length: 1\r\n\r\nx"))
	if r := srv.Read(c3, false); r.Status != 200 {
		t.Fatalf("the slot was not freed: %d", r.Status)
	}
}

func TestABodyReaderRouteGetsTheSameDerivedBodyTimeoutAsAnUpload(t *testing.T) {
	srv, lb := startLogged(t, func(a *bites.App) {
		a.Put("/big/{name}", sumBody, bites.MaxBody(32<<20), bites.MaxInFlight(2))
	})
	srv.Do(bitetest.Request{Method: "GET", Target: "/nothing"})
	for _, want := range []string{
		`setting="upload PUT /big/{name}: body timeout" value=4m16s source=default`,
		`setting="upload PUT /big/{name}: in flight" value=2 source=custom`,
	} {
		if !strings.Contains(lb.String(), want) {
			t.Errorf("the start-up log lacks %s\n%s", want, lb.String())
		}
	}
}

// The body timeout of a route whose body is read whole (ADR 0044, ADR 0024).

func TestAByteSliceRouteWithAnEightMiBMaxBodyGetsADerivedBodyTimeoutThatIsLoggedAndEnforced(t *testing.T) {
	clk := bitetest.NewClock()
	lb := &logBuf{}
	app := bites.New(bites.WithClock(clk), bites.WithLogger(slog.New(slog.NewTextHandler(lb, nil))))
	echo := func(_ context.Context, in struct{ Body []byte }) (bites.Text, error) {
		return bites.Text{Body: strconv.Itoa(len(in.Body))}, nil
	}
	app.Put("/parts", echo, bites.MaxBody(8<<20))
	app.Put("/small", echo, bites.MaxBody(1<<20))
	app.Put("/set", echo, bites.MaxBody(8<<20), bites.BodyTimeout(45*time.Second))
	srv := bitetest.Start(t, app)
	c := srv.Dial("203.0.113.9:1")
	defer c.Close()
	// 8 MiB / 128 KiB/s = 64 s, not 30 s: the deadline is armed 64 s ahead.
	fmt.Fprint(c, "PUT /parts HTTP/1.1\r\nHost: x\r\nContent-Length: 5000000\r\n\r\nabc")
	waitForTimer(t, clk, clk.Now().Add(64*time.Second))
	clk.Advance(65 * time.Second)
	if r := srv.Read(c, false); r.Status != 408 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	log := lb.String()
	for _, want := range []string{
		`setting="route PUT /parts: body timeout, derived from max body bytes" value=1m4s source=default`,
		`setting="route PUT /parts: max body bytes" value=8388608 source=custom`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the start-up log lacks %s\n%s", want, log)
		}
	}
	for _, not := range []string{"PUT /small: body timeout", "PUT /set: body timeout"} {
		if strings.Contains(log, not) {
			t.Errorf("the start-up log says %q for a route whose timeout was not derived\n%s", not, log)
		}
	}
}
