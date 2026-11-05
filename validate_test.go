package bites_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// ---- Validate (ADR 0021) ----

type Booking struct {
	Body struct {
		Start time.Time
		End   time.Time
		Name  string
	}
}

func (in Booking) Validate() error {
	var errs []error
	if !in.Body.End.After(in.Body.Start) {
		errs = append(errs, bites.Invalid("body.end", "must be after body.start"))
	}
	if in.Body.Name == "root" {
		errs = append(errs, bites.Invalid("body.name", "is reserved"))
	}
	return errors.Join(errs...)
}

func TestValidateRunsAfterDecodingAndItsErrorsAreTheSame400WithWireNames(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[Booking]) })
	r := srv.Do(jsonReq("POST", "/b", `{"start":"2026-01-02T00:00:00Z","end":"2026-01-01T00:00:00Z","name":"root"}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"body.end","message":"must be after body.start"},`+
		`{"location":"body.name","message":"is reserved"}]}`)
	r = srv.Do(jsonReq("POST", "/b", `{"start":"2026-01-01T00:00:00Z","end":"2026-01-02T00:00:00Z","name":"ada"}`))
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestValidateDoesNotRunWhenDecodingFailedSoItNeverSeesAHalfFilledInput(t *testing.T) {
	ran := false
	srv := start(t, func(app *bites.App) {
		app.Post("/b", func(_ context.Context, in spyValidated) (struct{}, error) { return struct{}{}, nil })
	})
	spyRan = &ran
	r := srv.Do(jsonReq("POST", "/b", `{"n":"x"}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.n","message":"expected an integer","rule":"type"}]}`)
	if ran {
		t.Fatal("Validate ran on an Input whose decoding failed")
	}
	if r := srv.Do(jsonReq("POST", "/b", `{"n":1}`)); r.Status != 200 || !ran {
		t.Fatalf("%d, ran=%v", r.Status, ran)
	}
}

var spyRan *bool

type spyValidated struct{ Body struct{ N int } }

func (spyValidated) Validate() error { *spyRan = true; return nil }

type CheckQuery struct {
	Query struct{ From, To int }
}

func (in CheckQuery) Validate() error {
	if in.Query.To < in.Query.From {
		return bites.Invalid("query.to", "must not be before query.from")
	}
	return nil
}

func TestValidateReportsALocationInAnySectionAndAWrappedFieldErrorIsFoundToo(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/q", echo[CheckQuery]) })
	wantProblem(t, srv.Do(req("GET", "/q?from=5&to=1")), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"query.to","message":"must not be before query.from"}]}`)
	srv = start(t, func(app *bites.App) {
		app.Get("/w", func(_ context.Context, in wrapsInvalid) (struct{}, error) { return struct{}{}, nil })
	})
	wantProblem(t, srv.Do(req("GET", "/w")), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"query.x","message":"bad"}]}`)
}

type wrapsInvalid struct{ Query struct{ X bites.Opt[int] } }

func (wrapsInvalid) Validate() error {
	return fmt.Errorf("checking: %w", bites.Invalid("query.x", "bad"))
}

type manyErrors struct{}

func (manyErrors) Validate() error {
	var errs []error
	for i := range 30 {
		errs = append(errs, bites.Invalid(fmt.Sprintf("body.items[%d]", i), "no"))
	}
	return errors.Join(errs...)
}

func TestValidateErrorsStopAtTwentyLikeDecodingErrorsDo(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/m", func(_ context.Context, in manyErrors) (struct{}, error) { return struct{}{}, nil })
	})
	r := srv.Do(req("GET", "/m"))
	if r.Status != 400 || strings.Count(r.Text(), `"location"`) != 20 || !strings.Contains(r.Text(), `"truncated":true`) {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

type validateStatus struct{}

func (validateStatus) Validate() error { return bites.Conflict("the slot is taken") }

type validatePlain struct{}

func (validatePlain) Validate() error { return errors.New("db is down") }

func TestAValidateErrorWithNoLocationIsAnsweredLikeAHandlerErrorWouldBe(t *testing.T) {
	srv, logs := startLogged(t, func(app *bites.App) {
		app.Get("/s", func(_ context.Context, in validateStatus) (struct{}, error) { return struct{}{}, nil })
		app.Get("/p", func(_ context.Context, in validatePlain) (struct{}, error) { return struct{}{}, nil })
	})
	wantProblem(t, srv.Do(req("GET", "/s")), 409, `{"title":"Conflict","status":409,"detail":"the slot is taken"}`)
	wantProblem(t, srv.Do(req("GET", "/p")), 500, `{"title":"Internal Server Error","status":500}`)
	if !strings.Contains(logs.String(), "db is down") {
		t.Fatalf("log: %s", logs)
	}
}

// ---- a located error from a handler or a Provider (ADR 0012, ADR 0049) ----

type orderItems struct {
	Body struct{ Items []struct{ ProductID int64 } }
}

func TestAHandlerThatReturnsInvalidAnswersThe400ThatNamesTheFieldNotA500(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Post("/orders", func(_ context.Context, in orderItems) (struct{}, error) {
			var errs []error
			for i, it := range in.Body.Items {
				if it.ProductID == 42 {
					errs = append(errs, bites.Invalid(fmt.Sprintf("body.items[%d].product_id", i), "names no product"))
				}
			}
			return struct{}{}, fmt.Errorf("checking the items: %w", errors.Join(errs...))
		})
	})
	wantProblem(t, srv.Do(jsonReq("POST", "/orders", `{"items":[{"product_id":1},{"product_id":42}]}`)), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"body.items[1].product_id","message":"names no product"}]}`)
}

func TestALocatedErrorJoinedToAStatusErrorIsStillThe400(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/j", func(context.Context, struct{}) (struct{}, error) {
			return struct{}{}, errors.Join(bites.Conflict("taken"), bites.Invalid("query.slot", "is taken"))
		})
	})
	wantProblem(t, srv.Do(req("GET", "/j")), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"query.slot","message":"is taken"}]}`)
}

