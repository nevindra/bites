// Package described shows the doc-comment generator of ADR 0020: the prose of the API document
// is written once, in the comments of the fields, and `go generate` turns it into the
// Descriptions methods that bites checks against the wire at Registration.
package described

import (
	"context"

	"github.com/nevindra/bites"
)

//go:generate go run github.com/nevindra/bites/cmd/bites-doc

// Audit is the part of a record every resource has; its fields are described as the fields of the
// types that embed it.
type Audit struct {
	// CreatedBy is the id of the account that created the record.
	CreatedBy string
}

// User is a registered account.
type User struct {
	Audit
	// ID is assigned by the server and never changes.
	ID int64
	// Name is how the person is shown to others.
	// It is not unique.
	Name  string
	Email string // Email receives the sign-in link.

	// Password is never sent.
	Password string `json:"-"`
}

// ListPath is the path of the list of a user's posts.
type ListPath struct {
	// UserID is the id of the account whose posts are listed.
	UserID int64
}

// ListQuery filters and pages a list of posts.
type ListQuery struct {
	// Page counts from 1.
	Page int
	// Tag keeps the posts that have it.
	Tag bites.Opt[string]
}

// ListIn is the Input of ListPosts.
type ListIn struct {
	Path  ListPath
	Query ListQuery
}

// Post is one entry of a user's blog.
type Post struct {
	// Title is shown in the list.
	Title string
}

// ListPosts lists the posts of a user.
func ListPosts(_ context.Context, in ListIn) ([]Post, error) { return []Post{{Title: "hello"}}, nil }

// GetUser returns an account.
func GetUser(_ context.Context, _ struct{ Path struct{ ID int64 } }) (User, error) {
	return User{ID: 1, Name: "Ada"}, nil
}
