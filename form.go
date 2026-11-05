package bites

import (
	"bytes"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"unsafe"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/wire"
)

// Form is a Body read as application/x-www-form-urlencoded (ADR 0044). T is a struct whose
// fields are decoded exactly like a Query section (ADR 0042): the same field types, repeated
// keys for a list, Opt for a field that may be missing, the constraint types, and wire names
// from the naming policy or WireNames. A mistake is located as body.name.
type Form[T any] struct {
	// Value is the form decoded into T, as a Query section is.
	Value T
}

// maxEchoedKey bounds a client-supplied field name echoed in a location (ADR 0012).
const maxEchoedKey = 64

const (
	msgUnknownField = "unknown field"
	msgUnknownQuery = "unknown query parameter"
	msgFormType     = "the body must be sent as application/x-www-form-urlencoded"
	msgFormFields   = "request body has too many fields"
)

// formBody compiles a Form[T] or Submitted[T] Body: T becomes a text section whose location prefix
// is body.
func (c *compiler) formBody(goPath string, t reflect.Type, off uintptr) (*bodyPlan, error) {
	kind := "Form"
	if isSubmitted(t) {
		kind = "Submitted"
	}
	vt := t.Field(0).Type
	if vt.Kind() != reflect.Struct {
		return nil, refusalf("%s is a %s of %s; a %s wraps a struct whose fields are the form fields", goPath, kind, typeLabel(vt), kind)
	}
	if k, _, _, _ := wire.PresenceOf(vt); k != wire.None {
		return nil, refusalf("%s is a %s of %s; a %s wraps a struct whose fields are the form fields", goPath, kind, typeLabel(vt), kind)
	}
	s, err := c.section(secForm, goPath, vt, off+t.Field(0).Offset)
	if err != nil {
		return nil, err
	}
	s.unknown = c.cfg.unknown
	b := &bodyPlan{kind: bodyForm, off: off, form: s}
	if kind == "Submitted" {
		b.sub, b.subOff = true, t.Field(1).Offset
	}
	return b, nil
}

// readForm reads and decodes a Form body. It answers 415 for another media type, 413 for a body
// over the limit or a form with more than maxFields fields, and collects every field error.
// It reports false when it has already answered.
func (b *bodyPlan) readForm(ex *engine.Exchange, in unsafe.Pointer, errs *jsoncodec.Errors, cfg *routeConfig, maxFields int) bool {
	head := ex.Head()
	if (head.Chunked || head.ContentLength > 0) && !isFormContentType(head) {
		problem(ex, 415, msgFormType)
		return false
	}
	body, err := ex.ReadBody(cfg.maxBody, cfg.bodyTimeout)
	if err != nil {
		var he *h1.Error
		if errors.As(err, &he) {
			problem(ex, he.Status, he.Reason)
		} else {
			problem(ex, 400, "")
		}
		return false
	}
	// Counted before anything is decoded, so a body of a million '&' costs one scan. n separators
	// bound n+1 fields, so the form is over the limit when the separators reach it.
	if maxFields > 0 && bytes.Count(body, ampersand) >= maxFields {
		problem(ex, 413, msgFormFields)
		return false
	}
	if b.sub {
		b.keepSent(in, body)
	}
	decodeForm(b.form, body, in, ex.Slab(), errs)
	return true
}

var ampersand = []byte{'&'}

//go:noinline
func decodeForm(s *textSection, body []byte, in unsafe.Pointer, slab *wire.Slab, errs *jsoncodec.Errors) {
	r := textRun{sec: s, base: unsafe.Add(in, s.off), slab: slab, errs: errs}
	for r.phase = 0; r.phase < 2; r.phase++ {
		if r.phase == 0 && s.lists == 0 {
			continue
		}
		wire.EachQuery(body, r.formPair)
	}
	r.finish()
}

