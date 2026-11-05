package bites_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

func TestAStatusErrorWithAHeaderCarriesItOnItsProblemAnswer(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/slow", func(context.Context, Empty) (struct{}, error) {
			return struct{}{}, bites.TooManyRequests("slow down").WithHeader("Retry-After", "7").WithHeader("RateLimit", `"default";r=0;t=7`)
		})
	})
	r := srv.Do(req("GET", "/slow"))
	wantProblem(t, r, 429, `{"title":"Too Many Requests","status":429,"detail":"slow down"}`)
	if r.Get("retry-after") != "7" || r.Get("ratelimit") != `"default";r=0;t=7` {
		t.Fatalf("%v", r.Header)
	}
}

func TestWithHeaderLeavesTheOriginalErrorUnchanged(t *testing.T) {
	base := bites.Unauthorized("no")
	a := base.WithHeader("WWW-Authenticate", "Bearer")
	b := base.WithHeader("X-Other", "1")
	srv := start(t, func(app *bites.App) {
		app.Get("/base", func(context.Context, Empty) (struct{}, error) { return struct{}{}, base })
		app.Get("/a", func(context.Context, Empty) (struct{}, error) { return struct{}{}, a })
		app.Get("/b", func(context.Context, Empty) (struct{}, error) { return struct{}{}, b })
	})
	if r := srv.Do(req("GET", "/base")); r.Get("www-authenticate") != "" || r.Get("x-other") != "" {
		t.Fatalf("base: %v", r.Header)
	}
	if r := srv.Do(req("GET", "/a")); r.Get("www-authenticate") != "Bearer" || r.Get("x-other") != "" {
		t.Fatalf("a: %v", r.Header)
	}
	if r := srv.Do(req("GET", "/b")); r.Get("www-authenticate") != "" || r.Get("x-other") != "1" {
		t.Fatalf("b: %v", r.Header)
	}
}

func TestAStatusErrorHeaderTheEngineOwnsOrThatIsNotATokenIsLeftOutAndLogged(t *testing.T) {
	srv, lb := startLogged(t, func(app *bites.App) {
		app.Get("/x", func(context.Context, Empty) (struct{}, error) {
			return struct{}{}, bites.Conflict("taken").WithHeader("Content-Type", "text/html").WithHeader("Bad Name", "v").WithHeader("X-Split", "a\r\nSet-Cookie: x=1")
		})
	})
	r := srv.Do(req("GET", "/x"))
	if r.Status != 409 || r.Get("content-type") != "application/problem+json" || r.Get("set-cookie") != "" || r.Get("x-split") != "a  Set-Cookie: x=1" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if !strings.Contains(lb.String(), "Content-Type") || !strings.Contains(lb.String(), `"Bad Name"`) {
		t.Fatalf("the refused headers were not logged:\n%s", lb.String())
	}
}

// missingError is an application's own error type, which says what it is on the wire (ADR 0049).
type missingError struct{ what string }

func (e *missingError) Error() string             { return "repo: " + e.what + " not found in table t_" + e.what }
func (e *missingError) HTTPStatus() (int, string) { return 404, e.what + " does not exist" }

func TestAnErrorTypeThatSaysItsStatusIsAnsweredWithItAndNotWithItsOwnText(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/a", func(context.Context, Empty) (struct{}, error) { return struct{}{}, &missingError{"deal"} })
		app.Get("/b", func(context.Context, Empty) (struct{}, error) {
			return struct{}{}, fmt.Errorf("load deal: %w", fmt.Errorf("again: %w", &missingError{"deal"}))
		})
		app.Get("/c", func(context.Context, Empty) (struct{}, error) {
			return struct{}{}, errors.Join(errors.New("first"), fmt.Errorf("wrapped: %w", &missingError{"deal"}))
		})
	})
	for _, p := range []string{"/a", "/b", "/c"} {
		wantProblem(t, srv.Do(req("GET", p)), 404, `{"title":"Not Found","status":404,"detail":"deal does not exist"}`)
	}
}

func TestAnErrorChainWithNoStatusAtAllIsStillA500WithNoMessage(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/x", func(context.Context, Empty) (struct{}, error) {
			return struct{}{}, fmt.Errorf("load: %w", errors.New("connection refused"))
		})
	})
	wantProblem(t, srv.Do(req("GET", "/x")), 500, `{"title":"Internal Server Error","status":500}`)
}