type tenant struct{ ID string }

func TestAProviderThatReturnsInvalidAnswersThe400BeforeTheHandlerRuns(t *testing.T) {
	ran := false
	srv := start(t, func(app *bites.App) {
		app.Provide(func(context.Context, bites.Request, struct{}) (tenant, error) {
			return tenant{}, bites.Invalid("header.X-Tenant", "names no tenant")
		})
		app.Get("/t", func(_ context.Context, in struct{ T tenant }) (struct{}, error) {
			ran = true
			return struct{}{}, nil
		})
	})
	wantProblem(t, srv.Do(req("GET", "/t")), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"header.X-Tenant","message":"names no tenant"}]}`)
	if ran {
		t.Fatal("the handler ran after its Provider failed")
	}
}

// ---- constraint types and Length (ADR 0021) ----

type nameRange string

func (nameRange) Length() (int, int) { return 2, 5 }

type atMost3 string

func (atMost3) Length() (int, int) { return 0, 3 }

type exactly4 string

func (exactly4) Length() (int, int) { return 4, 4 }

type Constrained struct {
	Path  struct{ ID bites.PositiveInt64 }
	Query struct {
		Q     bites.NonEmpty
		Limit bites.Opt[bites.PositiveInt]
		Tags  []bites.NonEmpty
	}
	Header struct{ Token nameRange }
	Cookie struct{ Session bites.NonEmpty }
}

func TestConstraintTypesAreCheckedInPathQueryHeaderAndCookieWithWireNames(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/c/{id}", echo[Constrained]) })
	ok := bitesReq("/c/7?q=a&limit=3&tags=x&tags=y", "abc", "s")
	if r := srv.Do(ok); r.Status != 200 || !strings.Contains(r.Text(), `"id":7`) || !strings.Contains(r.Text(), `"token":"abc"`) {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r := srv.Do(bitesReq("/c/0?q=&limit=0&tags=x&tags=&tags=z", "a", ""))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"path.id","message":"expected a positive integer","rule":"positive"},`+
		`{"location":"query.q","message":"must not be empty","rule":"nonempty"},`+
		`{"location":"query.limit","message":"expected a positive integer","rule":"positive"},`+
		`{"location":"query.tags[1]","message":"must not be empty","rule":"nonempty"},`+
		`{"location":"header.Token","message":"must be between 2 and 5 characters","rule":"length","min":2,"max":5},`+
		`{"location":"cookie.session","message":"must not be empty","rule":"nonempty"}]}`)
	// A value that is not a number keeps the message of the base type.
	r = srv.Do(bitesReq("/c/x?q=a", "abc", "s"))
	if !strings.Contains(r.Text(), `{"location":"path.id","message":"expected an integer","rule":"type"}`) {
		t.Fatalf("%s", r.Text())
	}
}

func bitesReq(target, token, session string) (r bitetest.Request) {
	r = req("GET", target)
	r.Header = map[string][]string{"Token": {token}, "Cookie": {"session=" + session}}
	return r
}

type Signup struct {
	Body struct {
		Name  bites.NonEmpty
		Nick  atMost3
		Code  exactly4
		Seats bites.Opt[bites.PositiveInt]
		Opt   bites.Opt[nameRange]
	}
}

