package bites_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	bites "github.com/nevindra/bites"
	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/memconn"
	"github.com/nevindra/bites/internal/ws"
)

// BenchmarkExploreStreams measures the streaming and body-heavy paths over memory on the real
// clock, to find where the framework's own CPU goes: WebSocket echo, bites.Topic fan-out, bites.SSE, bites.Upload
// and bites.Form, and the upgrade itself. There is no Multipart[T] and no Broker in this tree, so the
// multipart rows use bites.Upload and the fan-out rows use Topic.

type (
	exploreEchoIn  struct{ Text string }
	exploreEchoOut struct {
		Text string `json:"text"`
	}
	exploreNoIn struct{}
)

// The file is in the external test package: a type declared inside package nilogo that is named
// like a response type is refused as a WebSocket message, so the dot import keeps the names short.
func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type feedTick struct {
	N    int64  `json:"n"`
	Room string `json:"room"`
	OK   bool   `json:"ok"`
}

const exploreWSHead = "GET /ws HTTP/1.1\r\nHost: test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"

func exploreApp(b *testing.B, reg func(a *bites.App)) (*memconn.Listener, func()) {
	b.Helper()
	app := bites.New(bites.WithClock(clock.Real()), bites.WithLogger(quiet()))
	reg(app)
	ln := memconn.Listen(app.Clock())
	go app.Serve(ln)
	return ln, func() { app.Close() }
}

func exploreDial(b *testing.B, ln *memconn.Listener) net.Conn {
	b.Helper()
	c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
	if err != nil {
		b.Fatal(err)
	}
	return c
}

// exploreUpgrade sends the handshake and reads the answer up to the blank line.
func exploreUpgrade(b *testing.B, c net.Conn) {
	b.Helper()
	if _, err := c.Write([]byte(exploreWSHead)); err != nil {
		b.Fatal(err)
	}
	var acc []byte
	buf := make([]byte, 512)
	for !bytes.HasSuffix(acc, []byte("\r\n\r\n")) {
		n, err := c.Read(buf)
		if err != nil {
			b.Fatal(err)
		}
		acc = append(acc, buf[:n]...)
	}
	if !bytes.HasPrefix(acc, []byte("HTTP/1.1 101")) {
		b.Fatalf("handshake answered %q", acc)
	}
}

// exploreReadFrame reads one unmasked server frame and discards its payload; it returns the
// opcode and the payload length.
func exploreReadFrame(br *bufio.Reader) (byte, int, error) {
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return 0, 0, err
	}
	n := int(h[1] & 0x7f)
	switch n {
	case 126:
		var x [2]byte
		if _, err := io.ReadFull(br, x[:]); err != nil {
			return 0, 0, err
		}
		n = int(binary.BigEndian.Uint16(x[:]))
	case 127:
		var x [8]byte
		if _, err := io.ReadFull(br, x[:]); err != nil {
			return 0, 0, err
		}
		n = int(binary.BigEndian.Uint64(x[:]))
	}
	if _, err := br.Discard(n); err != nil {
		return 0, 0, err
	}
	return h[0] & 0x0f, n, nil
}

type exploreStruct2K struct {
	ID    int64             `json:"id"`
	Room  string            `json:"room"`
	Tags  []string          `json:"tags"`
	Attrs map[string]string `json:"attrs"`
	Blob  string            `json:"blob"`
}

func exploreBig(i int64) exploreStruct2K {
	return exploreStruct2K{ID: i, Room: "lobby", Tags: []string{"a", "b", "c"},
		Attrs: map[string]string{"k1": "v1", "k2": "v2"}, Blob: strings.Repeat("x", 1900)}
}

// exploreRoundTrip sends req on one connection over and over and reads the answer, whose length
// is taken from the first one.
func exploreRoundTrip(b *testing.B, ln *memconn.Listener, req []byte) {
	c := exploreDial(b, ln)
	buf := make([]byte, 64<<10)
	c.Write(req)
	want, _ := c.Read(buf)
	if string(buf[:12]) != "HTTP/1.1 200" {
		b.Fatalf("the first answer is not a 200:\n%s", buf[:want])
	}
	c.Close()
	c = exploreDial(b, ln)
	defer c.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		c.Write(req)
		if _, err := io.ReadFull(c, buf[:want]); err != nil {
			b.Fatal(err)
		}
	}
}

