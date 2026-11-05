package main

import (
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// One test that builds the router turns every mismatch in the app's types into a failing test
// (ADR 0007), and the same app is then driven over memory (ADR 0027).
func TestTheREADMEsRoutesRegisterAndAnswer(t *testing.T) {
	app := newApp()
	srv := bitetest.Start(t, app)

	r := srv.Do(bitetest.Request{Method: "GET", Target: "/users/1?expand=true"})
	if r.Status != 200 || r.Text() != `{"id":1,"name":"Ada","admin":true}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}

	r = srv.Do(bitetest.Request{Method: "GET", Target: "/users/abc"})
	want := `{"title":"Bad Request","status":400,"errors":[{"location":"path.id","message":"expected an integer","rule":"type"}]}`
	if r.Status != 400 || r.Get("content-type") != "application/problem+json" || r.Text() != want {
		t.Fatalf("%d %s", r.Status, r.Text())
	}

	r = srv.Do(bitetest.Request{Method: "GET", Target: "/users/9"})
	if r.Status != 404 || r.Text() != `{"title":"Not Found","status":404,"detail":"no such user"}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}

	r = srv.Do(bitetest.Request{Method: "POST", Target: "/users", Body: []byte(`{"name":"Grace"}`),
		Header: map[string][]string{"Content-Type": {"application/json"}}})
	if r.Status != 201 || r.Get("location") != "/users/2" || r.Text() != `{"id":2,"name":"Grace"}` {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Text())
	}
}

// The API document is the contract clients are built from, so it is held to a file in the
// repository: a change to a handler's types fails here with the lines that moved. Regenerate the
// file with BITETEST_UPDATE=1 go test ./examples/users.
func TestTheAPIDocumentIsTheOneInTestdata(t *testing.T) {
	app := newApp()
	app.OpenAPI("/openapi.json", bites.Info{Title: "Users", Version: "1"})
	bitetest.Document(t, app, "testdata/openapi.json")
}