func TestConstraintTypesAreCheckedInAJSONBodyAndReportedWithTheOtherErrors(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/s", echo[Signup]) })
	r := srv.Do(jsonReq("POST", "/s", `{"name":"Ada","nick":"abc","code":"1234","seats":2,"opt":"ab"}`))
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r = srv.Do(jsonReq("POST", "/s", `{"name":"","nick":"abcd","code":"123","seats":-1,"opt":"a"}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"body.name","message":"must not be empty","rule":"nonempty"},`+
		`{"location":"body.nick","message":"must be at most 3 characters","rule":"length","min":0,"max":3},`+
		`{"location":"body.code","message":"must be exactly 4 characters","rule":"length","min":4,"max":4},`+
		`{"location":"body.seats","message":"expected a positive integer","rule":"positive"},`+

		`{"location":"body.opt","message":"must be between 2 and 5 characters","rule":"length","min":2,"max":5}]}`)
}

func TestALengthIsCountedInCharactersNotBytes(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/s", echo[Signup]) })
	// "日本語" is 3 characters and 9 bytes; Nick allows 3.
	r := srv.Do(jsonReq("POST", "/s", `{"name":"x","nick":"日本語","code":"日本語字"}`))
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r = srv.Do(jsonReq("POST", "/s", `{"name":"x","nick":"日本語字","code":"日本語字"}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.nick","message":"must be at most 3 characters","rule":"length","min":0,"max":3}]}`)
}

func TestAConstraintFailureNeverEchoesTheClientsValue(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/s", echo[Signup]) })
	long := strings.Repeat("secret", 10000)
	r := srv.Do(jsonReq("POST", "/s", `{"name":"x","nick":"`+long+`","code":"1234"}`))
	if r.Status != 400 || strings.Contains(r.Text(), "secret") || len(r.Text()) > 300 {
		t.Fatalf("%d %d bytes", r.Status, len(r.Text()))
	}
}

type PalList struct {
	Body struct{ Pals []bites.NonEmpty }
}

func TestAListOfAConstraintTypeNamesTheElementThatFails(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/p", echo[PalList]) })
	if r := srv.Do(jsonReq("POST", "/p", `{"pals":["x","y"]}`)); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	wantProblem(t, srv.Do(jsonReq("POST", "/p", `{"pals":["x",""]}`)), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"body.pals[1]","message":"must not be empty","rule":"nonempty"}]}`)
}

type mixedValidated struct {
	Body struct{ Name bites.NonEmpty }
}

func (in mixedValidated) Validate() error { return bites.Invalid("body.name", "taken") }

func TestAConstraintFailureStopsTheRequestBeforeValidateRuns(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Post("/m", func(_ context.Context, in mixedValidated) (struct{}, error) { return struct{}{}, nil })
	})
	wantProblem(t, srv.Do(jsonReq("POST", "/m", `{"name":""}`)), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"body.name","message":"must not be empty","rule":"nonempty"}]}`)
	wantProblem(t, srv.Do(jsonReq("POST", "/m", `{"name":"x"}`)), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"body.name","message":"taken"}]}`)
}

// ---- Rules carried by an application's own named types ----

type RoomName string

func (RoomName) Length() (min, max int) { return 3, 8 }

type Guests int

func (Guests) Range() (min, max int64) { return 1, 6 }

type Contact string

var errNotEmail = errors.New("must be an email address")

func (c Contact) Check() error {
	if i := strings.IndexByte(string(c), '@'); i < 1 || i == len(c)-1 {
		return errNotEmail
	}
	return nil
}

type OwnRules struct {
	Query struct {
		Guests Guests
		Cc     bites.Opt[Contact]
	}
	Body struct {
		Room    RoomName
		Contact Contact
		Others  []Contact
	}
}

func TestANamedTypesLengthRangeAndCheckAreDecodingRulesAt400WithTheirLocations(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[OwnRules]) })
	r := srv.Do(jsonReq("POST", "/b?guests=9&cc=nobody", `{"room":"ab","contact":"x","others":["a@b","c"]}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"query.guests","message":"expected an integer between 1 and 6","rule":"range","min":1,"max":6},`+
		`{"location":"query.cc","message":"must be an email address","rule":"check"},`+
		`{"location":"body.room","message":"must be between 3 and 8 characters","rule":"length","min":3,"max":8},`+
		`{"location":"body.contact","message":"must be an email address","rule":"check"},`+
		`{"location":"body.others[1]","message":"must be an email address","rule":"check"}]}`)
	r = srv.Do(jsonReq("POST", "/b?guests=2", `{"room":"blue","contact":"a@b","others":[]}`))
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestACheckThatFailsInABodyTheFastPathWouldTakeIsStillA400(t *testing.T) {
	// A flat body of plain strings is decoded without the tokenizer; a failed Check there hands
	// it back, so the answer is the same as for any other body.
	type flat struct {
		Body struct{ Contact Contact }
	}
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[flat]) })
	wantProblem(t, srv.Do(jsonReq("POST", "/b", `{"contact":"x"}`)), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"body.contact","message":"must be an email address","rule":"check"}]}`)
	if r := srv.Do(jsonReq("POST", "/b", `{"contact":"a@b"}`)); r.Status != 200 || r.Text() != `{"body":{"contact":"a@b"}}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestTheAPIDocumentStatesALengthAndARangeAsTheirBounds(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Post("/b", echo[OwnRules])
	})
	doc := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()
	for _, want := range []string{`"maximum":6`, `"minimum":1`, `"maxLength":8`, `"minLength":3`} {
		if !strings.Contains(doc, want) {
			t.Errorf("the document lacks %s", want)
		}
	}
}

// ---- Items, Format and uuid.UUID (ADR 0021) ----

type Labels []string

func (Labels) Items() (min, max int) { return 1, 3 }

type Cap2 []int

func (Cap2) Items() (min, max int) { return 0, 2 }

type Pair []int

func (Pair) Items() (min, max int) { return 2, 2 }

type Mail string

func (Mail) Format() string { return "email" }

func (m Mail) Check() error {
	if strings.IndexByte(string(m), '@') < 1 {
		return errNotEmail
	}
	return nil
}

type ListRules struct {
	Query struct {
		Tags Labels
		Ids  bites.Opt[Pair]
	}
	Body struct {
		Labels Labels
		Cap    Cap2
		Pair   Pair
		Mail   Mail
		Ref    uuid.UUID
	}
}

func TestAListWhoseItemsBoundFailsIsA400AtTheListWithTheItemsRuleAndBounds(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[ListRules]) })
	r := srv.Do(jsonReq("POST", "/b?tags=a&tags=b&tags=c&tags=d&ids=1", `{"labels":[],"cap":[1,2,3,4,5],"pair":[1,2],"mail":"a@b.co","ref":"x"}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"query.tags","message":"must have between 1 and 3 items","rule":"items","min":1,"max":3},`+
		`{"location":"query.ids","message":"must have exactly 2 items","rule":"items","min":2,"max":2},`+
		`{"location":"body.labels","message":"must have between 1 and 3 items","rule":"items","min":1,"max":3},`+
		`{"location":"body.cap","message":"must have at most 2 items","rule":"items","min":0,"max":2},`+
		`{"location":"body.ref","message":"expected a UUID","rule":"type"}]}`)
	r = srv.Do(jsonReq("POST", "/b?tags=a&ids=1&ids=2", `{"labels":["x"],"cap":[],"pair":[1,2],"mail":"a@b.co","ref":"123e4567-e89b-12d3-a456-426614174000"}`))
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAListLongerThanItsItemsBoundIsStoppedWithoutStoringTheRest(t *testing.T) {
	type in struct{ Body Cap2 }
	srv := start(t, func(app *bites.App) {
		app.Post("/b", func(_ context.Context, in in) (struct{ N int }, error) { return struct{ N int }{len(in.Body)}, nil })
	})
	wantProblem(t, srv.Do(jsonReq("POST", "/b", `[1,2,3,`+strings.Repeat("4,", 1000)+`5]`)), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"body","message":"must have at most 2 items","rule":"items","min":0,"max":2}]}`)
}