func exploreMultipart(fields int, fileSize int, parts int, partSize int) []byte {
	const bd = "explore-boundary"
	var body bytes.Buffer
	for i := range fields {
		fmt.Fprintf(&body, "--%s\r\nContent-Disposition: form-data; name=\"t%d\"\r\n\r\nvalue number %d\r\n", bd, i, i)
	}
	if fileSize > 0 {
		file := bytes.Repeat([]byte("0123456789abcdef"), (fileSize+15)/16)[:fileSize]
		fmt.Fprintf(&body, "--%s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.bin\"\r\nContent-Type: application/octet-stream\r\n\r\n", bd)
		body.Write(file)
		body.WriteString("\r\n")
	}
	if parts > 0 {
		pd := bytes.Repeat([]byte("0123456789abcdef"), (partSize+15)/16)[:partSize]
		for i := range parts {
			fmt.Fprintf(&body, "--%s\r\nContent-Disposition: form-data; name=\"p%d\"; filename=\"p%d.bin\"\r\nContent-Type: application/octet-stream\r\n\r\n", bd, i, i)
			body.Write(pd)
			body.WriteString("\r\n")
		}
	}
	fmt.Fprintf(&body, "--%s--\r\n", bd)
	head := "POST /up HTTP/1.1\r\nHost: x\r\nContent-Type: multipart/form-data; boundary=" + bd + "\r\nContent-Length: " + strconv.Itoa(body.Len()) + "\r\n\r\n"
	return append([]byte(head), body.Bytes()...)
}

type exploreUpIn struct{ Body bites.Upload }

type exploreForm20 struct {
	A1, A2, A3, A4, A5, A6, A7, A8 string
	N1, N2, N3, N4, N5, N6         int
	B1, B2                         bool
	F1, F2                         float64
}

type exploreFormIn struct{ Body bites.Form[exploreForm20] }