func TestTheNearerOfAStatusErrorAndAnErrorTypeThatSaysItsStatusDecides(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		// The status error is outside the application's error: it was made on purpose, last.
		app.Get("/outer", func(context.Context, Empty) (struct{}, error) {
			return struct{}{}, fmt.Errorf("%w (cause: %w)", bites.Conflict("someone else did"), &missingError{"deal"})
		})
		// The application's error wraps a status error: the outer error is nearer.
		app.Get("/inner", func(context.Context, Empty) (struct{}, error) {
			return struct{}{}, &wrapsStatus{bites.Conflict("someone else did")}
		})
	})
	wantProblem(t, srv.Do(req("GET", "/outer")), 409, `{"title":"Conflict","status":409,"detail":"someone else did"}`)
	wantProblem(t, srv.Do(req("GET", "/inner")), 422, `{"title":"Unprocessable Content","status":422,"detail":"cannot be done"}`)
}

type wrapsStatus struct{ err error }

func (e *wrapsStatus) Error() string             { return "wraps: " + e.err.Error() }
func (e *wrapsStatus) Unwrap() error             { return e.err }
func (e *wrapsStatus) HTTPStatus() (int, string) { return 422, "cannot be done" }

type badStatus struct{}

func (badStatus) Error() string             { return "bad" }
func (badStatus) HTTPStatus() (int, string) { return 200, "fine" }

func TestAnErrorTypeWhoseStatusIsNotAnErrorStatusIsA500AndLogged(t *testing.T) {
	srv, lb := startLogged(t, func(app *bites.App) {
		app.Get("/x", func(context.Context, Empty) (struct{}, error) { return struct{}{}, badStatus{} })
	})
	wantProblem(t, srv.Do(req("GET", "/x")), 500, `{"title":"Internal Server Error","status":500}`)
	if !strings.Contains(lb.String(), "not an error status") {
		t.Fatalf("not logged:\n%s", lb.String())
	}
}

// dbError stands in for a driver's error type (*pgconn.PgError): a type the application does not
// own, so it cannot give it an HTTPStatus method (ADR 0049).
type dbError struct{ Code string }

func (e *dbError) Error() string { return "db: SQLSTATE " + e.Code + " in table secret_orders" }

var errNoRows = errors.New("db: no rows in result set")

// dbMapper is the application's mapping of the stand-in driver's errors.
func dbMapper(err error) *bites.StatusError {
	var db *dbError
	switch {
	case errors.Is(err, errNoRows):
		return bites.NotFound("no such record")
	case errors.As(err, &db) && db.Code == "23505":
		return bites.Conflict("already exists").With("code", "duplicate")
	case errors.As(err, &db) && db.Code == "23503":
		return bites.UnprocessableEntity("refers to a record that does not exist")
	}
	return nil
}

// startMapped is start with options.
func startMapped(t *testing.T, register func(app *bites.App), opts ...bites.Option) *bitetest.Server {
	t.Helper()
	srv, _ := startLogged(t, register, opts...)
	return srv
}

func failWith(err error) func(context.Context, Empty) (struct{}, error) {
	return func(context.Context, Empty) (struct{}, error) { return struct{}{}, err }
}

func TestAForeignErrorWrappedWithPercentWIsAnsweredByTheErrorMapper(t *testing.T) {
	srv, lb := startLogged(t, func(app *bites.App) {
		app.Get("/dup", failWith(fmt.Errorf("insert order: %w", &dbError{Code: "23505"})))
		app.Get("/fk", failWith(fmt.Errorf("insert line: %w", fmt.Errorf("tx: %w", &dbError{Code: "23503"}))))
		app.Get("/none", failWith(fmt.Errorf("load order: %w", errNoRows)))
		app.Get("/joined", failWith(errors.Join(errors.New("closing rows"), fmt.Errorf("load: %w", errNoRows))))
		app.Get("/other", failWith(fmt.Errorf("insert: %w", &dbError{Code: "XX000"})))
	}, bites.WithErrorMapper(dbMapper))
	wantProblem(t, srv.Do(req("GET", "/dup")), 409, `{"title":"Conflict","status":409,"detail":"already exists","code":"duplicate"}`)
	wantProblem(t, srv.Do(req("GET", "/fk")), 422, `{"title":"Unprocessable Content","status":422,"detail":"refers to a record that does not exist"}`)
	wantProblem(t, srv.Do(req("GET", "/none")), 404, `{"title":"Not Found","status":404,"detail":"no such record"}`)
	wantProblem(t, srv.Do(req("GET", "/joined")), 404, `{"title":"Not Found","status":404,"detail":"no such record"}`)
	// An error the mapper does not know is the 500 it always was, and says nothing.
	wantProblem(t, srv.Do(req("GET", "/other")), 500, `{"title":"Internal Server Error","status":500}`)
	if log := lb.String(); strings.Contains(log, "SQLSTATE 23505") || strings.Contains(log, "SQLSTATE 23503") || strings.Contains(log, "no rows") {
		t.Fatalf("a mapped 4xx was logged as an error:\n%s", log)
	}
	if log := lb.String(); !strings.Contains(log, "SQLSTATE XX000") {
		t.Fatalf("the unmapped error was not logged:\n%s", log)
	}
}

