package bites

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/memconn"
)

// BenchmarkHotPaths is the CPU profile's load for step 17 of the roadmap: each hot path of the
// reference workload over memory on the real clock, one connection a goroutine, so a profile of
// it shows the framework's own work (router, decoders, middleware, Providers, Observers) without
// the kernel. The engine's syscalls, deadlines on a real socket and parking are measured over TCP
// on Linux instead (docs/research/profile-fib). The routes and requests are those of bench/ and
// of the allocation rows in budget_test.go.
func BenchmarkHotPaths(b *testing.B) {
	type getIn struct{ Path struct{ ID int64 } }
	getHandler := func(_ context.Context, in getIn) (budgetOut, error) {
		return budgetOut{ID: in.Path.ID, Name: "Ada Lovelace", Email: "ada@example.com", Age: 36}, nil
	}
	callerHandler := func(ctx context.Context, in budgetProvidedIn) (budgetOut, error) {
		out, err := budgetHandler(ctx, in.plain())
		out.Name, out.Email = in.Caller.Name, in.S.Token
		return out, err
	}
	text := slog.New(slog.NewTextHandler(io.Discard, nil))
	jsonLog := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cors := CORS(CORSOptions{Origins: []string{"https://app.example.com"}, Credentials: true, ExposeHeaders: []string{"X-Request-ID"}})
	const target = "/users/42?expand=true&page=3"
	const origin = "Origin: https://app.example.com\r\n"
	for _, v := range []struct {
		name    string
		request []byte
		reg     func(a *App)
	}{
		{"GET", []byte("GET /users/42 HTTP/1.1\r\nHost: x\r\n\r\n"), func(a *App) { a.Get("/users/{id}", getHandler) }},
		{"POST", post(target, typedBody), func(a *App) { a.Post("/users/{id}", budgetHandler) }},
		{"POST_validate", post(target, typedBody), func(a *App) { a.Post("/users/{id}", budgetValidHandler) }},
		{"POST_form", formPost(target, "name=Ada+Lovelace&email=ada%40example.com&age=36"), func(a *App) { a.Post("/users/{id}", budgetFormHandler) }},
		{"POST_mw1", post(target, typedBody), func(a *App) { a.Post("/users/{id}", budgetHandler, Use(readsHeader)) }},
		{"POST_mw3", post(target, typedBody), func(a *App) { a.Post("/users/{id}", budgetHandler, Use(readsHeader, readsHeader, readsHeader)) }},
		{"POST_providers", postAuth(target, typedBody), func(a *App) {
			a.Provide(budgetSessionOf)
			a.Provide(budgetCallerOf)
			a.Post("/users/{id}", callerHandler)
		}},
		{"POST_requestid", post(target, typedBody), func(a *App) { a.Use(RequestID()); a.Post("/users/{id}", budgetHandler) }},
		{"POST_cors", postWith(target, origin, typedBody), func(a *App) { a.Use(cors); a.Post("/users/{id}", budgetHandler) }},
		{"POST_timeout", post(target, typedBody), func(a *App) { a.Use(Timeout(time.Second)); a.Post("/users/{id}", budgetHandler) }},
		{"POST_accesslog_text", post(target, typedBody), func(a *App) {
			WithObserver(AccessLog(AccessLogOptions{Logger: text}))(a)
			a.Post("/users/{id}", budgetHandler)
		}},
		{"POST_accesslog_json", post(target, typedBody), func(a *App) {
			WithObserver(AccessLog(AccessLogOptions{Logger: jsonLog}))(a)
			a.Post("/users/{id}", budgetHandler)
		}},
		{"POST_all_four", postWith(target, origin+"X-Request-ID: abc\r\n", typedBody), func(a *App) {
			WithObserver(AccessLog(AccessLogOptions{Logger: jsonLog}))(a)
			a.Use(RequestID(), cors, Timeout(time.Second))
			a.Post("/users/{id}", budgetHandler)
		}},
	} {
		b.Run(v.name, func(b *testing.B) {
			app := New(WithClock(clock.Real()), WithLogger(quiet()))
			v.reg(app)
			ln := memconn.Listen(app.Clock())
			go app.Serve(ln)
			defer app.Close()
			// The answer's length, taken once, so each goroutine reads exactly one answer a request.
			c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
			if err != nil {
				b.Fatal(err)
			}
			buf := make([]byte, 4096)
			c.Write(v.request)
			want, _ := c.Read(buf)
			if string(buf[:12]) != "HTTP/1.1 200" {
				b.Fatalf("the first answer is not a 200:\n%s", buf[:want])
			}
			c.Close()
			var mu sync.Mutex
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				mu.Lock()
				c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
				mu.Unlock()
				if err != nil {
					b.Error(err)
					return
				}
				defer c.Close()
				buf := make([]byte, want)
				for pb.Next() {
					c.Write(v.request)
					if _, err := io.ReadFull(c, buf); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}
