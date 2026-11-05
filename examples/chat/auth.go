package main

import (
	"context"
	"strings"

	"github.com/nevindra/bites"
)

// User is the caller, built by the auth Provider and asked for by an Input field of this type.
type User struct {
	Name string
}

// toyTokens is a TOY. A token is "toy-" and a name of 2 to 20 lower-case letters and digits, so
// "toy-ada" is ada. Anyone can make a token for any name: it proves nothing and exists so the
// example has a caller to show a Provider with. A real service verifies a signed token or looks a
// hash up (docs/guide/security.md); only the function given to BearerAuth would change.
func toyTokens() func(context.Context, bites.Request, struct{}) (User, error) {
	return bites.BearerAuth(func(ctx context.Context, token string) (User, error) {
		name, ok := strings.CutPrefix(token, "toy-")
		if !ok || len(name) < 2 || len(name) > 20 {
			return User{}, bites.ErrInvalidCredentials
		}
		for _, c := range name {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return User{}, bites.ErrInvalidCredentials
			}
		}
		return User{Name: name}, nil
	})
}
