package bites

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"unsafe"

	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/wire"
)

// Page is an HTML page for a browser (ADR 0078): Body is sent as text/html; charset=utf-8 with
// Status, so a form shown again with its mistakes is a Page with 422, and a sign-in form shown
// again after a wrong password is a Page with 422 or 200. It is the one answer that carries an
// error status in place of a returned error, because a browser shows the body of an answer, and a
// problem+json is not a page; an API's error is still an error, answered as problem+json.
//
// The zero value is an empty 200 page. A Status other than 0, 200 and 400 to 499 is a logged 500,
// and so are 401 and 407, which must name a challenge (WWW-Authenticate, RFC 9110) that a page
// does not have: show the form again with 422 instead. A Content-Type in Header is a logged 500
// too, since a Page is HTML; another media type is a Bytes. The API document states a Page as a
// 200 and a 4XX text/html answer.
type Page struct {
	// Status is the status the page is sent with: 0 or 200, or a client error from 400 to 499
	// other than 401 and 407.
	Status int
	// Body is the HTML, copied before the handler returns; render a template into a buffer and
	// give its bytes.
	Body []byte
	// Header holds the response headers to send; Content-Type is the Page's own.
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
}

const pageType = "text/html; charset=utf-8"

// isPageType reports whether t is Page.
func isPageType(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && t.PkgPath() == ownPkg && t.Name() == "Page"
}

// pageStatus is the status a Page is sent with, or 0 and the reason it cannot be sent.
func pageStatus(s int) (int, string) {
	switch {
	case s == 0 || s == 200:
		return 200, ""
	case s == 401 || s == 407:
		return 0, "a Page has status " + strconv.Itoa(s) + ", which must name a challenge (WWW-Authenticate) that a page does not have: show the form again with 422, or return Unauthorized for an API"
	case s >= 400 && s <= 499:
		return s, ""
	}
	return 0, "a Page has status " + strconv.Itoa(s) + "; it must be 200 or a client error from 400 to 499 (a redirect is a SeeOther, a server error is a returned error)"
}

// Submitted is a Body read as an HTML form, application/x-www-form-urlencoded, whose mistakes
// are handed to the handler instead of answered as a 400 (ADR 0078). T is decoded exactly as in
// Form[T]; what differs is who answers a bad form. With Form[T] the framework answers the 400
// problem+json an API client reads; with Submitted[T] the handler runs and shows the form again,
// as a Page, with each mistake next to its field and the text the person typed still in it.
//
// The mistakes are those of decoding the form (a missing field, a number that is not one, a value
// out of range, an unknown field) and those the Input's Validate returns at a body location; a
// mistake in the path, the query, a header or a cookie is still the 400 problem+json, with every
// mistake of the form listed in it too, and so is a body over the limit (413) or of another type
// (415). Validate runs only when decoding found no mistake, as on any route. A route whose Body is
// a Submitted must answer a Page (alone, in a OneOf or among its Returns), or Registration refuses
// it: the handler has nothing else to show the form in.
//
// The zero value holds no form: Valid reports false, and there are no problems and no text.
type Submitted[T any] struct {
	value T
	form  *submission
}

// submission is what a Submitted holds besides the value: copies, never a pooled buffer
// (ADR 0008).
type submission struct {
	problems []FieldError
	sent     [][2]string // each field as sent, decoded, in order
}

// Valid returns the decoded form and true when it has no problem. On false the value holds what
// could be decoded, and the page is shown again from Problem and Sent.
func (s Submitted[T]) Valid() (T, bool) {
	return s.value, s.form != nil && len(s.form.problems) == 0
}

// Problems returns every problem of the form, in the order found: the locations are body.name,
// as in the 400 an API client gets. At most 20 are kept from decoding, the number a 400 lists, and Add appends past them.
func (s Submitted[T]) Problems() []FieldError {
	if s.form == nil {
		return nil
	}
	return append([]FieldError(nil), s.form.problems...)
}

