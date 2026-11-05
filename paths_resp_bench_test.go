package bites_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	bites "github.com/nevindra/bites"
	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/memconn"
	"github.com/nevindra/bites/stdhttp"
)

// Exploration benchmarks for the response side: error answers, large JSON answers and bodies,
// gzip, files, conditional and range requests, a mounted net/http handler, the OpenAPI document,
// pipelining, and Registration. Not a guard: a harness for finding where CPU goes.

type xrItem struct {
	ID    int64    `json:"id"`
	Name  string   `json:"name"`
	Email string   `json:"email"`
	Age   int      `json:"age"`
	Tags  []string `json:"tags"`
	Score float64  `json:"score"`
}

func xrItems(n int) []xrItem {
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet"}
	out := make([]xrItem, n)
	for i := range out {
		out[i] = xrItem{ID: int64(i + 1), Name: words[i%10] + " " + words[(i*7+3)%10] + strconv.Itoa(i*31), Email: words[i%10] + strconv.Itoa(i) + "@example.com",
			Age: 18 + i%60, Tags: []string{words[(i*3)%10], words[(i*5)%10]}, Score: float64(i) * 1.25}
	}
	return out
}

type xrNoIn struct{}

type xrIDIn struct{ Path struct{ ID int64 } }

type xrBodyIn struct {
	Body struct {
		Title  string
		Author struct {
			Name  string
			Email string
			Age   int
		}
		Items []xrItem
	}
}

type xrBodyPathIn struct {
	Path struct{ ID int64 }
	Body struct {
		Title string
		Items []xrItem
	}
}

type xrValidPathIn struct {
	Path struct{ ID int64 }
	Body struct{ Age int }
}

func (in xrValidPathIn) Validate() error { return nil }

type xrValidIn struct {
	Body struct{ Age int }
}

func (in xrValidIn) Validate() error {
	if in.Body.Age < 0 {
		return bites.Invalid("body.age", "must not be negative")
	}
	return nil
}

type xrFileIn struct{ Path struct{ Name string } }

var xrMu sync.Mutex

// xrRead reads one answer: the status code, and the head when keep is not nil. The body is
// skipped by Content-Length or chunk by chunk; a 304 or 204 has none.
func xrRead(br *bufio.Reader, keep *strings.Builder) (int, error) {
	line, err := br.ReadSlice('\n')
	if err != nil {
		return 0, err
	}
	if keep != nil {
		keep.Write(line)
	}
	if len(line) < 12 {
		return 0, fmt.Errorf("short status line %q", line)
	}
	status := int(line[9]-'0')*100 + int(line[10]-'0')*10 + int(line[11]-'0')
	clen, chunked := -1, false
	for {
		line, err = br.ReadSlice('\n')
		if err != nil {
			return 0, err
		}
		if keep != nil {
			keep.Write(line)
		}
		if len(line) <= 2 {
			break
		}
		if len(line) > 16 && bytes.EqualFold(line[:15], []byte("content-length:")) {
			clen, _ = strconv.Atoi(strings.TrimSpace(string(line[15:])))
		} else if len(line) > 20 && bytes.EqualFold(line[:18], []byte("transfer-encoding:")) && bytes.Contains(bytes.ToLower(line), []byte("chunked")) {
			chunked = true
		}
	}
	switch {
	case status == 304 || status == 204 || status/100 == 1:
		return status, nil
	case chunked:
		for {
			line, err = br.ReadSlice('\n')
			if err != nil {
				return 0, err
			}
			n, _ := strconv.ParseInt(strings.TrimSpace(string(line)), 16, 64)
			if _, err = br.Discard(int(n) + 2); err != nil {
				return 0, err
			}
			if n == 0 {
				return status, nil
			}
		}
	case clen >= 0:
		_, err = br.Discard(clen)
		return status, err
	}
	return 0, fmt.Errorf("answer %d has no length", status)
}