func TestTheAPIDocumentStatesItemsFormatAndAUUIDAsMinItemsMaxItemsAndFormat(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Post("/b", echo[ListRules])
	})
	doc := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()
	for _, want := range []string{`"minItems":1`, `"maxItems":3`, `"maxItems":2`, `"format":"email"`, `"format":"uuid"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("the document lacks %s", want)
		}
	}
}

// ---- Pattern (ADR 0021) ----

type Slug string

func (Slug) Pattern() string { return `^[a-z0-9-]+$` }

type Sku string

func (Sku) Length() (min, max int) { return 4, 6 }
func (Sku) Pattern() string        { return `^[A-Z]+\d+$` }

type HasDigit string

func (HasDigit) Pattern() string { return `\d` }

type PatternRules struct {
	Query struct {
		Slug Slug
		Tags []Slug
	}
	Body struct {
		Slug  Slug
		Sku   Sku
		Digit HasDigit
		Maybe bites.Opt[Slug]
	}
}

func TestAValueThatDoesNotMatchItsPatternIsA400AtItsLocationWithTheRulePatternAndNoBounds(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[PatternRules]) })
	r := srv.Do(jsonReq("POST", "/b?slug=Bad%20Slug&tags=ok&tags=NOT", `{"slug":"No Way","sku":"AB1","digit":"x","maybe":"UP"}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"query.slug","message":"must match the pattern","rule":"pattern"},`+
		`{"location":"query.tags[1]","message":"must match the pattern","rule":"pattern"},`+
		`{"location":"body.slug","message":"must match the pattern","rule":"pattern"},`+
		`{"location":"body.sku","message":"must be between 4 and 6 characters","rule":"length","min":4,"max":6},`+
		`{"location":"body.digit","message":"must match the pattern","rule":"pattern"},`+
		`{"location":"body.maybe","message":"must match the pattern","rule":"pattern"}]}`)
}

func TestAValueThatMatchesItsPatternAndLengthPassesAndAPatternWithoutAnchorsMatchesAnywhere(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[PatternRules]) })
	r := srv.Do(jsonReq("POST", "/b?slug=a-b&tags=x&tags=y", `{"slug":"ok-1","sku":"AB12","digit":"abc1def"}`))
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r = srv.Do(jsonReq("POST", "/b?slug=a-b&tags=x", `{"slug":"ok","sku":"abcd","digit":"abc"}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"body.sku","message":"must match the pattern","rule":"pattern"},`+
		`{"location":"body.digit","message":"must match the pattern","rule":"pattern"}]}`)
}

func TestAPatternMessageNeverHoldsTheClientsValueOrThePattern(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[PatternRules]) })
	r := srv.Do(jsonReq("POST", "/b?slug=a&tags=a", `{"slug":"SECRET-VALUE","sku":"ABCD1","digit":"1"}`))
	if s := r.Text(); strings.Contains(s, "SECRET") || strings.Contains(s, "a-z0-9") {
		t.Fatalf("the answer holds the value or the pattern: %s", s)
	}
}

func TestTheAPIDocumentStatesAPatternAsPatternBesideItsLength(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Post("/b", echo[PatternRules])
	})
	doc := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()
	for _, want := range []string{`"pattern":"^[a-z0-9-]+$"`, `"pattern":"^[A-Z]+\\d+$"`, `"minLength":4`, `"maxLength":6`, `"pattern":"\\d"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("the document lacks %s", want)
		}
	}
}

// ---- CSV: a list sent as one comma-separated value (ADR 0042) ----

type Stage string

func (Stage) Values() []Stage { return []Stage{"won", "lost", "open"} }

type Stages bites.CSV[Stage]

func (Stages) CommaSeparated()       {}
func (Stages) Items() (min, max int) { return 1, 2 }

type CSVIn struct {
	Query struct {
		Stage bites.CSV[Stage]
		IDs   bites.Opt[bites.CSV[int]]
		Name  bites.CSV[bites.NonEmpty]
	}
}

func csvSrv(t *testing.T) *bitetest.Server {
	return start(t, func(app *bites.App) {
		app.Get("/c", func(_ context.Context, in CSVIn) (struct {
			Stage, Name []string
			N           int
			Set         bool
		}, error) {
			var out struct {
				Stage, Name []string
				N           int
				Set         bool
			}
			for _, s := range in.Query.Stage {
				out.Stage = append(out.Stage, string(s))
			}
			for _, s := range in.Query.Name {
				out.Name = append(out.Name, string(s))
			}
			ids, ok := in.Query.IDs.Get()
			out.N, out.Set = len(ids), ok
			return out, nil
		})
	})
}

func TestACSVQueryValueIsSplitAtCommasAndEachElementIsDecodedByItsType(t *testing.T) {
	srv := csvSrv(t)
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/c?stage=won,lost&name=a,b&ids=1,2,3"})
	if r.Status != 200 || r.Text() != `{"stage":["won","lost"],"name":["a","b"],"n":3,"set":true}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r = srv.Do(bitetest.Request{Method: "GET", Target: "/c?stage=won%2Clost&name=a"})
	if r.Status != 200 || !strings.Contains(r.Text(), `"stage":["won","lost"]`) || !strings.Contains(r.Text(), `"set":false`) {
		t.Fatalf("a percent-encoded comma splits too: %d %s", r.Status, r.Text())
	}
}

func TestAnEmptyCSVValueIsAnEmptyListAndAMissingKeyIsRequired(t *testing.T) {
	srv := csvSrv(t)
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/c?stage=&name=&ids="})
	if r.Status != 200 || r.Text() != `{"stage":[],"name":[],"n":0,"set":true}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	wantProblem(t, srv.Do(bitetest.Request{Method: "GET", Target: "/c?name=a"}), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"query.stage","message":"required","rule":"required"}]}`)
}

func TestACSVElementThatIsWrongNamesItsIndexAndAnEmptyElementIsDecodedNotDropped(t *testing.T) {
	srv := csvSrv(t)
	wantProblem(t, srv.Do(bitetest.Request{Method: "GET", Target: "/c?stage=won,bogus&name=a,,b&ids=1,x"}), 400,
		`{"title":"Bad Request","status":400,"errors":[`+
			`{"location":"query.stage[1]","message":"expected one of: won, lost, open","rule":"values"},`+
			`{"location":"query.name[1]","message":"must not be empty","rule":"nonempty"},`+
			`{"location":"query.ids[1]","message":"expected an integer","rule":"type"}]}`)
	wantProblem(t, srv.Do(bitetest.Request{Method: "GET", Target: "/c?stage=won,&name=a"}), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"query.stage[1]","message":"expected one of: won, lost, open","rule":"values"}]}`)
}

