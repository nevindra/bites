package bites_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

func get(t *testing.T, s *bitetest.Server, target string) bitetest.Response {
	t.Helper()
	return s.Do(bitetest.Request{Method: "GET", Target: target})
}

func TestAnSSEHandlerWritesEventsAndTheMultiLineDataBecomesOneDataFieldPerLine(t *testing.T) {
	s := start(t, func(a *bites.App) {
		a.Get("/feed", func(context.Context, Empty) (bites.SSE, error) {
			return bites.SSE{Func: func(_ context.Context, e bites.EventWriter) error {
				e.Comment("hello")
				e.Send(bites.Event{Data: "one"})
				e.Send(bites.Event{ID: "7", Name: "update", Data: "a\nb\r\nc\rd", Retry: 3500 * time.Millisecond})
				e.Send(bites.Event{Retry: time.Second})
				e.Send(bites.Event{Name: "ping"})
				e.Send(bites.Event{Data: "trailing\n"})
				return e.Comment("two\nlines")
			}}, nil
		})
	})
	rp := get(t, s, "/feed")
	want := ": hello\n\n" +
		"data: one\n\n" +
		"id: 7\nevent: update\nretry: 3500\ndata: a\ndata: b\ndata: c\ndata: d\n\n" +
		"retry: 1000\n\n" +
		"event: ping\ndata: \n\n" +
		"data: trailing\ndata: \n\n" +
		": two\n: lines\n\n"
	if rp.Text() != want {
		t.Fatalf("SSE body\n got %q\nwant %q", rp.Text(), want)
	}
	if rp.Status != 200 || rp.Get("Content-Type") != "text/event-stream" || rp.Get("Cache-Control") != "no-cache" || rp.Get("Transfer-Encoding") != "chunked" {
		t.Fatalf("SSE head: %d %v", rp.Status, rp.Header)
	}
}

func TestAnSSEEventWithALineBreakInItsIDOrNameIsRefusedAndNothingIsSent(t *testing.T) {
	var errs [3]error
	s := start(t, func(a *bites.App) {
		a.Get("/feed", func(context.Context, Empty) (bites.SSE, error) {
			return bites.SSE{Func: func(_ context.Context, e bites.EventWriter) error {
				errs[0] = e.Send(bites.Event{ID: "1\nevent: admin"})
				errs[1] = e.Send(bites.Event{Name: "x\ry"})
				errs[2] = e.Send(bites.Event{ID: "a\x00b"})
				return nil
			}}, nil
		})
	})
	if rp := get(t, s, "/feed"); rp.Text() != "" {
		t.Fatalf("body %q, want none", rp.Text())
	}
	for i, err := range errs {
		if !errors.Is(err, bites.ErrBadEvent) {
			t.Errorf("send %d: %v, want ErrBadEvent", i, err)
		}
	}
}

func TestAStreamReturnsItsStatusHeadersAndCookiesAndItsFunctionWritesTheBody(t *testing.T) {
	s := start(t, func(a *bites.App) {
		a.Get("/dl", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Status: 206, Header: bites.Header{"Content-Type": {"text/csv"}, "X-A": {"b"}, "Content-Length": {"5"}},
				Cookies: []bites.Cookie{{Name: "s", Value: "1"}},
				Func: func(_ context.Context, w *bites.StreamWriter) error {
					w.WriteString("a,b\n")
					return nil
				}}, nil
		})
		a.Get("/plain", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Func: func(_ context.Context, w *bites.StreamWriter) error { w.Write([]byte{1, 2}); return nil }}, nil
		})
	})
	rp := get(t, s, "/dl")
	if rp.Status != 206 || rp.Get("Content-Type") != "text/csv" || rp.Get("X-A") != "b" || rp.Text() != "a,b\n" ||
		!strings.Contains(rp.Get("Set-Cookie"), "s=1") || rp.Get("Content-Length") != "" || rp.Get("Transfer-Encoding") != "chunked" {
		t.Fatalf("%d %v %q", rp.Status, rp.Header, rp.Text())
	}
	if rp := get(t, s, "/plain"); rp.Status != 200 || rp.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("%d %v", rp.Status, rp.Header)
	}
}

func TestAStreamWriterKeptPastTheFunctionFailsWithErrStreamClosed(t *testing.T) {
	var kept *bites.StreamWriter
	var keptEvents bites.EventWriter
	s := start(t, func(a *bites.App) {
		a.Get("/s", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Func: func(_ context.Context, w *bites.StreamWriter) error { kept = w; return nil }}, nil
		})
		a.Get("/e", func(context.Context, Empty) (bites.SSE, error) {
			return bites.SSE{Func: func(_ context.Context, e bites.EventWriter) error { keptEvents = e; return nil }}, nil
		})
	})
	get(t, s, "/s")
	get(t, s, "/e")
	if _, err := kept.Write([]byte("x")); !errors.Is(err, bites.ErrStreamClosed) {
		t.Errorf("Write after return: %v", err)
	}
	if err := keptEvents.Send(bites.Event{Data: "x"}); !errors.Is(err, bites.ErrStreamClosed) {
		t.Errorf("Send after return: %v", err)
	}
	if err := (bites.EventWriter{}).Comment("x"); !errors.Is(err, bites.ErrStreamClosed) {
		t.Errorf("Comment on the zero EventWriter: %v", err)
	}
}