func TestAnErrorWithAStatusInItsChainIsNeverAskedOfTheErrorMapper(t *testing.T) {
	asked := 0
	srv := startMapped(t, func(app *bites.App) {
		// The application's own answer wraps the foreign error, and the other way round.
		app.Get("/a", failWith(bites.Forbidden("not yours").With("code", "own")))
		app.Get("/b", failWith(fmt.Errorf("x: %w", bites.NotFound("no such order"))))
		app.Get("/c", failWith(fmt.Errorf("x: %w", &missingError{"deal"})))
		app.Get("/d", failWith(fmt.Errorf("%w: %w", &dbError{Code: "23505"}, bites.BadRequest("bad"))))
	}, bites.WithErrorMapper(func(err error) *bites.StatusError {
		asked++
		return bites.Conflict("mapped")
	}))
	wantProblem(t, srv.Do(req("GET", "/a")), 403, `{"title":"Forbidden","status":403,"detail":"not yours","code":"own"}`)
	wantProblem(t, srv.Do(req("GET", "/b")), 404, `{"title":"Not Found","status":404,"detail":"no such order"}`)
	wantProblem(t, srv.Do(req("GET", "/c")), 404, `{"title":"Not Found","status":404,"detail":"deal does not exist"}`)
	wantProblem(t, srv.Do(req("GET", "/d")), 400, `{"title":"Bad Request","status":400,"detail":"bad"}`)
	if asked != 0 {
		t.Fatalf("the mapper was asked %d times for errors that already said their status", asked)
	}
}

func TestAProviderAndAMiddlewareErrorAreAnsweredByTheErrorMapper(t *testing.T) {
	srv := startMapped(t, func(app *bites.App) {
		app.Get("/mw", func(context.Context, Empty) (struct{}, error) { return struct{}{}, nil },
			bites.Use(func(context.Context, bites.Request, bites.Next) (bites.Response, error) {
				return bites.Response{}, fmt.Errorf("lookup: %w", errNoRows)
			}))
		app.Get("/prov", func(context.Context, struct{ Who Session }) (struct{}, error) { return struct{}{}, nil },
			bites.Provide(func(context.Context, bites.Request, struct{}) (Session, error) {
				return Session{}, fmt.Errorf("session: %w", &dbError{Code: "23505"})
			}))
	}, bites.WithErrorMapper(dbMapper))
	wantProblem(t, srv.Do(req("GET", "/mw")), 404, `{"title":"Not Found","status":404,"detail":"no such record"}`)
	wantProblem(t, srv.Do(req("GET", "/prov")), 409, `{"title":"Conflict","status":409,"detail":"already exists","code":"duplicate"}`)
}

func TestErrorMappersGivenMoreThanOnceAreAskedInOrderAndTheFirstAnswerWins(t *testing.T) {
	var order []string
	first := func(err error) *bites.StatusError {
		order = append(order, "first")
		if errors.Is(err, errNoRows) {
			return bites.NotFound("from the first")
		}
		return nil
	}
	second := func(err error) *bites.StatusError {
		order = append(order, "second")
		return bites.Conflict("from the second")
	}
	srv := startMapped(t, func(app *bites.App) {
		app.Get("/a", failWith(errNoRows))
		app.Get("/b", failWith(errors.New("anything")))
	}, bites.WithErrorMapper(first), bites.WithErrorMapper(second))
	wantProblem(t, srv.Do(req("GET", "/a")), 404, `{"title":"Not Found","status":404,"detail":"from the first"}`)
	wantProblem(t, srv.Do(req("GET", "/b")), 409, `{"title":"Conflict","status":409,"detail":"from the second"}`)
	if got := strings.Join(order, ","); got != "first,first,second" {
		t.Fatalf("asked %s, want first,first,second", got)
	}
}

func TestAMappedFiveHundredSendsTheMappersMessageAndIsLoggedWithTheRealError(t *testing.T) {
	srv, lb := startLogged(t, func(app *bites.App) {
		app.Get("/x", failWith(fmt.Errorf("pool: %w", &dbError{Code: "57P03"})))
		app.Get("/bare", failWith(&dbError{Code: "57P03"}))
	}, bites.WithErrorMapper(func(err error) *bites.StatusError {
		var db *dbError
		if errors.As(err, &db) && db.Code == "57P03" {
			if strings.Contains(err.Error(), "pool") {
				return bites.ServiceUnavailable("the database is starting, try again").WithHeader("Retry-After", "5")
			}
			return bites.NewError(503, "")
		}
		return nil
	}))
	r := srv.Do(req("GET", "/x"))
	wantProblem(t, r, 503, `{"title":"Service Unavailable","status":503,"detail":"the database is starting, try again"}`)
	if r.Get("retry-after") != "5" {
		t.Fatalf("%v", r.Header)
	}
	// An empty message is a title and a status only.
	wantProblem(t, srv.Do(req("GET", "/bare")), 503, `{"title":"Service Unavailable","status":503}`)
	if log := lb.String(); !strings.Contains(log, "SQLSTATE 57P03") || !strings.Contains(log, "status=503") || !strings.Contains(log, "level=ERROR") {
		t.Fatalf("a mapped 5xx was not logged with the original error:\n%s", log)
	}
}

