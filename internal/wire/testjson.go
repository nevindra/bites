package wire

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// jsonFiller is an Opt or a Null read by encoding/json/v2 outside the app's codec.
type jsonFiller interface {
	fillJSON(dec *jsontext.Decoder) error
}

func (o *Opt[T]) fillJSON(dec *jsontext.Decoder) error {
	var v T
	if err := json.UnmarshalDecode(dec, &v); err != nil {
		return err
	}
	*o = Opt[T]{value: v, set: true}
	return nil
}

func (n *Null[T]) fillJSON(dec *jsontext.Decoder) error {
	if dec.PeekKind() == 'n' {
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		*n = Null[T]{}
		return nil
	}
	var v T
	if err := json.UnmarshalDecode(dec, &v); err != nil {
		return err
	}
	*n = Null[T]{value: v, valid: true}
	return nil
}

// Unmarshalers reads an Opt and a Null with encoding/json/v2 as the app's codec writes them: a
// member that is present sets an Opt, and null is a Null with no value. It is for bitetest, which
// reads an answer into a test's own type without the app's codec; the app never uses it.
var Unmarshalers = json.WithUnmarshalers(json.UnmarshalFromFunc(func(dec *jsontext.Decoder, f jsonFiller) error {
	return f.fillJSON(dec)
}))
