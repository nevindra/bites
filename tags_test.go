package bites_test

import (
	"testing"

	"github.com/nevindra/bites"
)

// The tags bites lets through (ADR 0021); the ones it refuses are rows of the refusal table.

type tagWithValidator struct {
	Body struct {
		Name string `json:"name" validate:"required,min=3"`
	}
}

// Validate stands for an application's own validator reading the tags; here it only says it ran.
func (in tagWithValidator) Validate() error {
	if len(in.Body.Name) < 3 {
		return bites.Invalid("body.name", "must be at least 3 characters")
	}
	return nil
}

func TestAValidateTagIsLetThroughWhenTheInputHasAValidateThatCanRunAValidatorOnIt(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/u", echo[tagWithValidator]) })
	if r := srv.Do(jsonReq("POST", "/u", `{"name":"ab"}`)); r.Status != 400 {
		t.Fatalf("status %d, want 400 from Validate: %s", r.Status, r.Body)
	}
	if r := srv.Do(jsonReq("POST", "/u", `{"name":"abc"}`)); r.Status != 200 {
		t.Fatalf("status %d, want 200: %s", r.Status, r.Body)
	}
}

type tagSharedPaging struct {
	Query struct {
		PageSize int `json:"page_size"`
	}
}

func TestAJSONTagThatAgreesWithTheWireNameOfAQueryFieldIsLetThroughAndTheFieldIsRead(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/p", echo[tagSharedPaging]) })
	r := get(t, srv, "/p?page_size=5")
	if r.Status != 200 || string(r.Body) != `{"query":{"page_size":5}}` {
		t.Fatalf("status %d body %s", r.Status, r.Body)
	}
}

type tagOtherReaders struct {
	Body struct {
		Name  string `json:"name" db:"name" gorm:"column:name" doc:"the name" example:"Ada"`
		Owner tagSelfDecoding
	}
}

// tagSelfDecoding decodes itself, so its fields and their tags are its own.
type tagSelfDecoding struct {
	ID string `validate:"uuid"`
}

func (s *tagSelfDecoding) UnmarshalJSON(b []byte) error { s.ID = string(b); return nil }

func TestTagsAnotherReaderUsesAndTheFieldsOfATypeThatDecodesItselfAreLetThroughOnAJSONBody(t *testing.T) {
	schema := bites.WithJSONSchema[tagSelfDecoding](bites.JSONSchema{Type: "string"})
	srv := startWith(t, func(app *bites.App) { app.Post("/o", echo[tagOtherReaders]) }, schema)
	if r := srv.Do(jsonReq("POST", "/o", `{"name":"Ada","owner":"x"}`)); r.Status != 200 {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
}