func TestAnErrorOrAPanicBeforeTheFirstByteIsTheOrdinaryAnswer(t *testing.T) {
	s := start(t, func(a *bites.App) {
		a.Get("/status", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Func: func(_ context.Context, w *bites.StreamWriter) error {
				w.WriteString("never sent")
				return bites.NewError(404, "no such feed")
			}}, nil
		})
		a.Get("/boom", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Func: func(_ context.Context, w *bites.StreamWriter) error { panic("boom") }}, nil
		})
		a.Get("/nofunc", func(context.Context, Empty) (bites.Stream, error) { return bites.Stream{}, nil })
		a.Get("/nobody", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Status: 204, Func: func(context.Context, *bites.StreamWriter) error { return nil }}, nil
		})
		a.Get("/nosse", func(context.Context, Empty) (bites.SSE, error) { return bites.SSE{}, nil })
	})
	for _, tc := range []struct {
		target string
		status int
	}{{"/status", 404}, {"/boom", 500}, {"/nofunc", 500}, {"/nobody", 500}, {"/nosse", 500}} {
		rp := get(t, s, tc.target)
		if rp.Status != tc.status || !strings.Contains(rp.Get("Content-Type"), "problem+json") || strings.Contains(rp.Text(), "never sent") {
			t.Errorf("%s: %d %v %q, want a problem+json %d", tc.target, rp.Status, rp.Header, rp.Text(), tc.status)
		}
	}
}

func TestAPanicAfterTheFirstByteClosesTheConnectionWithNoTerminatingChunk(t *testing.T) {
	s := start(t, func(a *bites.App) {
		a.Get("/s", func(context.Context, Empty) (bites.SSE, error) {
			return bites.SSE{Func: func(_ context.Context, e bites.EventWriter) error {
				e.Send(bites.Event{Data: "first"})
				panic("boom")
			}}, nil
		})
	})
	c := s.Dial("203.0.113.9:4000")
	c.Write([]byte("GET /s HTTP/1.1\r\nHost: x\r\n\r\n"))
	b, _ := io.ReadAll(bufio.NewReader(c)) // reads to the close
	got := string(b)
	if !strings.HasPrefix(got, "HTTP/1.1 200 OK") || !strings.HasSuffix(got, "data: first\n\n\r\n") || strings.Contains(got, "0\r\n\r\n") || strings.Contains(got, "500") {
		t.Fatalf("response %q: want the 200 and the first event, then a bare close", got)
	}
}

func TestAnHTTP10ClientGetsNoChunkingAndTheBodyEndsWithTheConnection(t *testing.T) {
	s := start(t, func(a *bites.App) {
		a.Get("/s", func(context.Context, Empty) (bites.SSE, error) {
			return bites.SSE{Func: func(_ context.Context, e bites.EventWriter) error { return e.Send(bites.Event{Data: "x"}) }}, nil
		})
	})
	c := s.Dial("203.0.113.9:4000")
	c.Write([]byte("GET /s HTTP/1.0\r\n\r\n"))
	b, _ := io.ReadAll(c)
	head, body, _ := strings.Cut(string(b), "\r\n\r\n")
	if strings.Contains(strings.ToLower(head), "transfer-encoding") || !strings.Contains(head, "Connection: close") || body != "data: x\n\n" {
		t.Fatalf("head %q body %q: want Connection: close, no chunking, the event as the body", head, body)
	}
}

func TestTheClientLeavingCancelsTheStreamFunctionsContext(t *testing.T) {
	started := make(chan struct{})
	done := make(chan error, 1)
	s := start(t, func(a *bites.App) {
		a.Get("/s", func(context.Context, Empty) (bites.SSE, error) {
			return bites.SSE{Func: func(ctx context.Context, e bites.EventWriter) error {
				e.Comment("open")
				close(started)
				<-ctx.Done()
				done <- ctx.Err()
				return ctx.Err()
			}}, nil
		})
	})
	c := s.Dial("203.0.113.9:4000")
	c.Write([]byte("GET /s HTTP/1.1\r\nHost: x\r\n\r\n"))
	<-started
	c.Close()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ctx.Err() = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the context was not cancelled when the client left")
	}
}

func TestMiddlewareSeesAStreamAsAStatusAndAContentTypeWithSizeZeroAndMayDecorateItsHead(t *testing.T) {
	var sawStreaming, sawSize atomic.Int64
	var sawType atomic.Value
	var ctxVal atomic.Value
	type key struct{}
	s := start(t, func(a *bites.App) {
		a.Use(func(ctx context.Context, _ bites.Request, next bites.Next) (bites.Response, error) {
			res := next.Call(context.WithValue(ctx, key{}, "from middleware"))
			if res.Streaming() {
				sawStreaming.Add(1)
			}
			sawSize.Store(int64(res.Size()))
			sawType.Store(res.ContentType())
			res.AddHeader("X-Served-By", "mw")
			return res, nil
		})
		a.Get("/s", func(context.Context, Empty) (bites.SSE, error) {
			return bites.SSE{Func: func(ctx context.Context, e bites.EventWriter) error {
				ctxVal.Store(ctx.Value(key{}))
				return e.Send(bites.Event{Data: "x"})
			}}, nil
		})
	})
	rp := get(t, s, "/s")
	if sawStreaming.Load() != 1 || sawSize.Load() != 0 || sawType.Load() != "text/event-stream" {
		t.Errorf("middleware saw streaming=%d size=%d type=%v", sawStreaming.Load(), sawSize.Load(), sawType.Load())
	}
	if rp.Get("X-Served-By") != "mw" || rp.Text() != "data: x\n\n" {
		t.Errorf("head %v body %q", rp.Header, rp.Text())
	}
	if ctxVal.Load() != "from middleware" {
		t.Errorf("the stream's context lost the middleware's value: %v", ctxVal.Load())
	}
}

