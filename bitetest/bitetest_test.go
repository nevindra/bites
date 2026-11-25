package bitetest_test

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// account is a handler's own output type with no json tags, so its wire names are the app's
// naming policy's.
type account struct {
	ItemID    int64
	Name      string
	CreatedAt string
	Tags      []string
}

func accountApp(opts ...bites.Option) *bites.App {
	app := bites.New(append([]bites.Option{bites.WithClock(bitetest.NewClock())}, opts...)...)
	app.Get("/accounts/{id}", func(_ context.Context, in struct{ Path struct{ ID int64 } }) (account, error) {
		if in.Path.ID == 0 {
			return account{}, bites.NotFound("no such account")
		}
		return account{ItemID: in.Path.ID, Name: "ada", CreatedAt: "2026-01-01", Tags: []string{"a"}}, nil
	})
	app.Get("/text", func(context.Context, struct{}) (bites.Text, error) { return bites.Text{Body: "not json"}, nil })
	return app
}

func TestJSONReadsAnAnswerIntoTheHandlersOwnTypeUnderEitherNamingPolicy(t *testing.T) {
	for name, opts := range map[string][]bites.Option{"SnakeCase": nil, "CamelCase": {bites.WithNaming(bites.CamelCase)}} {
		srv := bitetest.Start(t, accountApp(opts...))
		resp := srv.Do(bitetest.Request{Method: "GET", Target: "/accounts/7"})
		got := bitetest.JSON[account](t, resp)
		if got.ItemID != 7 || got.Name != "ada" || got.CreatedAt != "2026-01-01" || len(got.Tags) != 1 {
			t.Errorf("%s: %s read as %+v", name, resp.Text(), got)
		}
	}
}

