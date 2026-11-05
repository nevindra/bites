// Command users is the README's taste as a program: a typed route, a 400 that names the field a
// client got wrong, and a route that is refused if its types do not fit its pattern. Run it and try
//
//	curl -i localhost:8080/users/1?expand=true
//	curl -i localhost:8080/users/abc
//	curl -i -X POST localhost:8080/users -H 'Content-Type: application/json' -d '{"name":"Grace"}'
package main

import (
	"context"
	"log"
	"sync"

	"github.com/nevindra/bites"
)

type GetUser struct {
	Path  struct{ ID int64 }
	Query struct{ Expand bites.Opt[bool] }
}

type CreateUser struct {
	Body struct {
		Name  string
		Email bites.Opt[string]
	}
}

type User struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email,omitzero"`
	Admin bool   `json:"admin,omitzero"`
}

// Users stands in for the struct that owns a database pool (ADR 0014): handlers are its methods,
// registered as method values.
type Users struct {
	mu   sync.Mutex
	byID map[int64]User
}

func (u *Users) Get(ctx context.Context, in GetUser) (User, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	user, ok := u.byID[in.Path.ID]
	if !ok {
		return User{}, bites.NotFound("no such user")
	}
	user.Admin = in.Query.Expand.Or(false) && user.ID == 1
	return user, nil
}

func (u *Users) Create(ctx context.Context, in CreateUser) (bites.Created[User], error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	id := int64(len(u.byID) + 1)
	user := User{ID: id, Name: in.Body.Name, Email: in.Body.Email.Or("")}
	u.byID[id] = user
	return bites.Created[User]{Body: user, Location: "/users/" + itoa(id)}, nil
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// newApp builds the app without serving it, so a test can build the router (ADR 0007).
func newApp() *bites.App {
	app := bites.New()
	users := &Users{byID: map[int64]User{1: {ID: 1, Name: "Ada"}}}

	app.Get("/users/{id}", users.Get)
	app.Post("/users", users.Create, bites.SetsLocation())
	return app
}

func main() {
	log.Fatal(newApp().Listen(":8080"))
}