func TestMiddlewareThatReplacesAStreamWithAFreshResponseOrChangesItsStatusIsObeyed(t *testing.T) {
	var ran atomic.Int64
	s := start(t, func(a *bites.App) {
		a.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			res := next.Call(ctx)
			switch req.RawQuery() {
			case "deny":
				return bites.Problem(403, "no"), nil
			case "created":
				res.SetStatus(201)
			}
			return res, nil
		})
		a.Get("/s", func(context.Context, Empty) (bites.SSE, error) {
			return bites.SSE{Func: func(_ context.Context, e bites.EventWriter) error {
				ran.Add(1)
				return e.Send(bites.Event{Data: "x"})
			}}, nil
		})
	})
	if rp := get(t, s, "/s?deny"); rp.Status != 403 || ran.Load() != 0 {
		t.Errorf("replaced: %d, function ran %d times", rp.Status, ran.Load())
	}
	if rp := get(t, s, "/s?created"); rp.Status != 201 || rp.Text() != "data: x\n\n" {
		t.Errorf("changed status: %d %q", rp.Status, rp.Text())
	}
}

func TestAWriterUsedFromOtherGoroutinesWhileTheFunctionReturnsDoesNotRace(t *testing.T) {
	var wg sync.WaitGroup
	s := start(t, func(a *bites.App) {
		a.Get("/s", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Func: func(_ context.Context, w *bites.StreamWriter) error {
				for range 4 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for range 50 {
							w.WriteString("x")
						}
					}()
				}
				return nil
			}}, nil
		})
	})
	get(t, s, "/s")
	wg.Wait()
}

// ---- Shutdown (ADR 0029) ----

func startApp(t *testing.T, register func(a *bites.App)) (*bitetest.Server, *bites.App) {
	t.Helper()
	app := bites.New()
	register(app)
	return bitetest.Start(t, app), app
}

func TestALiveSSEStreamIsEndedWithErrServerClosedWhenShutdownStarts(t *testing.T) {
	started, cause := make(chan struct{}), make(chan error, 1)
	s, app := startApp(t, func(a *bites.App) {
		a.Get("/feed", func(context.Context, Empty) (bites.SSE, error) {
			return bites.SSE{Func: func(ctx context.Context, e bites.EventWriter) error {
				e.Send(bites.Event{Data: "first"})
				close(started)
				<-ctx.Done()
				cause <- ctx.(interface{ Cause() error }).Cause() // context.Cause cannot see it (ADR 0016)
				return ctx.Err()
			}}, nil
		})
	})
	c := s.Dial("203.0.113.9:4000")
	c.Write([]byte("GET /feed HTTP/1.1\r\nHost: t\r\n\r\n"))
	<-started
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(context.Background()) }()
	if got := <-cause; !errors.Is(got, bites.ErrServerClosed) {
		t.Fatalf("cause = %v, want ErrServerClosed", got)
	}
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v: the stream was cut, so nothing was left", err)
	}
	rp := s.Read(c, false)
	if rp.Text() != "data: first\n\n" {
		t.Fatalf("body %q: the SSE should end cleanly after its first event", rp.Text())
	}
}

func TestALiveStreamThatIsNotSSEIsCutWithNoTerminatingChunkWhenShutdownStarts(t *testing.T) {
	started := make(chan struct{})
	s, app := startApp(t, func(a *bites.App) {
		a.Get("/nd", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Live: true, Func: func(ctx context.Context, w *bites.StreamWriter) error {
				w.WriteString("{}\n")
				w.Flush()
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}}, nil
		})
	})
	c := s.Dial("203.0.113.9:4000")
	c.Write([]byte("GET /nd HTTP/1.1\r\nHost: t\r\n\r\n"))
	<-started
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned %v", err)
	}
	br := bufio.NewReader(c)
	var all strings.Builder
	io.Copy(&all, br)
	if !strings.Contains(all.String(), "3\r\n{}\n\r\n") || strings.HasSuffix(all.String(), "0\r\n\r\n") {
		t.Fatalf("wire %q: want the first chunk and no terminating chunk", all.String())
	}
}

func TestAPlainStreamDrainsWhenShutdownStartsAndShutdownWaitsForIt(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	s, app := startApp(t, func(a *bites.App) {
		a.Get("/dl", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Func: func(ctx context.Context, w *bites.StreamWriter) error {
				w.WriteString("part one,")
				w.Flush()
				close(started)
				<-release
				if ctx.Err() != nil {
					return ctx.Err()
				}
				_, err := w.WriteString(" part two")
				return err
			}}, nil
		})
	})
	c := s.Dial("203.0.113.9:4000")
	c.Write([]byte("GET /dl HTTP/1.1\r\nHost: t\r\n\r\n"))
	<-started
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("Shutdown returned %v with a plain stream running", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v", err)
	}
	if rp := s.Read(c, false); rp.Text() != "part one, part two" {
		t.Fatalf("body %q, want the whole download", rp.Text())
	}
}