// formPair looks up one key and hands its value to the field. A key the type does not have is an
// error naming it, unless the route accepts unknown members (ADR 0015); it is reported in the
// second pass only, so that a list's count pass does not report it twice.
func (r *textRun) formPair(key, value []byte) bool {
	var idx int
	var ok bool
	var name string
	if bytes.IndexByte(key, '%') < 0 && bytes.IndexByte(key, '+') < 0 {
		if idx, ok = r.sec.lookup(key); !ok && r.phase == 1 {
			name = string(key)
		}
	} else if k, err := r.slab.Unescape(key, true); err == nil {
		idx, ok = r.sec.lookup(unsafe.Slice(unsafe.StringData(k), len(k)))
		name = k
	} else {
		if r.phase == 1 {
			addError(r.errs, "body", msgEscape)
		}
		return true
	}
	switch {
	case ok:
		r.offer(idx, value)
	case r.phase == 1 && !r.sec.unknown:
		addError(r.errs, "body."+clipKey(name), msgUnknownField)
	}
	return true
}

// clipKey cuts a client-supplied name to 64 bytes on a rune boundary.
func clipKey(n string) string {
	if len(n) <= maxEchoedKey {
		return n
	}
	i := maxEchoedKey
	for i > 0 && n[i]&0xC0 == 0x80 {
		i--
	}
	return n[:i]
}

// isFormContentType accepts application/x-www-form-urlencoded, any case, parameters ignored.
func isFormContentType(head *h1.Request) bool {
	for i := range head.Headers {
		h := &head.Headers[i]
		if !equalFold(h.Name, "content-type") {
			continue
		}
		v := h.Value
		if i := bytes.IndexByte(v, ';'); i >= 0 {
			v = v[:i]
		}
		return equalFold(bytes.TrimSpace(v), "application/x-www-form-urlencoded")
	}
	return false
}

// maxListedKeys is how many declared query keys the message of an unknown one lists.
const maxListedKeys = 8

// unknownQueryMessage is the sentence for a query key the section does not name: the plain
// sentence, then the declared key nearest to it when one is close, then the keys the route takes
// (ADR 0042). It runs on the error path only, so the strings it builds cost the success path
// nothing. Only names the route declares are written, and the route's middleware has already run
// by the time a query is decoded, so a client the route's sign-in refuses never sees them.
func (s *textSection) unknownQueryMessage(key string) string {
	if len(s.keys) == 0 {
		return msgUnknownQuery + "; this route takes no query parameters"
	}
	var b strings.Builder
	b.WriteString(msgUnknownQuery)
	if near, ok := nearestKey(key, s.keys); ok {
		b.WriteString(`; did you mean "`)
		b.WriteString(near)
		b.WriteString(`"?`)
	}
	b.WriteString("; this route takes ")
	for i, k := range s.keys {
		if i == maxListedKeys {
			b.WriteString(", and ")
			b.WriteString(strconv.Itoa(len(s.keys) - maxListedKeys))
			b.WriteString(" more")
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k)
	}
	return b.String()
}

// nearestKey is the declared key closest to key, if one is close enough to be a misspelling and
// not another parameter. Case, '_' and '-' are not differences (userId, user_id and user-id are
// one name), and past that a key is near when an edit distance of one (two for a name of eight
// bytes or more, three for sixteen or more) turns one into the other, a swap of two neighbours
// counting as one edit. A tie goes to the key declared first. A declared key that is the sent
// key with its separators removed is always near, however long.
func nearestKey(key string, declared []string) (string, bool) {
	k := foldKey(key)
	best, bestD := "", 1<<30
	for _, d := range declared {
		dk := foldKey(d)
		dist := editDistance(k, dk)
		longer := max(len(k), len(dk))
		limit := 1 + longer/8
		if dist <= limit && dist < bestD {
			best, bestD = d, dist
		}
	}
	return best, bestD != 1<<30
}

// foldKey lower-cases a key and drops '_' and '-'.
func foldKey(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '_' || c == '-':
		case 'A' <= c && c <= 'Z':
			b.WriteByte(c + 'a' - 'A')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// editDistance is the optimal string alignment distance of a and b: insertions, deletions,
// substitutions and the swap of two neighbours, each one edit. The keys are cut to 64 bytes, so
// the table is small.
func editDistance(a, b string) int {
	prev2 := make([]int, len(b)+1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(b)]
}