func TestACSVKeySentTwiceIsA400SentMoreThanOnce(t *testing.T) {
	srv := csvSrv(t)
	wantProblem(t, srv.Do(bitetest.Request{Method: "GET", Target: "/c?stage=won&stage=lost&name=a"}), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"query.stage","message":"sent more than once"}]}`)
}

type CSVHeaderIn struct {
	Header struct{ XIDs bites.CSV[int] }
	Query  struct{ Stage Stages }
}

func TestACSVHeaderTrimsSpacesAroundCommasAndAnItemsBoundOnANamedCSVIsA400(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/h", func(_ context.Context, in CSVHeaderIn) (struct{ N, M int }, error) {
			return struct{ N, M int }{len(in.Header.XIDs), len(in.Query.Stage)}, nil
		})
	})
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/h?stage=won", Header: map[string][]string{"X-Ids": {"1, 2 ,3"}}})
	if r.Status != 200 || r.Text() != `{"n":3,"m":1}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	wantProblem(t, srv.Do(bitetest.Request{Method: "GET", Target: "/h?stage=won,lost,open", Header: map[string][]string{"X-Ids": {"1"}}}), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"query.stage","message":"must have between 1 and 2 items","rule":"items","min":1,"max":2}]}`)
}

func TestTheAPIDocumentDescribesACSVQueryAsAnArrayInFormStyleWithoutExplode(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/c", func(context.Context, CSVIn) (struct{}, error) { return struct{}{}, nil })
		app.Get("/h", func(context.Context, CSVHeaderIn) (struct{}, error) { return struct{}{}, nil })
	})
	doc := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()
	for _, want := range []string{`"explode":false`, `"style":"form"`, `"style":"simple"`, `"type":"array"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("the document lacks %s", want)
		}
	}
}

// ---- One-sided bounds: a bound equal to the base type's own limit is not a bound (ADR 0021) ----

type (
	atLeastZero  int64
	atLeastZeroI int
	upTo32       int
	atMostTen    int64
	atLeast12    string
)

func (atLeastZero) Range() (int64, int64)  { return 0, math.MaxInt64 }
func (atLeastZeroI) Range() (int64, int64) { return 0, math.MaxInt }
func (upTo32) Range() (int64, int64)       { return 0, math.MaxInt32 }
func (atMostTen) Range() (int64, int64)    { return math.MinInt64, 10 }
func (atLeast12) Length() (int, int)       { return 12, math.MaxInt }

type OneSidedIn struct {
	Query struct {
		A atLeastZero
		B atLeastZeroI
		C upTo32
		D atMostTen
		E atLeast12
	}
}

func TestABoundEqualToTheBaseTypesLimitIsLeftOutOfTheDocument(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/o", echo[OneSidedIn])
	})
	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name   string
				Schema map[string]any
			}
		}
	}
	if err := json.Unmarshal([]byte(srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()), &doc); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]any{}
	for _, p := range doc.Paths["/o"]["get"].Parameters {
		got[p.Name] = p.Schema
	}
	has := func(name, key string) bool { _, ok := got[name][key]; return ok }
	switch {
	case got["a"]["minimum"] != float64(0) || has("a", "maximum"):
		t.Errorf("an int64 Range up to math.MaxInt64 states a minimum only: %v", got["a"])
	case got["b"]["minimum"] != float64(0) || has("b", "maximum"):
		t.Errorf("an int Range up to math.MaxInt states a minimum only: %v", got["b"])
	case got["c"]["maximum"] != float64(math.MaxInt32):
		t.Errorf("math.MaxInt32 is not an int's limit on this platform, so it stays a bound: %v", got["c"])
	case has("d", "minimum") || got["d"]["maximum"] != float64(10):
		t.Errorf("a Range from the type's minimum states a maximum only: %v", got["d"])
	case got["e"]["minLength"] != float64(12) || has("e", "maxLength"):
		t.Errorf("a Length up to math.MaxInt states a minLength only: %v", got["e"])
	}
}

func TestAOneSidedBoundIsAOneSidedSentenceAndAOneSidedMinMax(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/o", echo[OneSidedIn]) })
	wantProblem(t, srv.Do(bitetest.Request{Method: "GET", Target: "/o?a=-1&b=-1&c=-1&d=11&e=short"}), 400,
		`{"title":"Bad Request","status":400,"errors":[`+
			`{"location":"query.a","message":"expected an integer of at least 0","rule":"range","min":0},`+
			`{"location":"query.b","message":"expected an integer of at least 0","rule":"range","min":0},`+
			`{"location":"query.c","message":"expected an integer between 0 and 2147483647","rule":"range","min":0,"max":2147483647},`+
			`{"location":"query.d","message":"expected an integer of at most 10","rule":"range","max":10},`+
			`{"location":"query.e","message":"must be at least 12 characters","rule":"length","min":12}]}`)
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/o?a=9223372036854775807&b=0&c=5&d=-9&e=twelve-chars"}); r.Status != 200 {
		t.Fatalf("a value inside a one-sided bound is accepted: %d %s", r.Status, r.Text())
	}
}