// ---- Events[T]: typed server-sent events (ADR 0030) ----

type Tick struct {
	N    int    `json:"n"`
	Note string `json:"note,omitempty"`
}

// indented is a value whose MarshalJSON writes JSON over several lines.
type indented struct{ A, B int }

func (v indented) MarshalJSON() ([]byte, error) {
	return []byte("{\n  \"a\": " + strconv.Itoa(v.A) + ",\r\n  \"b\": " + strconv.Itoa(v.B) + "\n}"), nil
}

func TestAnEventsHandlerSendsEachValueAsOneDataFieldOfJSONWithItsIDNameAndRetry(t *testing.T) {
	s := start(t, func(a *bites.App) {
		a.Get("/ticks", func(context.Context, Empty) (bites.Events[Tick], error) {
			return bites.Events[Tick]{Header: bites.Header{"X-Feed": {"ticks"}}, Cookies: []bites.Cookie{{Name: "s", Value: "1"}},
				Func: func(_ context.Context, e bites.EventSender[Tick]) error {
					e.Comment("hello")
					e.Send("tick", Tick{N: 1})
					e.Send("", Tick{N: 2, Note: "a\nb"})
					e.SendEvent(bites.Event{ID: "3", Name: "tick", Retry: 2500 * time.Millisecond}, Tick{N: 3})
					return e.SendEvent(bites.Event{ID: "4"}, Tick{N: 4})
				}}, nil
		})
	})
	rp := get(t, s, "/ticks")
	want := ": hello\n\n" +
		"event: tick\ndata: {\"n\":1}\n\n" +
		"data: {\"n\":2,\"note\":\"a\\nb\"}\n\n" +
		"id: 3\nevent: tick\nretry: 2500\ndata: {\"n\":3}\n\n" +
		"id: 4\ndata: {\"n\":4}\n\n"
	if rp.Text() != want {
		t.Fatalf("Events body\n got %q\nwant %q", rp.Text(), want)
	}
	if rp.Status != 200 || rp.Get("Content-Type") != "text/event-stream" || rp.Get("Cache-Control") != "no-cache" ||
		rp.Get("X-Feed") != "ticks" || !strings.Contains(rp.Get("Set-Cookie"), "s=1") || rp.Get("Transfer-Encoding") != "chunked" {
		t.Fatalf("Events head: %d %v", rp.Status, rp.Header)
	}
}

func TestAnEventsValueWhoseMarshalJSONSpansLinesIsSentAsOneDataField(t *testing.T) {
	s := startWith(t, func(a *bites.App) {
		a.Get("/x", func(context.Context, Empty) (bites.Events[indented], error) {
			return bites.Events[indented]{Func: func(_ context.Context, e bites.EventSender[indented]) error {
				return e.Send("", indented{A: 1, B: 2})
			}}, nil
		})
	}, bites.WithJSONSchema[indented](bites.JSONSchema{Type: "object"}))
	got := get(t, s, "/x").Text()
	if !strings.HasPrefix(got, "data: {") || strings.Count(got, "\n") != 2 || !strings.Contains(got, `"a":`) {
		t.Fatalf("body %q, want one data field", got)
	}
	var v struct{ A, B int }
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(got), "data: ")), &v); err != nil || v.A != 1 || v.B != 2 {
		t.Fatalf("the data %q does not read back: %v %v", got, v, err)
	}
}

func TestAnEventLargerThanTheSendersBufferIsSentWhole(t *testing.T) {
	big, huge := strings.Repeat("x", 2000), strings.Repeat("y", 100<<10)
	s := start(t, func(a *bites.App) {
		a.Get("/x", func(context.Context, Empty) (bites.Events[Tick], error) {
			return bites.Events[Tick]{Func: func(_ context.Context, e bites.EventSender[Tick]) error {
				for _, note := range []string{big, huge, "small"} {
					if err := e.Send("", Tick{Note: note}); err != nil {
						return err
					}
				}
				return nil
			}}, nil
		})
	})
	want := "data: {\"n\":0,\"note\":\"" + big + "\"}\n\n" + "data: {\"n\":0,\"note\":\"" + huge + "\"}\n\n" + "data: {\"n\":0,\"note\":\"small\"}\n\n"
	if got := get(t, s, "/x").Text(); got != want {
		t.Fatalf("body of %d bytes, want %d", len(got), len(want))
	}
}

