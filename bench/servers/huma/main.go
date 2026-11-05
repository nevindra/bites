// The Huma server of the harness, on net/http through the humago adapter with DefaultConfig,
// as in docs/research/std-huma. Types and tags are the contract; validation is JSON Schema
// from them: the body's members are required and unknown ones refused, page is required, a
// type mismatch is a 422 (not a 400) as problem+json. DefaultConfig also adds a "$schema"
// member and a Link header to every JSON answer, so its responses are larger than the others.
package main

import (
	"context"
	"flag"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nevindra/bites/bench/common"
)

type postIn struct {
	ID     int64 `path:"id"`
	Expand bool  `query:"expand"`
	Page   int   `query:"page" required:"true"`
	Body   common.NewUser
}

type postOut struct{ Body common.Created }

type getIn struct {
	ID int64 `path:"id"`
}

type getOut struct{ Body common.User }

type blockOut struct{ Body common.Blocked }

func main() {
	flag.Parse()
	common.StartStats()
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("bench", "1.0.0"))
	huma.Post(api, "/users/{id}", func(_ context.Context, in *postIn) (*postOut, error) {
		return &postOut{Body: common.Created{ID: in.ID, Name: in.Body.Name, Email: in.Body.Email, Age: in.Body.Age, Page: in.Page}}, nil
	})
	huma.Get(api, "/users/{id}", func(_ context.Context, in *getIn) (*getOut, error) {
		return &getOut{Body: common.User{ID: in.ID, Name: "ada", Email: "ada@example.dev", Age: 36}}, nil
	})
	// GET /block: a handler that waits 10 ms (the block experiment).
	huma.Get(api, "/block", func(_ context.Context, _ *struct{}) (*blockOut, error) {
		time.Sleep(common.BlockDelay)
		return &blockOut{Body: common.Blocked{OK: true}}, nil
	})
	if err := http.ListenAndServe(*common.Addr, mux); err != nil {
		panic(err)
	}
}