func TestACSVParameterIsDocumentedAsAnArrayOfItsElementsWithTheItemsBound(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/c", func(context.Context, CSVIn) (struct{}, error) { return struct{}{}, nil })
		app.Get("/h", func(context.Context, CSVHeaderIn) (struct{}, error) { return struct{}{}, nil })
	})
	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []map[string]any
		}
		Components struct{ Schemas map[string]map[string]any }
	}
	if err := json.Unmarshal([]byte(srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()), &doc); err != nil {
		t.Fatal(err)
	}
	param := func(path, name string) map[string]any {
		for _, p := range doc.Paths[path]["get"].Parameters {
			if p["name"] == name {
				return p
			}
		}
		t.Fatalf("no parameter %s on %s", name, path)
		return nil
	}
	schema := func(p map[string]any) map[string]any { return p["schema"].(map[string]any) }
	stage := param("/c", "stage")
	items := schema(stage)["items"].(map[string]any)
	if schema(stage)["type"] != "array" || items["$ref"] != "#/components/schemas/Stage" || stage["style"] != "form" || stage["explode"] != false {
		t.Errorf("a plain CSV of an enum is an array of its $ref, form style, no explode: %v", stage)
	}
	if _, ok := doc.Components.Schemas["Stage"]["enum"]; !ok {
		t.Errorf("the enum the items refer to is a component: %v", doc.Components.Schemas["Stage"])
	}
	ids := param("/c", "ids")
	if schema(ids)["type"] != "array" || ids["required"] != false || schema(ids)["items"].(map[string]any)["type"] != "integer" {
		t.Errorf("an Opt CSV is an optional array: %v", ids)
	}
	named := param("/h", "stage")
	if s := schema(named); s["type"] != "array" || s["minItems"] != float64(1) || s["maxItems"] != float64(2) {
		t.Errorf("a named CSV states its Items bound: %v", named)
	}
	if h := param("/h", "X-Ids"); schema(h)["type"] != "array" || h["style"] != "simple" {
		t.Errorf("a header CSV is a simple array: %v", h)
	}
}

// ---- Range on a float, and the checked formats (ADR 0021) ----

type Money float64

func (Money) Range() (min, max float64) { return 0.01, 1000000 }

type Discount float64

func (Discount) Range() (min, max float64) { return 0, 0.5 }

type NotNegative float64

func (NotNegative) Range() (min, max float64) { return 0, math.MaxFloat64 }

type Address string

func (Address) Format() string { return "email" }

type Link string

func (Link) Format() string { return "uri" }

type Host string

func (Host) Format() string { return "hostname" }

type V4 string

func (V4) Format() string { return "ipv4" }

type V6 string

func (V6) Format() string { return "ipv6" }

type Day string

func (Day) Format() string { return "date" }

type Moment string

func (Moment) Format() string { return "date-time" }

type Ident string

func (Ident) Format() string { return "uuid" }

type Quote string

func (Quote) Format() string { return "money" }

type BoundsIn struct {
	Query struct {
		Price Money
		Floor NotNegative
	}
	Body struct {
		Amount Money
		Lines  []Money
		Extra  bites.Opt[Discount]
		Mail   Address
		Site   Link
		Host   Host
		V4     V4
		V6     V6
		Day    Day
		At     Moment
		ID     Ident
		Memo   Quote
	}
}

func TestAFloatRangeIsA400WithTheRangeRuleAndNumericBoundsInEverySection(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[BoundsIn]) })
	r := srv.Do(jsonReq("POST", "/b?price=0&floor=-1", `{"amount":2000000,"lines":[1,0],"extra":0.75,"mail":"a@b.co","site":"a:b","host":"h","v4":"1.2.3.4","v6":"::1","day":"2026-01-01","at":"2026-01-01T00:00:00Z","id":"123e4567-e89b-12d3-a456-426614174000","memo":"m"}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"query.price","message":"expected a number between 0.01 and 1000000","rule":"range","min":0.01,"max":1000000},`+
		`{"location":"query.floor","message":"expected a number of at least 0","rule":"range","min":0},`+
		`{"location":"body.amount","message":"expected a number between 0.01 and 1000000","rule":"range","min":0.01,"max":1000000},`+
		`{"location":"body.lines[1]","message":"expected a number between 0.01 and 1000000","rule":"range","min":0.01,"max":1000000},`+
		`{"location":"body.extra","message":"expected a number between 0 and 0.5","rule":"range","min":0,"max":0.5}]}`)
	if r := srv.Do(jsonReq("POST", "/b?price=19.99&floor=0", `{"amount":0.01,"lines":[1e6],"mail":"a@b.co","site":"a:b","host":"h","v4":"1.2.3.4","v6":"::1","day":"2026-01-01","at":"2026-01-01T00:00:00Z","id":"123e4567-e89b-12d3-a456-426614174000","memo":"m"}`)); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAValueOfACheckedFormatThatIsNotOfIt400sWithTheFormatRuleAndNeverEchoesTheValue(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[BoundsIn]) })
	r := srv.Do(jsonReq("POST", "/b?price=1&floor=0", `{"amount":1,"lines":[],"mail":"SECRET","site":"/relative","host":"a_b","v4":"::1","v6":"1.2.3.4","day":"2026-02-30","at":"yesterday","id":"123","memo":"anything"}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"body.mail","message":"must be an email address","rule":"format","format":"email"},`+
		`{"location":"body.site","message":"must be an absolute URI","rule":"format","format":"uri"},`+
		`{"location":"body.host","message":"must be a host name","rule":"format","format":"hostname"},`+
		`{"location":"body.v4","message":"must be an IPv4 address","rule":"format","format":"ipv4"},`+
		`{"location":"body.v6","message":"must be an IPv6 address","rule":"format","format":"ipv6"},`+
		`{"location":"body.day","message":"must be a date (YYYY-MM-DD)","rule":"format","format":"date"},`+
		`{"location":"body.at","message":"must be a date and time (RFC 3339)","rule":"format","format":"date-time"},`+
		`{"location":"body.id","message":"must be a UUID","rule":"format","format":"uuid"}]}`)
	if strings.Contains(r.Text(), "SECRET") {
		t.Error("the client's value was echoed")
	}
	ok := `{"amount":1,"lines":[],"mail":"ada@example.com","site":"https://example.com","host":"api.example.com","v4":"10.0.0.1","v6":"::1","day":"2026-02-28","at":"2026-11-03T10:00:00Z","id":"123e4567-e89b-12d3-a456-426614174000","memo":"anything"}`
	if r := srv.Do(jsonReq("POST", "/b?price=1&floor=0", ok)); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestACheckedFormatInTheQueryIsA400AtTheQueryLocation(t *testing.T) {
	type in struct {
		Query struct{ Email Address }
	}
	srv := start(t, func(app *bites.App) { app.Get("/q", echo[in]) })
	wantProblem(t, srv.Do(bitetest.Request{Method: "GET", Target: "/q?email=nope"}), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"query.email","message":"must be an email address","rule":"format","format":"email"}]}`)
}

func TestTheAPIDocumentStatesAFloatRangeAsMinimumAndMaximumAndTheFormatsAsTheyWere(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Post("/b", echo[BoundsIn])
	})
	doc := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()
	for _, want := range []string{`"maximum":1000000`, `"minimum":0.01`, `"format":"double"`, `"format":"email"`, `"format":"hostname"`, `"format":"money"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("the document lacks %s", want)
		}
	}
	if strings.Contains(doc, `1.7976931348623157e`) {
		t.Error("a bound that is the type's limit was stated")
	}
}