func TestAnEventWithALineBreakInItsIDOrNameOrWithDataOfItsOwnIsRefusedAndNothingIsSent(t *testing.T) {
	var errs [4]error
	var nan error
	s := start(t, func(a *bites.App) {
		a.Get("/x", func(context.Context, Empty) (bites.Events[Tick], error) {
			return bites.Events[Tick]{Func: func(_ context.Context, e bites.EventSender[Tick]) error {
				errs[0] = e.Send("x\ny", Tick{})
				errs[1] = e.SendEvent(bites.Event{ID: "1\rid: 2"}, Tick{})
				errs[2] = e.SendEvent(bites.Event{ID: "a\x00b"}, Tick{})
				errs[3] = e.SendEvent(bites.Event{Data: "mine"}, Tick{})
				return nil
			}}, nil
		})
		a.Get("/nan", func(context.Context, Empty) (bites.Events[float64], error) {
			return bites.Events[float64]{Func: func(_ context.Context, e bites.EventSender[float64]) error {
				nan = e.Send("", math.NaN())
				return nil
			}}, nil
		})
	})
	if rp := get(t, s, "/x"); rp.Text() != "" {
		t.Fatalf("body %q, want none", rp.Text())
	}
	for i, err := range errs {
		if !errors.Is(err, bites.ErrBadEvent) {
			t.Errorf("send %d: %v, want ErrBadEvent", i, err)
		}
	}
	if errs[3] == nil || !strings.Contains(errs[3].Error(), "has Data") {
		t.Errorf("an Event with Data: %v, want a message naming the Data", errs[3])
	}
	if rp := get(t, s, "/nan"); rp.Text() != "" || nan == nil {
		t.Fatalf("a NaN: body %q, error %v; want nothing sent and an error", rp.Text(), nan)
	}
}

func TestAnEventSenderKeptPastTheFunctionFailsWithErrStreamClosed(t *testing.T) {
	var kept bites.EventSender[Tick]
	s := start(t, func(a *bites.App) {
		a.Get("/x", func(context.Context, Empty) (bites.Events[Tick], error) {
			return bites.Events[Tick]{Func: func(_ context.Context, e bites.EventSender[Tick]) error { kept = e; return nil }}, nil
		})
	})
	get(t, s, "/x")
	var zero bites.EventSender[Tick]
	for i, err := range []error{kept.Send("", Tick{}), kept.Comment("late"), zero.Send("", Tick{}), zero.Comment("x")} {
		if !errors.Is(err, bites.ErrStreamClosed) {
			t.Errorf("call %d: %v, want ErrStreamClosed", i, err)
		}
	}
}

func TestAnEventsWithNoFuncIsA500(t *testing.T) {
	s := start(t, func(a *bites.App) {
		a.Get("/x", func(context.Context, Empty) (bites.Events[Tick], error) { return bites.Events[Tick]{}, nil })
	})
	if rp := get(t, s, "/x"); rp.Status != 500 || !strings.Contains(rp.Get("Content-Type"), "problem+json") {
		t.Fatalf("%d %v %q, want a problem+json 500", rp.Status, rp.Header, rp.Text())
	}
}

func TestAnEventSenderUsedFromManyGoroutinesSendsEveryEventWhole(t *testing.T) {
	const senders, each = 8, 50
	s := start(t, func(a *bites.App) {
		a.Get("/x", func(context.Context, Empty) (bites.Events[Tick], error) {
			return bites.Events[Tick]{Func: func(_ context.Context, e bites.EventSender[Tick]) error {
				var wg sync.WaitGroup
				for g := range senders {
					wg.Go(func() {
						for i := range each {
							e.SendEvent(bites.Event{ID: strconv.Itoa(g)}, Tick{N: i + 1, Note: strings.Repeat("z", 100*g)})
						}
					})
				}
				wg.Wait()
				return nil
			}}, nil
		})
	})
	events := strings.Split(strings.TrimSuffix(get(t, s, "/x").Text(), "\n\n"), "\n\n")
	if len(events) != senders*each {
		t.Fatalf("%d events, want %d", len(events), senders*each)
	}
	for _, ev := range events {
		id, data, ok := strings.Cut(ev, "\ndata: ")
		g, err := strconv.Atoi(strings.TrimPrefix(id, "id: "))
		var v Tick
		if !ok || err != nil || json.Unmarshal([]byte(data), &v) != nil || v.Note != strings.Repeat("z", 100*g) {
			t.Fatalf("an event was torn: %.80q", ev)
		}
	}
}

func TestMiddlewareSeesAnEventsAsAStreamOfServerSentEventsAndMayDecorateItsHead(t *testing.T) {
	var streaming atomic.Bool
	var ctype atomic.Value
	s := start(t, func(a *bites.App) {
		a.Use(func(ctx context.Context, _ bites.Request, next bites.Next) (bites.Response, error) {
			res := next.Call(ctx)
			streaming.Store(res.Streaming())
			ctype.Store(res.ContentType())
			res.AddHeader("X-Served-By", "mw")
			return res, nil
		})
		a.Get("/x", func(context.Context, Empty) (bites.Events[Tick], error) {
			return bites.Events[Tick]{Func: func(_ context.Context, e bites.EventSender[Tick]) error { return e.Send("", Tick{N: 1}) }}, nil
		})
	})
	rp := get(t, s, "/x")
	if !streaming.Load() || ctype.Load() != "text/event-stream" || rp.Get("X-Served-By") != "mw" || rp.Text() != "data: {\"n\":1}\n\n" {
		t.Fatalf("middleware saw streaming=%v %v; answer %v %q", streaming.Load(), ctype.Load(), rp.Header, rp.Text())
	}
}

