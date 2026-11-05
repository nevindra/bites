package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/pgnotify"
)

// This test runs against a real Postgres, and only when CHAT_TEST_DATABASE_URL names one:
//
//	docker run -d --name bites-example-pg -e POSTGRES_PASSWORD=chat -p 127.0.0.1:5432:5432 postgres:18
//	CHAT_TEST_DATABASE_URL=postgres://postgres:chat@127.0.0.1:5432/postgres go test ./...
//
// It uses room names of its own, so it can run twice against the same database.
func TestAMessagePostedOnOneInstanceReachesTheSocketsOfBothThroughTheBroker(t *testing.T) {
	url := os.Getenv("CHAT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.applySchema(ctx); err != nil { // a second application changes nothing
		t.Fatalf("the schema is not idempotent: %v", err)
	}

	// Two instances over one database: the sockets of each hear what the other posts.
	instance := func() *bitetest.Server {
		broker := pgnotify.New(pgnotify.Options{URL: url, Channel: "chat_it", Pool: store.pool, Log: quiet})
		done := make(chan struct{})
		go func() { broker.Run(ctx); close(done) }()
		t.Cleanup(func() { broker.Close(); <-done })
		deadline := time.Now().Add(10 * time.Second)
		for !broker.Listening() {
			if time.Now().After(deadline) {
				t.Fatal("the LISTEN connection did not come up")
			}
			time.Sleep(5 * time.Millisecond)
		}
		return bitetest.Start(t, newApp(testConfig(), store, NewHub(quiet, broker), quiet))
	}
	srv, other := instance(), instance()
	name := fmt.Sprintf("it-%d", time.Now().UnixNano())
	r := post(srv, "/rooms", "ada", `{"name":"`+name+`"}`)
	var room Room
	if r.Status != 201 || r.JSON(&room) != nil || room.ID == 0 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r = post(srv, "/rooms", "ada", `{"name":"`+name+`"}`); r.Status != 409 {
		t.Fatalf("duplicate: %d %s", r.Status, r.Text())
	}

	target := fmt.Sprintf("/rooms/%d/ws", room.ID)
	ada := srv.DialWS(bitetest.WSRequest{Target: target, Header: auth("ada"), Timeout: 10 * time.Second})
	grace := other.DialWS(bitetest.WSRequest{Target: target, Header: auth("grace"), Timeout: 10 * time.Second})

	ada.SendJSON(Say{Text: "over the socket"})
	for _, ws := range []*bitetest.WS{ada, grace} {
		var f Frame
		ws.ReadJSON(&f)
		if f.Message == nil || f.Message.Text != "over the socket" || f.Message.Author != "ada" || f.Message.RoomID != room.ID || f.Message.ID == 0 {
			t.Fatalf("%+v", f)
		}
	}

	if r = post(other, fmt.Sprintf("/rooms/%d/messages", room.ID), "grace", `{"text":"over http, with \"quotes\" and é"}`); r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	for _, ws := range []*bitetest.WS{ada, grace} {
		var f Frame
		ws.ReadJSON(&f)
		if f.Message == nil || f.Message.Text != `over http, with "quotes" and é` || f.Message.Author != "grace" {
			t.Fatalf("%+v", f)
		}
	}

	var page MessagePage
	if r = get(srv, fmt.Sprintf("/rooms/%d/messages", room.ID)); r.JSON(&page) != nil || len(page.Items) != 2 || page.Items[0].Author != "grace" {
		t.Fatalf("%s", r.Text())
	}
	if r = post(srv, "/rooms/9223372036854775807/messages", "ada", `{"text":"x"}`); r.Status != 404 {
		t.Fatalf("a room that is not there: %d %s", r.Status, r.Text())
	}
}
