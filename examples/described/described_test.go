package described_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/examples/described"
)

func TestTheGeneratedDescriptionsReachTheAPIDocumentThroughTheRegistrationCheck(t *testing.T) {
	app := bites.New()
	app.Get("/users/{user_id}/posts", described.ListPosts)
	app.Get("/users/{id}", described.GetUser)
	app.OpenAPI("/openapi.json", bites.Info{Title: "Described", Version: "1"})
	r := bitetest.Start(t, app).Do(bitetest.Request{Method: "GET", Target: "/openapi.json"})
	var doc map[string]any
	if err := json.Unmarshal(r.Body, &doc); err != nil {
		t.Fatal(err)
	}
	params := doc["paths"].(map[string]any)["/users/{user_id}/posts"].(map[string]any)["get"].(map[string]any)["parameters"].([]any)
	got := map[string]string{}
	for _, p := range params {
		m := p.(map[string]any)
		d, _ := m["description"].(string)
		got[m["name"].(string)] = d
	}
	want := map[string]string{
		"user_id": "UserID is the id of the account whose posts are listed.",
		"page":    "Page counts from 1.",
		"tag":     "Tag keeps the posts that have it.",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("parameter %s: %q, want %q", k, got[k], v)
		}
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	props := schemas["User"].(map[string]any)["properties"].(map[string]any)
	if d := props["name"].(map[string]any)["description"]; d != "Name is how the person is shown to others. It is not unique." {
		t.Errorf("User.name: %v", d)
	}
	if d := props["created_by"].(map[string]any)["description"]; d != "CreatedBy is the id of the account that created the record." {
		t.Errorf("an embedded field: %v", d)
	}
	if _, ok := props["password"]; ok {
		t.Error("a field left off the wire is in the document")
	}
}