func TestAnInterfaceReturnMayHoldAnEventsItsRouteDeclared(t *testing.T) {
	s := start(t, func(a *bites.App) {
		a.Get("/x", func(_ context.Context, in struct{ Query struct{ Live bool } }) (any, error) {
			if !in.Query.Live {
				return Tick{N: 0}, nil
			}
			return bites.Events[Tick]{Func: func(_ context.Context, e bites.EventSender[Tick]) error { return e.Send("", Tick{N: 1}) }}, nil
		}, bites.Returns[Tick](), bites.Returns[bites.Events[Tick]]())
	})
	if rp := get(t, s, "/x?live=true"); rp.Get("Content-Type") != "text/event-stream" || rp.Text() != "data: {\"n\":1}\n\n" {
		t.Fatalf("live: %v %q", rp.Header, rp.Text())
	}
	if rp := get(t, s, "/x?live=false"); rp.Get("Content-Type") != "application/json" || rp.Text() != `{"n":0}` {
		t.Fatalf("not live: %v %q", rp.Header, rp.Text())
	}
}

func TestALiveEventsStreamIsEndedCleanlyWithErrServerClosedWhenShutdownStarts(t *testing.T) {
	started, cause := make(chan struct{}), make(chan error, 1)
	s, app := startApp(t, func(a *bites.App) {
		a.Get("/feed", func(context.Context, Empty) (bites.Events[Tick], error) {
			return bites.Events[Tick]{Func: func(ctx context.Context, e bites.EventSender[Tick]) error {
				e.Send("", Tick{N: 1})
				close(started)
				<-ctx.Done()
				cause <- ctx.(interface{ Cause() error }).Cause() // context.Cause cannot see it (ADR 0016)
				return ctx.Err()
			}}, nil
		})
	})
	c := s.Dial("203.0.113.9:4000")
	c.Write([]byte("GET /feed HTTP/1.1\r\nHost: t\r\n\r\n"))
	<-started
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(context.Background()) }()
	if got := <-cause; !errors.Is(got, bites.ErrServerClosed) {
		t.Fatalf("cause = %v, want ErrServerClosed", got)
	}
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v: the stream was cut, so nothing was left", err)
	}
	if rp := s.Read(c, false); rp.Text() != "data: {\"n\":1}\n\n" {
		t.Fatalf("body %q: an Events should end cleanly after its first event", rp.Text())
	}
}

func TestAStreamWithAnAttachmentNameIsADownloadOnGetHeadAndGzip(t *testing.T) {
	s := startWith(t, func(a *bites.App) {
		for i, c := range downloadCases {
			a.Get("/s/"+strconv.Itoa(i), func(context.Context, Empty) (bites.Stream, error) {
				return bites.Stream{
					Attachment: c.name,
					Header:     bites.Header{"Content-Type": {"text/csv; charset=utf-8"}},
					Func: func(_ context.Context, w *bites.StreamWriter) error {
						_, err := w.WriteString(strings.Repeat("id,title\n42,Plan\n", 200))
						return err
					},
				}, nil
			})
		}
	}, bites.WithGzip(bites.Gzip{}))
	for i, c := range downloadCases {
		target := "/s/" + strconv.Itoa(i)
		oneDisposition(t, get(t, s, target), c.want)
		oneDisposition(t, s.Do(bitetest.Request{Method: "HEAD", Target: target}), c.want)
		oneDisposition(t, s.Do(bitetest.Request{Method: "GET", Target: target, Header: map[string][]string{"Accept-Encoding": {"gzip"}}}), c.want)
	}
}

// ---- Done: the end of a response, whichever way it comes (ADR 0030) ----

// doneRoute registers /s whose handler takes a turn (taken++) and returns a Stream carrying Done
// (done++), with fn as its Func; /e does the same and returns the Stream together with an error.
func doneRoute(taken, done *atomic.Int64, fn func(context.Context, *bites.StreamWriter) error) func(*bites.App) {
	return func(a *bites.App) {
		a.Get("/s", func(context.Context, Empty) (bites.Stream, error) {
			taken.Add(1)
			return bites.Stream{Func: fn, Done: func() { done.Add(1) }}, nil
		})
		a.Get("/e", func(context.Context, Empty) (bites.Stream, error) {
			taken.Add(1)
			return bites.Stream{Func: fn, Done: func() { done.Add(1) }}, errors.New("refused after taking the turn")
		})
	}
}

// settled waits for every request to end and fails unless Done ran exactly once for each turn taken.
func settled(t *testing.T, s *bitetest.Server, taken, done *atomic.Int64) {
	t.Helper()
	s.WaitEnded()
	if taken.Load() != done.Load() || done.Load() != 1 {
		t.Fatalf("turns taken %d, Done called %d times; want one of each", taken.Load(), done.Load())
	}
}

func TestADoneOfAStreamRunsOnceAfterTheBodyOfAGET(t *testing.T) {
	var taken, done, atEnd atomic.Int64
	s := start(t, doneRoute(&taken, &done, func(_ context.Context, w *bites.StreamWriter) error {
		atEnd.Store(done.Load())
		_, err := w.WriteString("body")
		return err
	}))
	if rp := get(t, s, "/s"); rp.Status != 200 || rp.Text() != "body" {
		t.Fatalf("%d %q", rp.Status, rp.Text())
	}
	settled(t, s, &taken, &done)
	if atEnd.Load() != 0 {
		t.Fatal("Done ran before the function did")
	}
}

