package wire

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Policy turns a Go field name into a wire name (ADR 0011). One is chosen per application.
type Policy func(goName string) string

var (
	_ Policy = SnakeCase
	_ Policy = CamelCase
)

// Words splits a Go name into words. A run of capitals is one word, and its last capital
// starts the next word when a lower-case letter follows, unless that letter is a lone plural
// 's' (UserIDs is User IDs); digits stay with the word before
// them; an underscore ends a word and is dropped. The words keep their case.
//
//	ItemID -> Item ID    HTTPStatus -> HTTP Status    OAuth2Token -> O Auth2 Token
//	Page2Size -> Page2 Size
//
// Only the boundaries are decided here, so the policies below differ in case alone. It runs at
// Registration, so it favours clarity over allocation.
func Words(goName string) []string {
	rs := []rune(goName)
	var words []string
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			words = append(words, string(rs[start:end]))
		}
		start = -1
	}
	for i, r := range rs {
		if r == '_' {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		if !unicode.IsUpper(r) {
			continue
		}
		prev := rs[i-1]
		boundary := unicode.IsLower(prev) || unicode.IsDigit(prev) ||
			(unicode.IsUpper(prev) && i+1 < len(rs) && unicode.IsLower(rs[i+1]) && !pluralS(rs, i+1))
		if boundary {
			flush(i)
			start = i
		}
	}
	flush(len(rs))
	return words
}

// pluralS reports whether rs[i] is a lone 's' closing a run of capitals: the plural of an
// initialism (IDs, URLs), which stays with it. Without this the stated rule alone splits
// UserIDs into User, I, Ds, because the D is followed by a lower-case letter. The 's' is
// lone when the name ends there or a new word begins right after it.
func pluralS(rs []rune, i int) bool {
	if rs[i] != 's' {
		return false
	}
	if i+1 == len(rs) {
		return true
	}
	n := rs[i+1]
	return unicode.IsUpper(n) || unicode.IsDigit(n) || n == '_'
}

// SnakeCase is the default Policy: ItemID is item_id.
func SnakeCase(goName string) string {
	return strings.ToLower(strings.Join(Words(goName), "_"))
}

// CamelCase is the lowerCamel Policy: ItemID is itemId, HTTPStatus is httpStatus.
func CamelCase(goName string) string {
	var b strings.Builder
	for i, w := range Words(goName) {
		if i == 0 {
			b.WriteString(strings.ToLower(w))
		} else {
			b.WriteString(title(w))
		}
	}
	return b.String()
}

// HeaderName gives a header field's canonical kebab name: RequestID is Request-Id and
// IfNoneMatch is If-None-Match. Headers are not under the Policy (ADR 0011). A leading X
// followed by an upper-case letter is a word of its own, so XAPIKey is X-Api-Key and not
// Xapi-Key; this holds for header names only.
func HeaderName(goName string) string {
	var ws []string
	if len(goName) > 1 && goName[0] == 'X' && unicode.IsUpper(rune(goName[1])) {
		ws = append([]string{"X"}, Words(goName[1:])...)
	} else {
		ws = Words(goName)
	}
	for i, w := range ws {
		ws[i] = title(w)
	}
	return strings.Join(ws, "-")
}

// title upper-cases the first rune and lower-cases the rest.
func title(w string) string {
	r, n := utf8.DecodeRuneInString(w)
	return string(unicode.ToUpper(r)) + strings.ToLower(w[n:])
}
