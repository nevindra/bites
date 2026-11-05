// Package testcodec is the door bitetest uses to write JSON as an App writes it, with the App's
// naming policy, formats and codec (ADR 0074). bites sets Encode when it is imported; bitetest
// calls it to send a test's value to the App as a client of that App would write it. It is
// internal so that no application reaches the App's codec through it.
package testcodec

// Encode writes v as JSON as app writes it, or returns why app's codec cannot write v's type. app
// is a *bites.App; the type is hidden here because this package cannot import it.
var Encode func(app any, v any) ([]byte, error)

// Decode reads b into v, a non-nil pointer, as a client of app reads what app wrote: with app's
// naming policy, formats and codec, so an Opt, a Null and a tagged union fill. handled is false
// when app's codec has no plan for v's type (a map[string]any, an any, a type with a channel),
// and nothing was read; the caller then decodes with encoding/json. app is a *bites.App.
var Decode func(app any, b []byte, v any) (handled bool, err error)