// xrRun runs one sub-benchmark: the request bytes are written in one Write and `answers`
// answers read, one connection a RunParallel goroutine. The status is checked once before timing.
func xrRun(b *testing.B, request []byte, answers, wantStatus int, register func(a *bites.App), opts ...bites.Option) {
	app := bites.New(append([]bites.Option{bites.WithClock(clock.Real()), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}, opts...)...)
	register(app)
	ln := memconn.Listen(app.Clock())
	go app.Serve(ln)
	defer app.Close()
	c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
	if err != nil {
		b.Fatal(err)
	}
	var head strings.Builder
	br := bufio.NewReaderSize(c, 64<<10)
	c.Write(request)
	for range answers {
		got, err := xrRead(br, &head)
		if err != nil || got != wantStatus {
			b.Fatalf("status %d (err %v), want %d:\n%s", got, err, wantStatus, head.String())
		}
	}
	c.Close()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		xrMu.Lock()
		c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
		xrMu.Unlock()
		if err != nil {
			b.Error(err)
			return
		}
		defer c.Close()
		br := bufio.NewReaderSize(c, 64<<10)
		for pb.Next() {
			c.Write(request)
			for range answers {
				if _, err := xrRead(br, nil); err != nil {
					b.Error(err)
					return
				}
			}
		}
	})
}

func xrGet(target, extra string) []byte {
	return []byte("GET " + target + " HTTP/1.1\r\nHost: x\r\n" + extra + "\r\n")
}