func TestADoneOfAStreamRunsOnceForAHEADWhoseFunctionNeverRan(t *testing.T) {
	var taken, done, ran atomic.Int64
	s := start(t, doneRoute(&taken, &done, func(context.Context, *bites.StreamWriter) error {
		ran.Add(1)
		return nil
	}))
	if rp := s.Do(bitetest.Request{Method: "HEAD", Target: "/s"}); rp.Status != 200 || rp.Text() != "" {
		t.Fatalf("%d %q", rp.Status, rp.Text())
	}
	settled(t, s, &taken, &done)
	if ran.Load() != 0 {
		t.Fatal("the function ran for a HEAD")
	}
}

func TestADoneOfAStreamRunsOnceWhenTheClientLeavesMidStream(t *testing.T) {
	var taken, done atomic.Int64
	started := make(chan struct{})
	s := start(t, doneRoute(&taken, &done, func(ctx context.Context, w *bites.StreamWriter) error {
		w.WriteString("first")
		w.Flush()
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))
	c := s.Dial("203.0.113.9:4000")
	c.Write([]byte("GET /s HTTP/1.1\r\nHost: x\r\n\r\n"))
	<-started
	c.Close()
	settled(t, s, &taken, &done)
}

func TestADoneOfAStreamRunsOnceWhenAWriteFailsBecauseTheClientIsGone(t *testing.T) {
	var taken, done atomic.Int64
	gone := make(chan struct{})
	s := start(t, doneRoute(&taken, &done, func(_ context.Context, w *bites.StreamWriter) error {
		w.WriteString("first")
		w.Flush()
		<-gone
		for {
			if _, err := w.WriteString(strings.Repeat("x", 1<<16)); err != nil {
				return err
			}
			if err := w.Flush(); err != nil {
				return err
			}
		}
	}))
	c := s.Dial("203.0.113.9:4000")
	c.Write([]byte("GET /s HTTP/1.1\r\nHost: x\r\n\r\n"))
	c.Close()
	close(gone)
	settled(t, s, &taken, &done)
}

func TestADoneOfAStreamRunsOnceWhenItsFunctionReturnsAnErrorBeforeOrAfterTheFirstByte(t *testing.T) {
	for name, fn := range map[string]func(context.Context, *bites.StreamWriter) error{
		"before": func(context.Context, *bites.StreamWriter) error { return errors.New("boom") },
		"after": func(_ context.Context, w *bites.StreamWriter) error {
			w.WriteString("x")
			w.Flush()
			return errors.New("boom")
		},
	} {
		t.Run(name, func(t *testing.T) {
			var taken, done atomic.Int64
			s := start(t, doneRoute(&taken, &done, fn))
			s.Dial("203.0.113.9:4000").Write([]byte("GET /s HTTP/1.1\r\nHost: x\r\n\r\n")) // the cut stream is no response to read
			settled(t, s, &taken, &done)
		})
	}
}

func TestADoneOfAStreamRunsOnceWhenItsFunctionPanicsBeforeOrAfterTheFirstByte(t *testing.T) {
	for name, fn := range map[string]func(context.Context, *bites.StreamWriter) error{
		"before": func(context.Context, *bites.StreamWriter) error { panic("boom") },
		"after": func(_ context.Context, w *bites.StreamWriter) error {
			w.WriteString("x")
			w.Flush()
			panic("boom")
		},
	} {
		t.Run(name, func(t *testing.T) {
			var taken, done atomic.Int64
			s := start(t, doneRoute(&taken, &done, fn))
			s.Dial("203.0.113.9:4000").Write([]byte("GET /s HTTP/1.1\r\nHost: x\r\n\r\n")) // the cut stream is no response to read
			settled(t, s, &taken, &done)
		})
	}
}

func TestADoneOfAStreamRunsOnceWhenAMiddlewareReplacesTheResponseOrFailsOrPanics(t *testing.T) {
	for _, mode := range []string{"replace", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			var taken, done, ran atomic.Int64
			s := start(t, func(a *bites.App) {
				a.Use(func(ctx context.Context, _ bites.Request, next bites.Next) (bites.Response, error) {
					res := next.Call(ctx)
					switch mode {
					case "replace":
						return bites.Problem(403, "no"), nil
					case "error":
						return res, errors.New("late failure")
					}
					panic("late panic")
				})
				doneRoute(&taken, &done, func(context.Context, *bites.StreamWriter) error {
					ran.Add(1)
					return nil
				})(a)
			})
			if rp := get(t, s, "/s"); rp.Status < 400 {
				t.Fatalf("status %d, want the middleware's answer", rp.Status)
			}
			settled(t, s, &taken, &done)
			if ran.Load() != 0 {
				t.Fatal("the function ran although the response was replaced")
			}
		})
	}
}

