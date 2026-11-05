// The bites server of the harness: the Input struct is the contract, and validation (a path
// id that is an integer, a required integer page, an optional bool expand, a JSON body with
// every field present and no unknown one) comes from the types alone.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"flag"
	"io"
	"log/slog"
	"math/big"
	"os"
	"strconv"
	"time"

	"github.com/nevindra/bites"

	"github.com/nevindra/bites/bench/common"
)

type postIn struct {
	Path  struct{ ID int64 }
	Query struct {
		Expand bites.Opt[bool]
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

var useTLS = flag.Bool("tls", false, "serve TLS with a self-signed ecdsa P-256 certificate made at start-up; default off")

func selfSigned() *tls.Config {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "bench.test"}, DNSNames: []string{"bench.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

// routeMiddleware is BITES_MW middleware on every route (docs/research/middleware-core): each
// reads a header and the path and adds a response header, which is what most real ones do.
// Unset or 0 it is none, the route of milestone 1. BITES_MWKIND=bare makes each a pass-through.
func routeMiddleware() []bites.RouteOption {
	n, _ := strconv.Atoi(os.Getenv("BITES_MW"))
	bare := os.Getenv("BITES_MWKIND") == "bare" // pass through: only the cost of the chain
	if n <= 0 {
		return nil
	}
	mw := func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		if bare {
			return next.Call(ctx), nil
		}
		_ = req.Header("authorization")
		_ = req.Path()
		res := next.Call(ctx)
		res.AddHeader("X-Served-By", "bench")
		return res, nil
	}
	ms := make([]bites.Middleware, n)
	for i := range ms {
		ms[i] = mw
	}
	return []bites.RouteOption{bites.Use(ms...)}
}

// The BITES_VARIANT routes (docs/research/m2-request-path): the same two requests through what
// milestone 2 steps 6 to 8 and 7 added to the request path. Unset, the routes of milestone 1.
//
//	prov   the Input asks for a Caller, whose Provider needs a Session, whose Provider reads a header
//	valid  constraint types in every field of both Inputs, and a Validate that returns nil
//	mw4    the four first-party middleware: CORS, RequestID and AccessLog (text, discarded) on the
//	       app, Timeout(5s) on each route
//	cors, rid, log, timeout   one of the four alone, placed as in mw4

type session struct{ Token string }

type caller struct {
	ID   int64
	Name string
}

func sessionOf(_ context.Context, req bites.Request, _ struct{}) (session, error) {
	return session{Token: req.Header("authorization")}, nil
}

func callerOf(_ context.Context, _ bites.Request, d struct{ S session }) (caller, error) {
	return caller{ID: 1, Name: d.S.Token}, nil
}

type provPostIn struct {
	Caller caller
	Path   struct{ ID int64 }
	Query  struct {
		Expand bites.Opt[bool]
		Page   int
	}
	Body struct {
		Name  string
		Email string
		Age   int
	}
}

type provGetIn struct {
	Caller caller
	Path   struct{ ID int64 }
}

type email string

func (email) Length() (int, int) { return 3, 254 }

type validPostIn struct {
	Path  struct{ ID bites.PositiveInt64 }
	Query struct {
		Expand bites.Opt[bool]
		Page   bites.PositiveInt
	}
	Body struct {
		Name  bites.NonEmpty
		Email email
		Age   bites.PositiveInt
	}
}

func (validPostIn) Validate() error { return nil }

type validGetIn struct {
	Path struct{ ID bites.PositiveInt64 }
}

func (validGetIn) Validate() error { return nil }

func main() {
	flag.Parse()
	common.StartStats()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	opts := []bites.Option{bites.WithLogger(quiet)}
	if v := os.Getenv("BITES_VARIANT"); v == "mw4" || v == "log" {
		// AccessLog is an Observer (ADR 0051), given at New rather than with Use.
		opts = append(opts, bites.WithObserver(bites.AccessLog(bites.AccessLogOptions{Logger: quiet})))
	}
	app := bites.New(opts...)
	mw := routeMiddleware()
	switch os.Getenv("BITES_VARIANT") {
	case "prov":
		app.Provide(sessionOf)
		app.Provide(callerOf)
		app.Post("/users/{id}", func(_ context.Context, in provPostIn) (common.Created, error) {
			return common.Created{ID: in.Path.ID, Name: in.Body.Name, Email: in.Body.Email, Age: in.Body.Age, Page: in.Query.Page}, nil
		}, mw...)
		app.Get("/users/{id}", func(_ context.Context, in provGetIn) (common.User, error) {
			return common.User{ID: in.Path.ID, Name: "ada", Email: "ada@example.dev", Age: 36}, nil
		}, mw...)
	case "valid":
		app.Post("/users/{id}", func(_ context.Context, in validPostIn) (common.Created, error) {
			return common.Created{ID: int64(in.Path.ID), Name: string(in.Body.Name), Email: string(in.Body.Email), Age: int(in.Body.Age), Page: int(in.Query.Page)}, nil
		}, mw...)
		app.Get("/users/{id}", func(_ context.Context, in validGetIn) (common.User, error) {
			return common.User{ID: int64(in.Path.ID), Name: "ada", Email: "ada@example.dev", Age: 36}, nil
		}, mw...)
	case "mw4", "cors", "rid", "log", "timeout":
		v := os.Getenv("BITES_VARIANT")
		if v == "mw4" || v == "cors" {
			app.Use(bites.CORS(bites.CORSOptions{Origins: []string{"https://app.example.com"}}))
		}
		if v == "mw4" || v == "rid" {
			app.Use(bites.RequestID())
		}
		if v == "mw4" || v == "timeout" {
			mw = append(mw, bites.Use(bites.Timeout(5*time.Second)))
		}
		fallthrough
	default:
		app.Post("/users/{id}", func(_ context.Context, in postIn) (common.Created, error) {
			return common.Created{ID: in.Path.ID, Name: in.Body.Name, Email: in.Body.Email, Age: in.Body.Age, Page: in.Query.Page}, nil
		}, mw...)
		app.Get("/users/{id}", func(_ context.Context, in getIn) (common.User, error) {
			return common.User{ID: in.Path.ID, Name: "ada", Email: "ada@example.dev", Age: 36}, nil
		}, mw...)
	}
	// GET /block: a handler that waits 10 ms (the block experiment).
	app.Get("/block", func(_ context.Context, _ struct{}) (common.Blocked, error) {
		time.Sleep(common.BlockDelay)
		return common.Blocked{OK: true}, nil
	})
	if *useTLS {
		if err := app.ListenTLS(*common.Addr, selfSigned()); err != nil {
			panic(err)
		}
		return
	}
	if err := app.Listen(*common.Addr); err != nil {
		panic(err)
	}
}
