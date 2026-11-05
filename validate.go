package bites

import (
	"github.com/nevindra/bites/internal/engine"
	"reflect"

	"github.com/nevindra/bites/internal/jsoncodec"
)

// An Input may have a Validate method (ADR 0021): the checks the types cannot make, such as
// "end is after start". It runs after decoding has succeeded and before the handler, and the
// errors it returns are answered in the same 400 as decoding errors (ADR 0012):
//
//	func (in CreateBooking) Validate() error {
//		if !in.Body.End.After(in.Body.Start) {
//			return bites.Invalid("body.end", "must be after body.start")
//		}
//		return nil
//	}
//
// Validate is declared on the Input by value, as the Input is, with the signature
// func() error; Registration refuses anything else.

// FieldError is one located input error: where in the request, and what is wrong with it. It
// is what Invalid returns and what the 400 lists.
type FieldError struct {
	// Location is in wire names, never Go field names: body.name, query.limit, header.X-Token
	// (a header by its canonical name, which the naming policy does not touch, ADR 0011),
	// body.items[2].qty. The framework cannot check it, because it is written by hand.
	Location string
	// Message says what was expected. Write it as the framework's own are written: a short
	// sentence about the field, without the client's value in it.
	Message string
}

func (e *FieldError) Error() string { return e.Location + ": " + e.Message }

// Invalid is a located input error for one field: the 400 lists it, by its location, as it lists
// the decoder's own. A Validate method returns it, and so may a handler, a Provider or a
// middleware that finds a field wrong only after a lookup (a product that does not exist); found
// anywhere in the returned error's tree, it makes the answer that 400. Several are returned
// together with errors.Join, and the 400 lists each:
//
//	return errors.Join(
//		bites.Invalid("body.start", "must be in the future"),
//		bites.Invalid("body.end", "must be after body.start"),
//	)
func Invalid(location, message string) error {
	return &FieldError{Location: location, Message: message}
}

// collectFieldErrors adds every FieldError found in err's tree (errors.Join, and fmt.Errorf with
// several %w, are trees) to errs, within the cap of ADR 0012, and reports how many it found.
func collectFieldErrors(errs *jsoncodec.Errors, err error) int {
	switch e := err.(type) {
	case nil:
		return 0
	case *FieldError:
		addError(errs, e.Location, e.Message)
		return 1
	case interface{ Unwrap() []error }:
		n := 0
		for _, c := range e.Unwrap() {
			n += collectFieldErrors(errs, c)
		}
		return n
	case interface{ Unwrap() error }:
		return collectFieldErrors(errs, e.Unwrap())
	}
	return 0
}

// answerLocated answers err with the 400 of ADR 0012 when its tree holds a located error, and
// reports whether it did.
func (a *App) answerLocated(exch *engine.Exchange, err error) bool {
	st := getState()
	defer putState(st)
	if collectFieldErrors(&st.errs, err) == 0 {
		return false
	}
	invalidInput(exch, &st.errs)
	return true
}

// answerInvalid answers the error Validate returned. Field errors are the 400 of ADR 0012; an
// error with none in it is not a located input error, so it is answered as a handler's error
// would be (ADR 0049): the status it carries, or a 500 with no message, logged.
func (a *App) answerInvalid(exch *engine.Exchange, err error) {
	st := getState()
	defer putState(st)
	if collectFieldErrors(&st.errs, err) == 0 {
		a.answerError(exch, err)
		return
	}
	invalidInput(exch, &st.errs)
}

var errorType = reflect.TypeFor[error]()

// validateOf looks for a Validate method on the Input type t and checks it (ADR 0021). It
// reports whether the Input has a usable one.
func validateOf(t reflect.Type) (bool, error) {
	label := typeLabel(t)
	if m, ok := t.MethodByName("Validate"); ok {
		if mt := m.Type; mt.NumIn() != 1 || mt.NumOut() != 1 || mt.Out(0) != errorType {
			return false, refusalf("%s has a Validate method of type %s; it must be func() error, so that it runs after decoding and reports its errors with Invalid", label, trimRecv(mt))
		}
		return true, nil
	}
	if m, ok := reflect.PointerTo(t).MethodByName("Validate"); ok {
		if mt := m.Type; mt.NumIn() != 1 || mt.NumOut() != 1 || mt.Out(0) != errorType {
			return false, refusalf("%s has a Validate method of type %s; it must be func() error, so that it runs after decoding and reports its errors with Invalid", label, trimRecv(mt))
		}
		return false, refusalf("%s.Validate has a pointer receiver, but an Input is taken by value, so the method would never be found: declare it on %s", label, label)
	}
	return false, nil
}

// trimRecv prints a method's type without its receiver: func() string.
func trimRecv(mt reflect.Type) string {
	in := make([]reflect.Type, 0, mt.NumIn())
	for i := 1; i < mt.NumIn(); i++ {
		in = append(in, mt.In(i))
	}
	out := make([]reflect.Type, mt.NumOut())
	for i := range out {
		out[i] = mt.Out(i)
	}
	return reflect.FuncOf(in, out, mt.IsVariadic()).String()
}

// hasValidate reports whether a section's type has a Validate method of any kind, which nothing
// would call.
func hasValidate(t reflect.Type) bool {
	if _, ok := t.MethodByName("Validate"); ok {
		return true
	}
	_, ok := reflect.PointerTo(t).MethodByName("Validate")
	return ok
}