func TestADoneOfAStreamRunsOnceWhenTheHandlerReturnsItWithAnError(t *testing.T) {
	var taken, done, ran atomic.Int64
	s := start(t, doneRoute(&taken, &done, func(context.Context, *bites.StreamWriter) error {
		ran.Add(1)
		return nil
	}))
	if rp := get(t, s, "/e"); rp.Status != 500 {
		t.Fatalf("status %d, want the error's 500", rp.Status)
	}
	settled(t, s, &taken, &done)
	if ran.Load() != 0 {
		t.Fatal("the function ran")
	}
}

func TestADoneOfAStreamRunsOnceWhenItsFuncIsNilAndTheAnswerIsALogged500(t *testing.T) {
	var taken, done atomic.Int64
	s := start(t, doneRoute(&taken, &done, nil))
	if rp := get(t, s, "/s"); rp.Status != 500 {
		t.Fatalf("status %d", rp.Status)
	}
	settled(t, s, &taken, &done)
}

func TestADoneOfAnSSEAndOfAnEventsRunsOnceForAGETAndForAHEAD(t *testing.T) {
	var sse, ev atomic.Int64
	s := start(t, func(a *bites.App) {
		a.Get("/sse", func(context.Context, Empty) (bites.SSE, error) {
			return bites.SSE{Done: func() { sse.Add(1) }, Func: func(_ context.Context, e bites.EventWriter) error {
				return e.Send(bites.Event{Data: "x"})
			}}, nil
		})
		a.Get("/ev", func(context.Context, Empty) (bites.Events[int], error) {
			return bites.Events[int]{Done: func() { ev.Add(1) }, Func: func(_ context.Context, e bites.EventSender[int]) error {
				return e.Send("", 1)
			}}, nil
		})
	})
	for _, m := range []string{"GET", "HEAD"} {
		s.Do(bitetest.Request{Method: m, Target: "/sse"})
		s.Do(bitetest.Request{Method: m, Target: "/ev"})
	}
	s.WaitEnded()
	if sse.Load() != 2 || ev.Load() != 2 {
		t.Fatalf("Done of SSE %d, of Events %d; want 2 each", sse.Load(), ev.Load())
	}
}

func TestAPanicInADoneIsRecoveredAndLoggedAndTheConnectionServesTheNextRequest(t *testing.T) {
	s, lb := startLogged(t, func(a *bites.App) {
		a.Get("/s", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Done: func() { panic("done boom") }, Func: func(_ context.Context, w *bites.StreamWriter) error {
				_, err := w.WriteString("x")
				return err
			}}, nil
		})
	})
	c := s.Dial("203.0.113.9:4000")
	for range 2 {
		c.Write([]byte("GET /s HTTP/1.1\r\nHost: x\r\n\r\n"))
		if rp := s.Read(c, false); rp.Text() != "x" {
			t.Fatalf("body %q", rp.Text())
		}
	}
	s.WaitEnded()
	if !strings.Contains(lb.String(), "panic in a stream's Done") {
		t.Fatalf("no log of the panic:\n%s", lb.String())
	}
}

func TestADoneOfALiveStreamRunsOnceWhenShutdownCutsIt(t *testing.T) {
	var taken, done atomic.Int64
	started := make(chan struct{})
	s, app := startApp(t, func(a *bites.App) {
		a.Get("/s", func(context.Context, Empty) (bites.Stream, error) {
			taken.Add(1)
			return bites.Stream{Live: true, Done: func() { done.Add(1) }, Func: func(ctx context.Context, w *bites.StreamWriter) error {
				w.WriteString("x")
				w.Flush()
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}}, nil
		})
	})
	c := s.Dial("203.0.113.9:4000")
	c.Write([]byte("GET /s HTTP/1.1\r\nHost: x\r\n\r\n"))
	<-started
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	settled(t, s, &taken, &done)
}

func TestADoneOfAPlainStreamRunsOnceWhenShutdownDrainsIt(t *testing.T) {
	var taken, done atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	s, app := startApp(t, func(a *bites.App) {
		a.Get("/s", func(context.Context, Empty) (bites.Stream, error) {
			taken.Add(1)
			return bites.Stream{Done: func() { done.Add(1) }, Func: func(_ context.Context, w *bites.StreamWriter) error {
				w.WriteString("x")
				w.Flush()
				close(started)
				<-release
				_, err := w.WriteString("y")
				return err
			}}, nil
		})
	})
	c := s.Dial("203.0.113.9:4000")
	c.Write([]byte("GET /s HTTP/1.1\r\nHost: x\r\n\r\n"))
	<-started
	shut := make(chan error, 1)
	go func() { shut <- app.Shutdown(context.Background()) }()
	if done.Load() != 0 {
		t.Fatal("Done ran while the stream was still being drained")
	}
	close(release)
	if err := <-shut; err != nil {
		t.Fatal(err)
	}
	settled(t, s, &taken, &done)
}

func TestADoneOfAStreamReturnedAsAnInterfaceRunsOnceWhenTheHandlerReturnsItWithAnError(t *testing.T) {
	var done atomic.Int64
	s := start(t, func(a *bites.App) {
		a.Get("/s", func(context.Context, Empty) (any, error) {
			return bites.Stream{Done: func() { done.Add(1) }}, errors.New("no")
		}, bites.Returns[bites.Stream]())
	})
	get(t, s, "/s")
	s.WaitEnded()
	if done.Load() != 1 {
		t.Fatalf("Done called %d times, want 1", done.Load())
	}
}