// ---- Exclusive bounds, MultipleOf, Unique and Members (ADR 0021) ----

type Price float64

func (Price) ExclusiveMin() float64 { return 0 }

type Fraction float64

func (Fraction) ExclusiveMin() float64 { return 0 }
func (Fraction) ExclusiveMax() float64 { return 1 }

type Capped float64

func (Capped) Range() (min, max float64) { return -math.MaxFloat64, 1000 }
func (Capped) ExclusiveMin() float64     { return 0 }

type Cents float64

func (Cents) MultipleOf() float64 { return 0.01 }

type Step int

func (Step) Range() (min, max int64) { return 10, 100 }
func (Step) MultipleOf() int64       { return 5 }

type EvenWide int64

func (EvenWide) MultipleOf() int64 { return 2 }

type Handles []string

func (Handles) Unique() bool { return true }

type Picks []int

func (Picks) Items() (min, max int) { return 1, 40 }
func (Picks) Unique() bool          { return true }

type Scores []float64

func (Scores) Unique() bool { return true }

type Refs []uuid.UUID

func (Refs) Unique() bool { return true }

type Settings map[string]string

func (Settings) Members() (min, max int) { return 1, 2 }

type StrictIn struct {
	Query struct {
		Price Price
		Tags  bites.Opt[Handles]
	}
	Body struct {
		Ratio  Fraction
		Capped Capped
		Cents  Cents
		Step   Step
		Wide   EvenWide
		Opts   Settings
		Picks  Picks
		Scores Scores
		Refs   Refs
	}
}

const strictOK = `{"ratio":0.5,"capped":1000,"cents":19.99,"step":15,"wide":8,"opts":{"a":"1"},"picks":[3,1,2],"scores":[1,2.5],"refs":["123e4567-e89b-12d3-a456-426614174000"]}`

func TestAFloatEqualToAnExclusiveBoundIs400NamingTheBoundAsExclusive(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[StrictIn]) })
	r := srv.Do(jsonReq("POST", "/b?price=0", `{"ratio":1,"capped":0,"cents":1,"step":10,"wide":2,"opts":{"a":"1"},"picks":[1],"scores":[],"refs":[]}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"query.price","message":"expected a number greater than 0","rule":"range","exclusive_min":0},`+
		`{"location":"body.ratio","message":"expected a number greater than 0 and less than 1","rule":"range","exclusive_min":0,"exclusive_max":1},`+
		`{"location":"body.capped","message":"expected a number greater than 0 and at most 1000","rule":"range","exclusive_min":0,"max":1000}]}`)
	for _, ok := range []string{`{"ratio":0.0001,"capped":1000}`, `{"ratio":0.9999,"capped":0.001}`} {
		body := strings.Replace(strictOK, `"ratio":0.5,"capped":1000`, strings.Trim(ok, "{}"), 1)
		if r := srv.Do(jsonReq("POST", "/b?price=0.01", body)); r.Status != 200 {
			t.Errorf("%s: %d %s", ok, r.Status, r.Text())
		}
	}
}

func TestAValueThatIsNotAMultipleIs400WithTheMultipleRuleAndTheStepAfterItsRangeIsChecked(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[StrictIn]) })
	r := srv.Do(jsonReq("POST", "/b?price=1", `{"ratio":0.5,"capped":1,"cents":19.995,"step":17,"wide":7,"opts":{"a":"1"},"picks":[1],"scores":[],"refs":[]}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"body.cents","message":"expected a multiple of 0.01","rule":"multiple","multiple":0.01},`+
		`{"location":"body.step","message":"expected a multiple of 5","rule":"multiple","multiple":5},`+
		`{"location":"body.wide","message":"expected a multiple of 2","rule":"multiple","multiple":2}]}`)
	r = srv.Do(jsonReq("POST", "/b?price=1", `{"ratio":0.5,"capped":1,"cents":1,"step":105,"wide":2,"opts":{"a":"1"},"picks":[1],"scores":[],"refs":[]}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.step","message":"expected an integer between 10 and 100","rule":"range","min":10,"max":100}]}`)
	if r := srv.Do(jsonReq("POST", "/b?price=1", strictOK)); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAMultipleOfAFloatToleratesTheErrorOfDecimalDigitsAndNothingBigger(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[StrictIn]) })
	body := func(cents string) *bitetest.Request {
		r := jsonReq("POST", "/b?price=1", strings.Replace(strictOK, `"cents":19.99`, `"cents":`+cents, 1))
		return &r
	}
	for _, c := range []string{"0.3", "0.07", "1.15", "19.99", "1e6", "123456.78", "0"} {
		if r := srv.Do(*body(c)); r.Status != 200 {
			t.Errorf("%s is a whole number of cents: %d %s", c, r.Status, r.Text())
		}
	}
	for _, c := range []string{"0.005", "0.0100000001", "19.995", "1e-20", "0.015"} {
		if r := srv.Do(*body(c)); r.Status != 400 {
			t.Errorf("%s is not a whole number of cents: %d %s", c, r.Status, r.Text())
		}
	}
}