// Problem returns the message of the first problem of the field with the wire name name ("capacity",
// not "body.capacity"), or "" when it has none: a template shows it next to the field, as
// {{.Form.Problem "capacity"}}.
func (s Submitted[T]) Problem(name string) string {
	if s.form == nil {
		return ""
	}
	for _, p := range s.form.problems {
		if loc, ok := strings.CutPrefix(p.Location, "body."); ok && loc == name {
			return p.Message
		}
	}
	return ""
}

// Sent returns the first value sent for the field with the wire name name, decoded, or "" when
// none was sent: what the person typed, to put back in the field when the form is shown again,
// even when it could not be decoded ("abc" for a number). Write it through html/template, which
// escapes it.
func (s Submitted[T]) Sent(name string) string {
	if s.form != nil {
		for _, kv := range s.form.sent {
			if kv[0] == name {
				return kv[1]
			}
		}
	}
	return ""
}

// SentAll returns every value sent for the field with the wire name name, in order (the boxes
// ticked of a list), or nil when none was sent.
func (s Submitted[T]) SentAll(name string) []string {
	var out []string
	if s.form != nil {
		for _, kv := range s.form.sent {
			if kv[0] == name {
				out = append(out, kv[1])
			}
		}
	}
	return out
}

// Add records a problem the handler found itself, such as "this venue is booked on that date",
// on the field with the wire name name; Valid then reports false and Problem returns message. It
// is how a handler that checks the form against its data shows the form again with that mistake.
func (s *Submitted[T]) Add(name, message string) {
	if s.form == nil {
		s.form = &submission{}
	}
	s.form.problems = append(s.form.problems, FieldError{Location: "body." + name, Message: message})
}

// isSubmitted reports whether t is a Submitted[T].
func isSubmitted(t reflect.Type) bool { return isOwn(t, "Submitted[") }

// submittedForm is where the submission pointer of the Submitted Body at off lies in in.
func (b *bodyPlan) submittedForm(in unsafe.Pointer) **submission {
	return (**submission)(unsafe.Add(in, b.off+b.subOff))
}

// keepSent copies the form as sent into the Submitted, each field decoded.
func (b *bodyPlan) keepSent(in unsafe.Pointer, body []byte) {
	own := append([]byte(nil), body...)
	sub := &submission{}
	if len(own) > 0 {
		// readForm has bounded the fields by Limits.MaxFields, so this is bounded too.
		sub.sent = make([][2]string, 0, bytes.Count(own, ampersand)+1)
	}
	wire.EachQuery(own, func(key, value []byte) bool {
		k, err := wire.UnescapeInPlace(key, true)
		if err != nil {
			k = string(key)
		}
		v, err := wire.UnescapeInPlace(value, true)
		if err != nil {
			v = string(value)
		}
		sub.sent = append(sub.sent, [2]string{k, v})
		return true
	})
	*b.submittedForm(in) = sub
}

// keepProblems moves the errors of the form into its Submitted, as copies, and empties errs.
func (b *bodyPlan) keepProblems(in unsafe.Pointer, errs *jsoncodec.Errors) {
	sub := *b.submittedForm(in)
	for _, e := range errs.List {
		sub.problems = append(sub.problems, FieldError{Location: strings.Clone(e.Location), Message: strings.Clone(e.Message)})
	}
	errs.Reset()
}

// keepValidate moves the errors Validate returned into the Submitted when every one of them is at
// a body location, and reports whether it did; any other is answered as the 400.
func (b *bodyPlan) keepValidate(in unsafe.Pointer, err error) bool {
	st := getState()
	defer putState(st)
	if collectFieldErrors(&st.errs, err) == 0 {
		return false
	}
	for _, e := range st.errs.List {
		if e.Location != "body" && !strings.HasPrefix(e.Location, "body.") {
			return false
		}
	}
	b.keepProblems(in, &st.errs)
	return true
}

// formValid reports whether the Submitted Body in in has no problem.
func (b *bodyPlan) formValid(in unsafe.Pointer) bool {
	sub := *b.submittedForm(in)
	return sub != nil && len(sub.problems) == 0
}