func TestAnErrorMapperThatAnswersWithANonErrorStatusIsALoggedFiveHundred(t *testing.T) {
	for _, status := range []int{0, 200, 302, 399, 600} {
		srv, lb := startLogged(t, func(app *bites.App) { app.Get("/x", failWith(errNoRows)) },
			bites.WithErrorMapper(func(error) *bites.StatusError { return bites.NewError(status, "no") }))
		wantProblem(t, srv.Do(req("GET", "/x")), 500, `{"title":"Internal Server Error","status":500}`)
		if !strings.Contains(lb.String(), "not an error status") || !strings.Contains(lb.String(), "no rows") {
			t.Fatalf("status %d: not logged with the error:\n%s", status, lb.String())
		}
	}
}

func TestAnErrorMapperThatPanicsIsRecoveredAndTheErrorIsAnsweredAsUnmapped(t *testing.T) {
	calls := 0
	srv, lb := startLogged(t, func(app *bites.App) { app.Get("/x", failWith(errNoRows)) },
		bites.WithErrorMapper(func(error) *bites.StatusError {
			calls++
			panic("mapper bug")
		}), bites.WithErrorMapper(func(error) *bites.StatusError { calls += 100; return bites.Conflict("never") }))
	for range 2 {
		wantProblem(t, srv.Do(req("GET", "/x")), 500, `{"title":"Internal Server Error","status":500}`)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2: a mapper after the one that panicked must not be asked", calls)
	}
	if log := lb.String(); !strings.Contains(log, "mapper bug") || !strings.Contains(log, "no rows") {
		t.Fatalf("the panic and the original error were not both logged:\n%s", log)
	}
}

func TestAnErrorMapperIsNeverAskedOnASuccess(t *testing.T) {
	// A mapper is asked only when there is an error to answer. The other case where it must not be
	// asked, a client that has left, is held by answerError's early return coming before mapError
	// (ADR 0049: nobody is waiting, so nothing is answered, mapped or not); no test reaches it.
	asked := 0
	srv := startMapped(t, func(app *bites.App) {
		app.Get("/ok", func(context.Context, Empty) (struct{}, error) { return struct{}{}, nil })
	},
		bites.WithErrorMapper(func(error) *bites.StatusError { asked++; return nil }))
	if r := srv.Do(req("GET", "/ok")); r.Status != 200 || asked != 0 {
		t.Fatalf("%d, asked %d times on a success", r.Status, asked)
	}
}

func TestPreconditionFailedIsA412ProblemWithItsMessageAsDetail(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Put("/rooms/{id}", func(context.Context, struct{ Path struct{ ID int64 } }) (bites.NoContent, error) {
			return bites.NoContent{}, bites.PreconditionFailed("the room changed: read it again")
		})
	})
	wantProblem(t, srv.Do(req("PUT", "/rooms/7")), 412,
		`{"title":"Precondition Failed","status":412,"detail":"the room changed: read it again"}`)
}

func TestPreconditionRequiredIsA428AndAnyRegisteredStatusIsTitledByItsReasonPhrase(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Put("/a", func(context.Context, struct{}) (bites.NoContent, error) {
			return bites.NoContent{}, bites.PreconditionRequired("send If-Match")
		})
		app.Put("/b", func(context.Context, struct{}) (bites.NoContent, error) {
			return bites.NoContent{}, bites.NewError(423, "locked by ada")
		})
		app.Put("/c", func(context.Context, struct{}) (bites.NoContent, error) {
			return bites.NoContent{}, bites.NewError(499, "odd")
		})
	})
	wantProblem(t, srv.Do(req("PUT", "/a")), 428, `{"title":"Precondition Required","status":428,"detail":"send If-Match"}`)
	wantProblem(t, srv.Do(req("PUT", "/b")), 423, `{"title":"Locked","status":423,"detail":"locked by ada"}`)
	wantProblem(t, srv.Do(req("PUT", "/c")), 499, `{"title":"Status 499","status":499,"detail":"odd"}`)
}
