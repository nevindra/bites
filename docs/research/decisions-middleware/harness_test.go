package probe

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	nilogo "github.com/nevindra/bites"
	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/memconn"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func rig(t testing.TB, register func(*nilogo.App)) net.Conn {
	t.Helper()
	app := nilogo.New(nilogo.WithClock(clock.NewFake(time.Now())), nilogo.WithLogger(quiet()))
	register(app)
	ln := memconn.Listen(app.Clock())
	go app.Serve(ln)
	t.Cleanup(func() { app.Close() })
	c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// keepAlive drives one request over and over on one connection and returns mallocs per request
// (same method as budget_test.go in the root package).
func keepAlive(t testing.TB, c net.Conn, request []byte, wantStatus string) float64 {
	t.Helper()
	buf := make([]byte, 4096)
	want := 0
	do := func() {
		c.Write(request)
		if want == 0 {
			want, _ = c.Read(buf)
			return
		}
		io.ReadFull(c, buf[:want])
	}
	do()
	if got := string(buf[:len(wantStatus)]); got != wantStatus {
		t.Fatalf("first response %q, want prefix %q\n%s", got, wantStatus, buf[:want])
	}
	for range 100 {
		do()
	}
	return testing.AllocsPerRun(2000, do)
}

func post(target, body string) []byte {
	return []byte("POST " + target + " HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body)
}

type In struct {
	Path  struct{ ID int64 }
	Query struct{ Page int }
	Body  struct {
		Name  string
		Email string
		Age   int
	}
}

type Out struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age"`
	Page  int    `json:"page"`
}

func handler(_ context.Context, in In) (Out, error) {
	return Out{ID: in.Path.ID, Name: in.Body.Name, Email: in.Body.Email, Age: in.Body.Age, Page: in.Query.Page}, nil
}

const body = `{"name":"Ada Lovelace","email":"ada@example.com","age":36}`