func TestResponseJSONMatchesTheWireNamesAsJSONDoes(t *testing.T) {
	srv := bitetest.Start(t, accountApp())
	var got account
	if err := srv.Do(bitetest.Request{Method: "GET", Target: "/accounts/7"}).JSON(&got); err != nil || got.ItemID != 7 || got.CreatedAt != "2026-01-01" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestJSONReadsAProblemAnswerIntoAProblemType(t *testing.T) {
	srv := bitetest.Start(t, accountApp())
	resp := srv.Do(bitetest.Request{Method: "GET", Target: "/accounts/abc"})
	p := bitetest.JSON[struct {
		Status int
		Errors []struct{ Location, Message string }
	}](t, resp)
	if p.Status != 400 || len(p.Errors) != 1 || p.Errors[0].Location != "path.id" {
		t.Fatalf("%s read as %+v", resp.Text(), p)
	}
}

// fatalTB records the failure JSON reports instead of ending the test.
type fatalTB struct {
	testing.TB
	msg string
}

func (f *fatalTB) Helper() {}
func (f *fatalTB) Fatalf(format string, args ...any) {
	f.msg = fmt.Sprintf(format, args...)
}

func TestJSONFailsTheTestWithTheStatusAndTheBodyWhenTheAnswerIsNotJSONOfThatShape(t *testing.T) {
	srv := bitetest.Start(t, accountApp())

	text := srv.Do(bitetest.Request{Method: "GET", Target: "/text"})
	tb := &fatalTB{TB: t}
	bitetest.JSON[account](tb, text)
	if !strings.HasPrefix(tb.msg, "bitetest: decoding the 200 answer as bitetest_test.account: ") || !strings.HasSuffix(tb.msg, "\nbody: not json") {
		t.Errorf("a body that is not JSON: the failure was %q", tb.msg)
	}

	// A problem's "status" is a number, which a string field cannot hold.
	problem := srv.Do(bitetest.Request{Method: "GET", Target: "/accounts/abc"})
	tb = &fatalTB{TB: t}
	bitetest.JSON[struct{ Status string }](tb, problem)
	if !strings.HasPrefix(tb.msg, "bitetest: decoding the 400 answer as struct { Status string }: ") || !strings.HasSuffix(tb.msg, "\nbody: "+problem.Text()) {
		t.Errorf("a member of another type: the failure was %q", tb.msg)
	}
}

// exitTB records the failure and ends the goroutine, as Fatalf ends a test.
type exitTB struct {
	testing.TB
	msg string
}

func (f *exitTB) Helper() {}
func (f *exitTB) Fatalf(format string, args ...any) {
	f.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func TestARequestAfterShutdownFailsSayingTheAppStoppedAndNamingWithShutdownDelay(t *testing.T) {
	app := bites.New()
	app.Get("/x", func(context.Context, struct{}) (bites.NoContent, error) { return bites.NoContent{}, nil })
	tb := &exitTB{TB: t}
	srv := bitetest.Start(tb, app)
	if got := srv.Do(bitetest.Request{Method: "GET", Target: "/x"}); got.Status != 204 {
		t.Fatalf("before Shutdown: %d", got.Status)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Do(bitetest.Request{Method: "GET", Target: "/x"})
	}()
	<-done
	if !strings.Contains(tb.msg, "Shutdown or Close has stopped it") || !strings.Contains(tb.msg, "bites.WithShutdownDelay") {
		t.Errorf("the failure after Shutdown was %q", tb.msg)
	}
}

func TestDoReturnsTheAnswerAnAppGivesBeforeItHasReadTheBody(t *testing.T) {
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Post("/upload", func(context.Context, struct{ Body []byte }) (bites.NoContent, error) {
		return bites.NoContent{}, nil
	}, bites.MaxBody(1024))
	srv := bitetest.Start(t, app)
	resp := srv.Do(bitetest.Request{Method: "POST", Target: "/upload", Body: make([]byte, 4<<20)})
	if resp.Status != 413 {
		t.Fatalf("%d %s", resp.Status, resp.Text())
	}
}

func TestAHeaderGivenSeveralValuesIsSentAsThatManyLines(t *testing.T) {
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Get("/tags", func(_ context.Context, in struct{ Header struct{ XTag []string } }) ([]string, error) {
		return in.Header.XTag, nil
	})
	srv := bitetest.Start(t, app)
	resp := srv.Do(bitetest.Request{Method: "GET", Target: "/tags", Header: map[string][]string{"X-Tag": {"a", "b"}}})
	if resp.Text() != `["a","b"]` {
		t.Fatalf("%d %s", resp.Status, resp.Text())
	}
}

func TestOpenEventsReadsEachEventAndCommentAsTheAppSendsIt(t *testing.T) {
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	publish := make(chan string)
	app.Get("/events", func(context.Context, struct{}) (bites.SSE, error) {
		return bites.SSE{Func: func(ctx context.Context, e bites.EventWriter) error {
			if err := e.Comment("ready"); err != nil {
				return err
			}
			for text := range publish {
				if err := e.Send(bites.Event{ID: "7", Name: "status", Data: text}); err != nil {
					return err
				}
			}
			return e.Send(bites.Event{Retry: 3 * time.Second})
		}}, nil
	})
	srv := bitetest.Start(t, app)
	events := srv.OpenEvents(bitetest.Request{Target: "/events"})
	if c := events.Next(); c.Comment != "ready" {
		t.Fatalf("first: %+v", c)
	}
	publish <- "paid\nshipped"
	if ev := events.Next(); ev.ID != "7" || ev.Name != "status" || ev.Data != "paid\nshipped" {
		t.Fatalf("%+v", ev)
	}
	close(publish)
	if ev := events.Next(); ev.Retry != 3*time.Second || ev.Data != "" {
		t.Fatalf("%+v", ev)
	}
	if _, err := events.TryNext(0); err != io.EOF {
		t.Fatalf("after the end: %v", err)
	}
}

func TestOpenEventsOnAnAnswerThatIsNotAStreamFailsTheTestWithItsStatus(t *testing.T) {
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Get("/events", func(context.Context, struct{}) (bites.SSE, error) {
		return bites.SSE{}, bites.Unauthorized("sign in")
	})
	ft := &fatalTB{TB: t}
	bitetest.Start(ft, app).OpenEvents(bitetest.Request{Target: "/events"})
	if !strings.Contains(ft.msg, "answered 401") || !strings.Contains(ft.msg, "sign in") {
		t.Fatalf("%q", ft.msg)
	}
}

// task is a handler's own output type with the framework's presence types and a Date.
type task struct {
	Title    string
	Due      bites.Null[bites.Date]
	Assignee bites.Null[string]
	Note     bites.Opt[string]
	Tag      bites.Opt[string]
}

func TestJSONReadsNullOptAndDateAsTheAppWritesThem(t *testing.T) {
	due, _ := bites.NewDate(2026, 12, 1)
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Get("/t", func(context.Context, struct{}) (task, error) {
		return task{Title: "docs", Due: bites.NonNull(due), Note: bites.Some("soon")}, nil
	})
	got := bitetest.JSON[task](t, bitetest.Start(t, app).Do(bitetest.Request{Method: "GET", Target: "/t"}))
	if d, ok := got.Due.Get(); !ok || d != due || !got.Assignee.IsNull() || got.Note.Or("") != "soon" || got.Tag.IsSet() || got.Title != "docs" {
		t.Fatalf("%#v", got)
	}
}

type profile struct {
	DisplayName string
	Nickname    bites.Opt[string]
	Deleted     bites.Null[string]
	Role        bites.Opt[string]
}

func profileApp() *bites.App {
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Get("/profile/{id}", func(_ context.Context, in struct{ Path struct{ ID int64 } }) (profile, error) {
		p := profile{DisplayName: "Ada"}
		if in.Path.ID == 1 {
			p.Nickname = bites.Some("ace")
			p.Deleted = bites.NonNull("gone")
		}
		return p, nil
	})
	app.Get("/loose", func(context.Context, struct{}) (profile, error) { return profile{DisplayName: "Ada"}, nil })
	return app
}

func TestResponseJSONFillsAPresentOptAndLeavesAnAbsentOneEmptyUnderTheAppsNaming(t *testing.T) {
	srv := bitetest.Start(t, profileApp())
	with := bitetest.JSON[profile](t, srv.Do(bitetest.Request{Method: "GET", Target: "/profile/1"}))
	if v, ok := with.Nickname.Get(); !ok || v != "ace" || with.DisplayName != "Ada" {
		t.Errorf("present: %+v", with)
	}
	without := bitetest.JSON[profile](t, srv.Do(bitetest.Request{Method: "GET", Target: "/profile/2"}))
	if _, ok := without.Nickname.Get(); ok || without.DisplayName != "Ada" {
		t.Errorf("absent: %+v", without)
	}
}

func TestResponseJSONFillsANullThatHoldsAValueAndOneThatIsNull(t *testing.T) {
	srv := bitetest.Start(t, profileApp())
	with := bitetest.JSON[profile](t, srv.Do(bitetest.Request{Method: "GET", Target: "/profile/1"}))
	if v, ok := with.Deleted.Get(); !ok || v != "gone" {
		t.Errorf("holds: %+v", with.Deleted)
	}
	without := bitetest.JSON[profile](t, srv.Do(bitetest.Request{Method: "GET", Target: "/profile/2"}))
	if _, ok := without.Deleted.Get(); ok {
		t.Errorf("null: %+v", without.Deleted)
	}
}

func TestResponseJSONReadsSnakeCaseWireNamesIntoGoFieldNamesWithTheAppsCodec(t *testing.T) {
	srv := bitetest.Start(t, profileApp())
	body := srv.Do(bitetest.Request{Method: "GET", Target: "/profile/1"})
	if !strings.Contains(body.Text(), `"display_name"`) {
		t.Fatalf("the app wrote %s", body.Text())
	}
	got := bitetest.JSON[profile](t, body)
	if got.DisplayName != "Ada" {
		t.Errorf("read as %+v", got)
	}
}

func TestResponseJSONSkipsAMemberTheTypeHasNoFieldForAndLeavesAMissingRequiredOneZero(t *testing.T) {
	r := bitetest.Response{Body: []byte(`{"extra":1}`)}
	var p profile
	if err := r.JSON(&p); err != nil || p.DisplayName != "" {
		t.Errorf("by hand: %+v %v", p, err)
	}
	srv := bitetest.Start(t, profileApp())
	resp := srv.Do(bitetest.Request{Method: "GET", Target: "/loose"})
	var small struct {
		Nope string
		Ok   int
	}
	if err := resp.JSON(&small); err != nil || small.Ok != 0 {
		t.Errorf("app: %+v %v", small, err)
	}
}

func TestResponseJSONStillReadsIntoAMapAnAnyAndAnErrorForTheWrongType(t *testing.T) {
	srv := bitetest.Start(t, profileApp())
	resp := srv.Do(bitetest.Request{Method: "GET", Target: "/profile/1"})
	var m map[string]any
	if err := resp.JSON(&m); err != nil || m["display_name"] != "Ada" || m["nickname"] != "ace" {
		t.Errorf("map: %v %v", m, err)
	}
	var a any
	if err := resp.JSON(&a); err != nil || a.(map[string]any)["display_name"] != "Ada" {
		t.Errorf("any: %v %v", a, err)
	}
	var wrong struct{ DisplayName int }
	if err := resp.JSON(&wrong); err == nil {
		t.Errorf("a string read as an int gave no error: %+v", wrong)
	}
}

// order is a route's Body section with no json tags, so its wire names are the app's naming
// policy's.
type order struct {
	CustomerName string
	Note         bites.Opt[string]
	Gift         bites.Null[string]
}

// orderApp echoes the order it decoded, with the Content-Type it was sent with.
func orderApp(opts ...bites.Option) *bites.App {
	app := bites.New(append([]bites.Option{bites.WithClock(bitetest.NewClock())}, opts...)...)
	app.Post("/orders", func(_ context.Context, in struct {
		Header struct{ ContentType string }
		Body   order
	}) (map[string]string, error) {
		note, _ := in.Body.Note.Get()
		gift := "null"
		if g, ok := in.Body.Gift.Get(); ok {
			gift = g
		}
		return map[string]string{"customer": in.Body.CustomerName, "note": note, "has_note": fmt.Sprint(in.Body.Note.IsSet()), "gift": gift, "content_type": in.Header.ContentType}, nil
	})
	app.Patch("/orders", func(_ context.Context, in struct {
		Header struct{ ContentType string }
		Body   bites.Raw[struct{ Gift bites.Null[string] }]
	}) (map[string]string, error) {
		return map[string]string{"content_type": in.Header.ContentType, "body": string(in.Body.Bytes())}, nil
	})
	return app
}

func TestRequestJSONIsWrittenWithTheAppsNamingPolicyAndSentAsApplicationJSON(t *testing.T) {
	for name, opts := range map[string][]bites.Option{"SnakeCase": nil, "CamelCase": {bites.WithNaming(bites.CamelCase)}} {
		srv := bitetest.Start(t, orderApp(opts...))
		resp := srv.Do(bitetest.Request{Method: "POST", Target: "/orders", JSON: order{CustomerName: "Ada", Gift: bites.NonNull("socks")}})
		got := bitetest.JSON[map[string]string](t, resp)
		if resp.Status != 200 || got["customer"] != "Ada" || got["has_note"] != "false" || got["gift"] != "socks" || got["content_type"] != "application/json" {
			t.Errorf("%s: %d %s", name, resp.Status, resp.Body)
		}
	}
}

func TestRequestJSONKeepsTheContentTypeTheHeaderGivesAndWritesAMapWithItsKeys(t *testing.T) {
	srv := bitetest.Start(t, orderApp())
	resp := srv.Do(bitetest.Request{Method: "PATCH", Target: "/orders", Header: map[string][]string{"content-type": {"application/json; charset=utf-8"}},
		JSON: map[string]any{"gift": nil}})
	got := bitetest.JSON[map[string]string](t, resp)
	if resp.Status != 200 || got["content_type"] != "application/json; charset=utf-8" || got["body"] != `{"gift":null}` {
		t.Fatalf("%d %s", resp.Status, resp.Body)
	}
}

// failsWith sends r to a server started with a test that records its failure, and returns it.
func failsWith(t *testing.T, r bitetest.Request) string {
	t.Helper()
	tb := &exitTB{TB: t}
	srv := bitetest.Start(tb, orderApp())
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Do(r)
	}()
	<-done
	return tb.msg
}

func TestARequestWithBothBodyAndJSONFailsTheTestSayingToGiveOne(t *testing.T) {
	msg := failsWith(t, bitetest.Request{Method: "POST", Target: "/orders", Body: []byte(`{}`), JSON: order{CustomerName: "Ada"}})
	if msg != "bitetest: the Request to POST /orders has both Body and JSON: give JSON for a Go value written as the app writes JSON, or Body for the bytes themselves, not both" {
		t.Fatalf("the failure was %q", msg)
	}
}

func TestARequestJSONTheAppsCodecCannotWriteFailsTheTestWithTheReason(t *testing.T) {
	msg := failsWith(t, bitetest.Request{Method: "POST", Target: "/orders", JSON: struct{ Done chan int }{}})
	if !strings.HasPrefix(msg, "bitetest: writing the JSON of the Request to POST /orders, a struct { Done chan int }, as the app writes JSON: ") || !strings.HasSuffix(msg, "; send hand-written JSON in Body instead") {
		t.Fatalf("the failure was %q", msg)
	}
}

func TestARequestJSONThatIsABodyWrapperOfBitesFailsTheTestNamingWhatToSend(t *testing.T) {
	msg := failsWith(t, bitetest.Request{Method: "POST", Target: "/orders", JSON: bites.Raw[order]{Value: order{CustomerName: "Ada"}}})
	if !strings.HasPrefix(msg, "bitetest: the JSON of the Request to POST /orders is a bites.Raw[") || !strings.HasSuffix(msg, "a Body type of bites and not a JSON value: give the value it decodes, in.Body.Value, as JSON") {
		t.Fatalf("a Raw: the failure was %q", msg)
	}
	msg = failsWith(t, bitetest.Request{Method: "POST", Target: "/orders", JSON: bites.Form[order]{}})
	if !strings.HasSuffix(msg, "a form is not JSON: send the encoded form (url.Values.Encode) in Body with Content-Type application/x-www-form-urlencoded") {
		t.Fatalf("a Form: the failure was %q", msg)
	}
}

// frame is a tagged union a route reads (ADR 0083).
type frame struct {
	Join bites.Opt[struct{ Room int64 }]
	Say  bites.Opt[struct{ Text string }]
}

func (frame) Discriminator() string { return "type" }

func TestRequestJSONWritesATaggedUnionByItsVariant(t *testing.T) {
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Post("/frames", func(_ context.Context, in struct{ Body frame }) (map[string]string, error) {
		say, ok := in.Body.Say.Get()
		return map[string]string{"say": fmt.Sprint(ok), "text": say.Text, "join": fmt.Sprint(in.Body.Join.IsSet())}, nil
	})
	srv := bitetest.Start(t, app)
	resp := srv.Do(bitetest.Request{Method: "POST", Target: "/frames", JSON: frame{Say: bites.Some(struct{ Text string }{"hi"})}})
	got := bitetest.JSON[map[string]string](t, resp)
	if resp.Status != 200 || got["say"] != "true" || got["text"] != "hi" || got["join"] != "false" {
		t.Fatalf("%d %s", resp.Status, resp.Body)
	}
}

func TestOpenEventsSendsItsRequestJSONAsDoDoes(t *testing.T) {
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Post("/watch", func(_ context.Context, in struct{ Body order }) (bites.SSE, error) {
		return bites.SSE{Func: func(ctx context.Context, w bites.EventWriter) error {
			return w.Send(bites.Event{Name: "hello", Data: in.Body.CustomerName})
		}}, nil
	})
	srv := bitetest.Start(t, app)
	events := srv.OpenEvents(bitetest.Request{Method: "POST", Target: "/watch", JSON: order{CustomerName: "Ada"}})
	if ev := events.Next(); ev.Name != "hello" || ev.Data != "Ada" {
		t.Fatalf("%+v", ev)
	}
}