func TestAListThatRepeatsAnItemIs400AtTheFirstRepeatWithTheUniqueRuleAndNoEcho(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[StrictIn]) })
	r := srv.Do(jsonReq("POST", "/b?price=1&tags=a&tags=SECRET&tags=b&tags=SECRET", strings.Replace(strictOK, `"picks":[3,1,2]`, `"picks":[3,1,3,3]`, 1)))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"query.tags[3]","message":"repeats an earlier item","rule":"unique"},`+
		`{"location":"body.picks[2]","message":"repeats an earlier item","rule":"unique"}]}`)
	if strings.Contains(r.Text(), "SECRET") {
		t.Error("the client's value was echoed")
	}
	// 1 and 1.0 are one number, as in JSON Schema.
	r = srv.Do(jsonReq("POST", "/b?price=1", strings.Replace(strictOK, `"scores":[1,2.5]`, `"scores":[2.5,1,1.0]`, 1)))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.scores[2]","message":"repeats an earlier item","rule":"unique"}]}`)
	r = srv.Do(jsonReq("POST", "/b?price=1", strings.Replace(strictOK, `"refs":["123e4567-e89b-12d3-a456-426614174000"]`, `"refs":["123e4567-e89b-12d3-a456-426614174000","123E4567-E89B-12D3-A456-426614174000"]`, 1)))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.refs[1]","message":"repeats an earlier item","rule":"unique"}]}`)
	if r := srv.Do(jsonReq("POST", "/b?price=1&tags=a&tags=A&tags=b", strictOK)); r.Status != 200 {
		t.Fatalf("strings differing in case are different: %d %s", r.Status, r.Text())
	}
}

func TestAUniqueListLongerThanSixteenItemsIsComparedThroughASetAndFindsTheSameRepeat(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[StrictIn]) })
	long := make([]string, 0, 40)
	for i := 0; i < 39; i++ {
		long = append(long, strconv.Itoa(i))
	}
	ok := `[` + strings.Join(long, ",") + `]`
	body := func(picks string) bitetest.Request {
		return jsonReq("POST", "/b?price=1", strings.Replace(strictOK, `"picks":[3,1,2]`, `"picks":`+picks, 1))
	}
	if r := srv.Do(body(ok)); r.Status != 200 {
		t.Fatalf("39 distinct numbers: %d %s", r.Status, r.Text())
	}
	r := srv.Do(body(`[` + strings.Join(long, ",") + `,17]`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.picks[39]","message":"repeats an earlier item","rule":"unique"}]}`)
}

func TestAnItemThatDidNotDecodeIsNotComparedSoAZeroIsNotAFalseRepeat(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/b", echo[StrictIn]) })
	r := srv.Do(jsonReq("POST", "/b?price=1", strings.Replace(strictOK, `"picks":[3,1,2]`, `"picks":["x","y"]`, 1)))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"body.picks[0]","message":"expected an integer","rule":"type"},`+
		`{"location":"body.picks[1]","message":"expected an integer","rule":"type"}]}`)
}

func TestAMapOutsideItsMembersBoundIs400AndTheMembersPastTheMaxAreNotStored(t *testing.T) {
	type in struct{ Body Settings }
	srv := start(t, func(app *bites.App) {
		app.Post("/b", func(_ context.Context, in in) (struct{ N int }, error) { return struct{ N int }{len(in.Body)}, nil })
	})
	wantProblem(t, srv.Do(jsonReq("POST", "/b", `{}`)), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"body","message":"must have between 1 and 2 members","rule":"members","min":1,"max":2}]}`)
	big := new(strings.Builder)
	big.WriteString(`{`)
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(big, `"k%d":"v",`, i)
	}
	big.WriteString(`"z":"v"}`)
	wantProblem(t, srv.Do(jsonReq("POST", "/b", big.String())), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"body","message":"must have between 1 and 2 members","rule":"members","min":1,"max":2}]}`)
	if r := srv.Do(jsonReq("POST", "/b", `{"a":"1","b":"2"}`)); r.Status != 200 || !strings.Contains(r.Text(), `"N":2`) && !strings.Contains(r.Text(), `"n":2`) {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestTheAPIDocumentStatesExclusiveBoundsMultipleOfUniqueItemsAndMemberCounts(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Post("/b", echo[StrictIn])
	})
	doc := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()
	for _, want := range []string{
		`"exclusiveMinimum":0`, `"exclusiveMaximum":1`, `"maximum":1000`, `"multipleOf":0.01`, `"multipleOf":5`, `"multipleOf":2`,
		`"uniqueItems":true`, `"minProperties":1`, `"maxProperties":2`, `"minItems":1`, `"maxItems":40`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the document lacks %s", want)
		}
	}
	var d struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name   string
				Schema map[string]any
			}
		}
	}
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		t.Fatal(err)
	}
	for _, p := range d.Paths["/b"]["post"].Parameters {
		switch p.Name {
		case "price":
			if p.Schema["exclusiveMinimum"] != float64(0) || p.Schema["minimum"] != nil {
				t.Errorf("a Price parameter states exclusiveMinimum and no minimum: %v", p.Schema)
			}
		case "tags":
			if p.Schema["uniqueItems"] != true || p.Schema["minItems"] != nil {
				t.Errorf("a Handles parameter states uniqueItems and no item bound: %v", p.Schema)
			}
		}
	}
}