func xrPost(target, body string) []byte {
	return []byte("POST " + target + " HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body)
}

func xrJSONBody(items int) string {
	var sb strings.Builder
	sb.WriteString(`{"title":"a report with a title","author":{"name":"Ada Lovelace","email":"ada@example.com","age":36},"items":[`)
	for i, it := range xrItems(items) {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":%d,"name":%q,"email":%q,"age":%d,"tags":["%s","%s"],"score":%v}`, it.ID, it.Name, it.Email, it.Age, it.Tags[0], it.Tags[1], it.Score)
	}
	sb.WriteString(`]}`)
	return sb.String()
}

func BenchmarkExploreResponses(b *testing.B) {
	items := map[int][]xrItem{}
	for _, n := range []int{45, 100, 700, 1000} {
		items[n] = xrItems(n)
	}
	list := func(n int) func(a *bites.App) {
		return func(a *bites.App) {
			a.Get("/l", func(context.Context, xrNoIn) ([]xrItem, error) { return items[n], nil })
		}
	}
	dir := b.TempDir()
	small, big := make([]byte, 4<<10), make([]byte, 1<<20)
	for i := range small {
		small[i] = byte('a' + i%23)
	}
	for i := range big {
		big[i] = byte('a' + (i*7)%251%26)
	}
	os.WriteFile(filepath.Join(dir, "small.txt"), small, 0o644)
	os.WriteFile(filepath.Join(dir, "big.txt"), big, 0o644)
	root, err := os.OpenRoot(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer root.Close()
	fileRoute := func(a *bites.App) {
		a.Get("/files/{name}", func(_ context.Context, in xrFileIn) (bites.File, error) {
			return bites.File{Root: root, Name: in.Path.Name}, nil
		})
	}
	// The ETag of small.txt, asked once from a throwaway app.
	etag := func() string {
		app := bites.New(bites.WithClock(clock.Real()), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		fileRoute(app)
		ln := memconn.Listen(app.Clock())
		go app.Serve(ln)
		defer app.Close()
		c, _ := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
		defer c.Close()
		c.Write(xrGet("/files/small.txt", ""))
		var h strings.Builder
		xrRead(bufio.NewReader(c), &h)
		for _, l := range strings.Split(h.String(), "\r\n") {
			if strings.HasPrefix(strings.ToLower(l), "etag:") {
				return strings.TrimSpace(l[5:])
			}
		}
		b.Fatalf("no etag:\n%s", h.String())
		return ""
	}()
	gz := bites.WithGzip(bites.Gzip{})

	hello := func(a *bites.App) {
		a.Get("/users/{id}", func(_ context.Context, in xrIDIn) (xrItem, error) { return items[45][0], nil })
		a.Post("/users", func(_ context.Context, in xrBodyIn) (xrItem, error) { return items[45][0], nil })
		a.Post("/valid", func(_ context.Context, in xrValidIn) (xrItem, error) { return items[45][0], nil })
	}
	pipe := bytes.Repeat(xrGet("/users/42", ""), 16)
	openapiApp := func(a *bites.App) {
		for i := range 50 {
			p := fmt.Sprintf("/r%d/{id}", i)
			switch i % 3 {
			case 0:
				a.Get(p, func(_ context.Context, in xrIDIn) (xrItem, error) { return xrItem{}, nil })
			case 1:
				a.Post(p, func(_ context.Context, in xrBodyPathIn) (xrItem, error) { return xrItem{}, nil })
			default:
				a.Put(p, func(_ context.Context, in xrValidPathIn) ([]xrItem, error) { return nil, nil })
			}
		}
		a.OpenAPI("/openapi.json", bites.Info{Title: "T", Version: "1"})
	}
	type v struct {
		name    string
		req     []byte
		answers int
		status  int
		reg     func(a *bites.App)
		opts    []bites.Option
	}
	for _, c := range []v{
		{"err_404", xrGet("/nope", ""), 1, 404, hello, nil},
		{"err_405", []byte("DELETE /users/42 HTTP/1.1\r\nHost: x\r\n\r\n"), 1, 405, hello, nil},
		{"err_400_path", xrGet("/users/abc", ""), 1, 400, hello, nil},
		{"err_400_json", xrPost("/users", `{"title": "x", "author": {`), 1, 400, hello, nil},
		{"err_400_validate", xrPost("/valid", `{"age": -1}`), 1, 400, hello, nil},
		{"list_100", xrGet("/l", ""), 1, 200, list(100), nil},
		{"list_1000", xrGet("/l", ""), 1, 200, list(1000), nil},
		{"body_16KiB_in", xrPost("/users", xrJSONBody(100)), 1, 200, hello, nil},
		{"gzip_4KiB", xrGet("/l", "Accept-Encoding: gzip\r\n"), 1, 200, list(45), []bites.Option{gz}},
		{"gzip_64KiB", xrGet("/l", "Accept-Encoding: gzip\r\n"), 1, 200, list(700), []bites.Option{gz}},
		{"nogzip_4KiB_ref", xrGet("/l", ""), 1, 200, list(45), nil},
		{"nogzip_64KiB_ref", xrGet("/l", ""), 1, 200, list(700), nil},
		{"file_4KiB", xrGet("/files/small.txt", ""), 1, 200, fileRoute, nil},
		{"file_1MiB", xrGet("/files/big.txt", ""), 1, 200, fileRoute, nil},
		{"file_304", xrGet("/files/small.txt", "If-None-Match: "+etag+"\r\n"), 1, 304, fileRoute, nil},
		{"file_range", xrGet("/files/big.txt", "Range: bytes=1000-5095\r\n"), 1, 206, fileRoute, nil},
		{"static_4KiB", xrGet("/s/small.txt", ""), 1, 200, func(a *bites.App) { a.Static("/s", bites.Static{Root: root}) }, nil},
		{"html_bytes", xrGet("/h", ""), 1, 200, func(a *bites.App) {
			page := bytes.Repeat([]byte("<p>hello</p>"), 300)
			a.Get("/h", func(context.Context, xrNoIn) (bites.Bytes, error) {
				return bites.Bytes{ContentType: "text/html; charset=utf-8", Body: page}, nil
			})
		}, nil},
		{"stdhttp_mount", xrGet("/plain", ""), 1, 200, func(a *bites.App) {
			stdhttp.Mount(a, "/plain", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"id":42,"name":"Ada Lovelace","email":"ada@example.com","age":36}`))
			}))
		}, nil},
		{"openapi_50routes", xrGet("/openapi.json", ""), 1, 200, openapiApp, nil},
		{"pipelined_16_GET", pipe, 16, 200, hello, nil},
		{"single_GET_ref", xrGet("/users/42", ""), 1, 200, hello, nil},
	} {
		b.Run(c.name, func(b *testing.B) { xrRun(b, c.req, c.answers, c.status, c.reg, c.opts...) })
	}
}

func BenchmarkExploreRegistration(b *testing.B) {
	for _, n := range []int{50, 500} {
		b.Run(fmt.Sprintf("routes_%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				app := bites.New(bites.WithClock(clock.Real()), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
				for i := range n {
					p := fmt.Sprintf("/r%d/{id}", i)
					switch i % 4 {
					case 0:
						app.Get(p, func(_ context.Context, in xrIDIn) (xrItem, error) { return xrItem{}, nil })
					case 1:
						app.Post(p, func(_ context.Context, in xrBodyPathIn) (xrItem, error) { return xrItem{}, nil })
					case 2:
						app.Put(p, func(_ context.Context, in xrValidPathIn) ([]xrItem, error) { return nil, nil })
					default:
						app.Delete(p, func(_ context.Context, in xrIDIn) (bites.NoContent, error) { return bites.NoContent{}, nil })
					}
				}
				app.OpenAPI("/openapi.json", bites.Info{Title: "T", Version: "1"})
				bites.StartForTest(app)
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(n), "ns/route")
		})
	}
}