func BenchmarkExploreStreams(b *testing.B) {
	// 1. WebSocket echo, one Socket.
	for _, mode := range []string{"bytes", "json"} {
		for _, size := range []int{32, 1 << 10, 64 << 10} {
			b.Run(fmt.Sprintf("ws_echo_%s/%dB", mode, size), func(b *testing.B) {
				ln, stop := exploreApp(b, func(a *bites.App) {
					if mode == "bytes" {
						a.WebSocket("/ws", func(_ context.Context, _ exploreNoIn, s *bites.Socket[[]byte]) (bites.Inbox[[]byte], error) {
							return bites.Inbox[[]byte]{OnMessage: func(_ context.Context, m []byte) error { return s.Send(m) }}, nil
						}, bites.MaxMessage(1<<20))
					} else {
						a.WebSocket("/ws", func(_ context.Context, _ exploreNoIn, s *bites.Socket[exploreEchoOut]) (bites.Inbox[exploreEchoIn], error) {
							return bites.Inbox[exploreEchoIn]{OnMessage: func(_ context.Context, m exploreEchoIn) error { return s.Send(exploreEchoOut{Text: m.Text}) }}, nil
						}, bites.MaxMessage(1<<20))
					}
				})
				defer stop()
				c := exploreDial(b, ln)
				defer c.Close()
				exploreUpgrade(b, c)
				br := bufio.NewReaderSize(c, 128<<10)
				payload := bytes.Repeat([]byte("a"), size)
				if mode == "json" {
					payload = []byte(`{"text":"` + string(payload) + `"}`)
				}
				frame := ws.AppendClientFrame(nil, true, ws.OpText, payload, [4]byte{9, 8, 7, 6})
				c.Write(frame)
				op, n, err := exploreReadFrame(br)
				wantOp := byte(ws.OpBinary)
				if mode == "json" {
					wantOp = byte(ws.OpText)
				}
				if err != nil || op != wantOp || n < size {
					b.Fatalf("first echo: op %d len %d err %v", op, n, err)
				}
				b.SetBytes(int64(size))
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					c.Write(frame)
					if _, _, err := exploreReadFrame(br); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N), "ns/message")
			})
		}
	}

	// 2. bites.Topic fan-out.
	for _, subs := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("topic_publish/%dsubs", subs), func(b *testing.B) {
			var topic bites.Topic[exploreEchoOut]
			ln, stop := exploreApp(b, func(a *bites.App) {
				a.WebSocket("/ws", func(_ context.Context, _ exploreNoIn, s *bites.Socket[exploreEchoOut]) (bites.Inbox[exploreEchoIn], error) {
					topic.Subscribe(s)
					return bites.Inbox[exploreEchoIn]{}, nil
				})
			})
			defer stop()
			var got atomic.Int64
			for range subs {
				c := exploreDial(b, ln)
				exploreUpgrade(b, c)
				go func() {
					buf := make([]byte, 32<<10)
					for {
						n, err := c.Read(buf)
						if err != nil {
							return
						}
						got.Add(int64(n))
					}
				}()
				defer c.Close()
			}
			for topic.Len() < subs {
				runtime.Gosched()
			}
			msg := exploreEchoOut{Text: "hello"}
			perMsg := int64(2 + len(`{"text":"hello"}`))
			if err := topic.Publish(msg); err != nil {
				b.Fatal(err)
			}
			for got.Load() < perMsg*int64(subs) {
				runtime.Gosched()
			}
			got.Store(0)
			b.ReportAllocs()
			b.ResetTimer()
			var sent int64
			for b.Loop() {
				topic.Publish(msg)
				sent++
				for got.Load() < sent*perMsg*int64(subs) {
					runtime.Gosched()
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N), "ns/publish")
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(subs), "ns/delivery")
		})
	}

	// 3. bites.SSE and Events.
	const evN = 1000
	for _, tc := range []struct {
		name string
		reg  func(a *bites.App)
	}{
		{"sse_small", func(a *bites.App) {
			a.Get("/feed", func(_ context.Context, _ exploreNoIn) (bites.SSE, error) {
				return bites.SSE{Func: func(_ context.Context, e bites.EventWriter) error {
					for range evN {
						if err := e.Send(bites.Event{ID: "42", Name: "tick", Data: "a line of 30 bytes of event data"}); err != nil {
							return err
						}
					}
					return nil
				}}, nil
			})
		}},
		{"events_small", func(a *bites.App) {
			a.Get("/feed", func(_ context.Context, _ exploreNoIn) (bites.Events[feedTick], error) {
				return bites.Events[feedTick]{Func: func(_ context.Context, e bites.EventSender[feedTick]) error {
					for i := range int64(evN) {
						if err := e.SendEvent(bites.Event{ID: "42", Name: "tick"}, feedTick{N: i, Room: "lobby", OK: true}); err != nil {
							return err
						}
					}
					return nil
				}}, nil
			})
		}},
		{"events_2KiB", func(a *bites.App) {
			a.Get("/feed", func(_ context.Context, _ exploreNoIn) (bites.Events[exploreStruct2K], error) {
				return bites.Events[exploreStruct2K]{Func: func(_ context.Context, e bites.EventSender[exploreStruct2K]) error {
					for i := range int64(evN) {
						if err := e.SendEvent(bites.Event{ID: "42", Name: "tick"}, exploreBig(i)); err != nil {
							return err
						}
					}
					return nil
				}}, nil
			})
		}},
	} {
		b.Run("stream_"+tc.name, func(b *testing.B) {
			ln, stop := exploreApp(b, tc.reg)
			defer stop()
			c := exploreDial(b, ln)
			defer c.Close()
			req := []byte("GET /feed HTTP/1.1\r\nHost: x\r\n\r\n")
			buf := make([]byte, 256<<10)
			do := func() int {
				c.Write(req)
				total := 0
				var tail []byte
				for {
					m, err := c.Read(buf)
					total += m
					tail = append(tail, buf[:m]...)
					if len(tail) > 5 {
						tail = tail[len(tail)-5:]
					}
					if err != nil || string(tail) == "0\r\n\r\n" {
						return total
					}
				}
			}
			n := do()
			if n < evN*30 {
				b.Fatalf("the stream answered %d bytes", n)
			}
			b.SetBytes(int64(n))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				do()
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/evN, "ns/event")
		})
	}

	// 5. Uploads, read part by part.
	for _, tc := range []struct {
		name                        string
		fields, file, parts, partSz int
	}{
		{"3fields+4KiB", 3, 4 << 10, 0, 0},
		{"3fields+1MiB", 3, 1 << 20, 0, 0},
		{"10parts_x_64KiB", 0, 0, 10, 64 << 10},
	} {
		b.Run("upload_"+tc.name, func(b *testing.B) {
			var seen atomic.Int64
			ln, stop := exploreApp(b, func(a *bites.App) {
				a.Post("/up", func(_ context.Context, in exploreUpIn) (bites.Text, error) {
					var total int64
					err := in.Body.Each(func(p bites.Part) error {
						var buf [32 << 10]byte
						for {
							n, err := p.Read(buf[:])
							total += int64(n)
							if err != nil {
								return nil
							}
						}
					})
					seen.Store(total)
					return bites.Text{Body: "ok"}, err
				}, bites.MaxBody(64<<20))
			})
			defer stop()
			req := exploreMultipart(tc.fields, tc.file, tc.parts, tc.partSz)
			c := exploreDial(b, ln)
			c.Write(req)
			buf := make([]byte, 4096)
			n, _ := c.Read(buf)
			c.Close()
			if string(buf[:12]) != "HTTP/1.1 200" {
				b.Fatalf("the first answer is not a 200:\n%s", buf[:n])
			}
			if want := int64(tc.file + tc.parts*tc.partSz); seen.Load() < want {
				b.Fatalf("handler saw %d part bytes, want at least %d", seen.Load(), want)
			}
			b.SetBytes(int64(tc.file + tc.parts*tc.partSz))
			c = exploreDial(b, ln)
			defer c.Close()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				c.Write(req)
				if _, err := io.ReadFull(c, buf[:n]); err != nil {
					b.Fatal(err)
				}
			}
		})
	}

	// 6. bites.Form with 20 fields.
	b.Run("form_20fields", func(b *testing.B) {
		ln, stop := exploreApp(b, func(a *bites.App) {
			a.Post("/form", func(_ context.Context, in exploreFormIn) (bites.Text, error) {
				return bites.Text{Body: "ok"}, nil
			})
		})
		defer stop()
		body := "a1=alpha&a2=beta+gamma&a3=delta%40x.io&a4=epsilon&a5=zeta&a6=eta&a7=theta&a8=iota&n1=1&n2=22&n3=333&n4=4444&n5=55555&n6=666666&b1=true&b2=false&f1=1.5&f2=2.25"
		req := []byte("POST /form HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body)
		exploreRoundTrip(b, ln, req)
	})

	// 7. The upgrade itself.
	b.Run("ws_upgrade_open_close", func(b *testing.B) {
		ln, stop := exploreApp(b, func(a *bites.App) {
			a.WebSocket("/ws", func(_ context.Context, _ exploreNoIn, s *bites.Socket[[]byte]) (bites.Inbox[[]byte], error) {
				return bites.Inbox[[]byte]{}, nil
			})
		})
		defer stop()
		closeFrame := ws.AppendClientFrame(nil, true, ws.OpClose, binary.BigEndian.AppendUint16(nil, 1000), [4]byte{1, 2, 3, 4})
		one := func() {
			c := exploreDial(b, ln)
			exploreUpgrade(b, c)
			c.Write(closeFrame)
			io.Copy(io.Discard, c)
			c.Close()
		}
		one()
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			one()
		}
	})
}
