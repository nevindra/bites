package bites_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

type Join struct{ Room int64 }
type Say struct{ Text string }

// ClientFrame is what a client sends: one of its variants, told apart by "type".
type ClientFrame struct {
	Join bites.Opt[Join]
	Say  bites.Opt[Say]
}

func (ClientFrame) Discriminator() string { return "type" }

type Joined struct{ Room int64 }
type Said struct{ Text, By string }

type ServerFrame struct {
	Joined bites.Opt[Joined]
	Said   bites.Opt[Said]
}

func (ServerFrame) Discriminator() string { return "type" }

func unionApp() *bites.App {
	app := bites.New()
	app.WebSocket("/ws", func(_ context.Context, _ struct{}, s *bites.Socket[ServerFrame]) (bites.Inbox[ClientFrame], error) {
		return bites.Inbox[ClientFrame]{OnMessage: func(_ context.Context, f ClientFrame) error {
			var out ServerFrame
			if j, ok := f.Join.Get(); ok {
				out.Joined.Set(Joined{Room: j.Room})
			} else if m, ok := f.Say.Get(); ok {
				out.Said.Set(Said{Text: m.Text, By: "ada"})
			}
			return s.Send(out)
		}}, nil
	})
	app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
	return app
}

func TestASocketReadsAndSendsATaggedUnionAndAnUnknownTagIsAnErrorThatNamesTheTags(t *testing.T) {
	srv := bitetest.Start(t, unionApp())
	w := srv.DialWS(bitetest.WSRequest{Target: "/ws"})
	w.SendText(`{"type":"join","room":7}`)
	if got := w.ReadText(); got != `{"type":"joined","room":7}` {
		t.Fatalf("got %s", got)
	}
	w.SendText(`{"text":"hi","type":"say"}`)
	if got := w.ReadText(); got != `{"type":"said","text":"hi","by":"ada"}` {
		t.Fatalf("got %s", got)
	}
	w.SendText(`{"type":"shout"}`)
	if code, reason := w.ReadClose(); code != 1007 || reason != "malformed message: message.type: expected one of: join, say" {
		t.Fatalf("an unknown tag: %d %q", code, reason)
	}
}

func TestTheAPIDocumentDescribesAUnionAsOneOfItsVariantsWithTheDiscriminator(t *testing.T) {
	doc, err := unionApp().OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Components struct {
			Schemas map[string]struct {
				OneOf []struct {
					Title      string
					Required   []string
					Properties map[string]struct{ Const string }
				}
				Discriminator struct{ PropertyName string }
			}
		}
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		t.Fatal(err)
	}
	cf, ok := d.Components.Schemas["ClientFrame"]
	if !ok || cf.Discriminator.PropertyName != "type" || len(cf.OneOf) != 2 {
		t.Fatalf("ClientFrame is %+v in %s", cf, doc)
	}
	join := cf.OneOf[0]
	if join.Title != "join" || join.Properties["type"].Const != "join" || join.Required[0] != "type" || join.Required[1] != "room" {
		t.Fatalf("the join variant is %+v", join)
	}
	if sf := d.Components.Schemas["ServerFrame"]; len(sf.OneOf) != 2 || sf.OneOf[1].Properties["type"].Const != "said" {
		t.Fatalf("ServerFrame is %+v", sf)
	}
}

// RefOnly is the one variant of RelayFrame that only servers carry: the reference a Topic
// publishes when a message is too large for its broker.
type RefOnly struct{ ID string }

type RelayFrame struct {
	Said bites.Opt[Said]
	Ref  bites.Opt[RefOnly]
}

func (RelayFrame) Discriminator() string { return "type" }
func (RelayFrame) ServerOnly() []string  { return []string{"Ref"} }

func relayApp(register func(app *bites.App), lb *logBuf) *bites.App {
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithLogger(slog.New(slog.NewTextHandler(lb, nil))))
	register(app)
	app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
	return app
}

func TestTheAPIDocumentOmitsAServerOnlyVariantFromTheUnionAndItsMapping(t *testing.T) {
	app := relayApp(func(app *bites.App) {
		app.WebSocket("/ws", func(_ context.Context, _ struct{}, s *bites.Socket[RelayFrame]) (bites.Inbox[RelayFrame], error) {
			return bites.Inbox[RelayFrame]{}, nil
		})
	}, &logBuf{})
	doc, err := app.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Components struct {
			Schemas map[string]struct {
				OneOf []struct{ Title string }
			}
		}
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"RelayFrame.Request", "RelayFrame.Response"} {
		if rf := d.Components.Schemas[name]; len(rf.OneOf) != 1 || rf.OneOf[0].Title != "said" {
			t.Fatalf("%s is %+v: want the one variant said in %s", name, rf, doc)
		}
	}
	if strings.Contains(string(doc), `"ref"`) || strings.Contains(string(doc), "RefOnly") {
		t.Fatalf("the document names the server-only variant: %s", doc)
	}
}

func TestSendRefusesAServerOnlyVariantNamingItAndSendsNothing(t *testing.T) {
	var sendErr error
	app := relayApp(func(app *bites.App) {
		app.WebSocket("/ws", func(_ context.Context, _ struct{}, s *bites.Socket[RelayFrame]) (bites.Inbox[RelayFrame], error) {
			return bites.Inbox[RelayFrame]{OnMessage: func(_ context.Context, f RelayFrame) error {
				var out RelayFrame
				out.Ref.Set(RefOnly{ID: "n1"})
				sendErr = s.Send(out)
				var ok RelayFrame
				ok.Said.Set(Said{Text: "after"})
				return s.Send(ok)
			}}, nil
		})
	}, &logBuf{})
	srv := bitetest.Start(t, app)
	w := srv.DialWS(bitetest.WSRequest{Target: "/ws"})
	w.SendText(`{"type":"said","text":"hi","by":"x"}`)
	if got := w.ReadText(); got != `{"type":"said","text":"after","by":""}` {
		t.Fatalf("the first frame the client got is %s: the server-only variant must send nothing", got)
	}
	if sendErr == nil || !strings.Contains(sendErr.Error(), "the union bites_test.RelayFrame has its server-only variant ref (field Ref) set") {
		t.Fatalf("Send returned %v", sendErr)
	}
	w.SendText(`{"type":"ref","id":"n1"}`)
	if code, reason := w.ReadClose(); code != 1007 || !strings.HasSuffix(reason, "expected one of: said") {
		t.Fatalf("a client that sends the server-only tag: %d %q", code, reason)
	}
}

func TestAnAnswerHoldingAServerOnlyVariantIsA500WithTheReasonLoggedAndNothingSent(t *testing.T) {
	lb := &logBuf{}
	app := relayApp(func(app *bites.App) {
		app.Get("/relay", func(context.Context, Empty) (RelayFrame, error) {
			var out RelayFrame
			out.Ref.Set(RefOnly{ID: "n1"})
			return out, nil
		})
	}, lb)
	srv := bitetest.Start(t, app)
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/relay"})
	if r.Status != 500 || strings.Contains(r.Text(), "n1") {
		t.Fatalf("answered %d %s", r.Status, r.Text())
	}
	srv.Close()
	if !strings.Contains(lb.String(), "server-only variant ref") {
		t.Fatalf("the reason was not logged: %s", lb.String())
	}
}
