// Package foreigntags registers an Input whose fields carry the tags Gin, Fiber and Huma read,
// and reports whether Registration refuses it or a request that breaks the tags is accepted.
package foreigntags

import (
	"context"
	"testing"

	nilogo "github.com/nevindra/bites"
	nilotest "github.com/nevindra/bites/bitetest"
)

type CreateUser struct {
	Path struct {
		ID int64 `path:"id" validate:"min=1"`
	}
	Body struct {
		Name  string `json:"name" binding:"required,min=3" validate:"required,min=3,max=40"`
		Email string `json:"email" validate:"required,email"`
	}
}

func create(_ context.Context, in CreateUser) (nilogo.Created[map[string]any], error) {
	return nilogo.Created[map[string]any]{Body: map[string]any{"name": in.Body.Name, "email": in.Body.Email}}, nil
}

func TestForeignTags(t *testing.T) {
	app := nilogo.New()
	refused := func() (msg any) {
		defer func() { msg = recover() }()
		app.Post("/users/{id}", create)
		return nil
	}()
	if refused != nil {
		t.Logf("refused at Registration: %v", refused)
		return
	}
	t.Log("Registration accepted the tags")
	srv := nilotest.Start(t, app)
	resp := srv.Do(nilotest.Request{Method: "POST", Target: "/users/0", Header: map[string]string{"Content-Type": "application/json"}, Body: []byte(`{"name":"a","email":"not-an-email"}`)})
	t.Logf("status %d body %s", resp.Status, resp.Body)
}
