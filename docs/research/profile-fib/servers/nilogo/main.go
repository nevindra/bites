// The nilo-go server of the harness: the Input struct is the contract, and validation (a path
// id that is an integer, a required integer page, an optional bool expand, a JSON body with
// every field present and no unknown one) comes from the types alone.
package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"time"

	nilogo "github.com/nevindra/bites"

	"github.com/nevindra/bites/research/profile-fib/common"
	"github.com/nevindra/bites/research/profile-fib/prof"
)

type postIn struct {
	Path  struct{ ID int64 }
	Query struct {
		Expand nilogo.Opt[bool]
		Page   int
	}
	Body struct {
		Name  string
		Email string
		Age   int
	}
}

type getIn struct {
	Path struct{ ID int64 }
}

func main() {
	flag.Parse()
	common.StartStats()
	prof.Start()
	app := nilogo.New(nilogo.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Post("/users/{id}", func(_ context.Context, in postIn) (common.Created, error) {
		return common.Created{ID: in.Path.ID, Name: in.Body.Name, Email: in.Body.Email, Age: in.Body.Age, Page: in.Query.Page}, nil
	})
	app.Get("/users/{id}", func(_ context.Context, in getIn) (common.User, error) {
		return common.User{ID: in.Path.ID, Name: "ada", Email: "ada@example.dev", Age: 36}, nil
	})
	// GET /block: a handler that waits 10 ms (the block experiment).
	app.Get("/block", func(_ context.Context, _ struct{}) (common.Blocked, error) {
		time.Sleep(common.BlockDelay)
		return common.Blocked{OK: true}, nil
	})
	if err := app.Listen(*common.Addr); err != nil {
		panic(err)
	}
}
