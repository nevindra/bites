// Command headernames prints the header name nilo-go derives from a Go field name under the
// rule of ADR 0011 as built (Words in internal/wire/naming.go, copied below because a probe is
// its own module and cannot import internal/) and under the proposed X-prefix rule, against the
// name the header has on the wire. Header names are compared case-insensitively, as HTTP does.
package main

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// words is a copy of wire.Words at 4291b54.
func words(goName string) []string {
	rs := []rune(goName)
	var ws []string
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			ws = append(ws, string(rs[start:end]))
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
	return ws
}

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

func title(w string) string {
	r, n := utf8.DecodeRuneInString(w)
	return string(unicode.ToUpper(r)) + strings.ToLower(w[n:])
}

func join(ws []string) string {
	for i, w := range ws {
		ws[i] = title(w)
	}
	return strings.Join(ws, "-")
}

// built is HeaderName as built.
func built(goName string) string { return join(words(goName)) }

// xprefix is the proposed rule: a leading X followed by an upper-case letter is a word of its own.
func xprefix(goName string) string {
	if len(goName) > 1 && goName[0] == 'X' && unicode.IsUpper(rune(goName[1])) {
		return join(append([]string{"X"}, words(goName[1:])...))
	}
	return built(goName)
}

func main() {
	cases := []struct{ goName, wire string }{
		{"XAPIKey", "X-API-Key"},
		{"XApiKey", "X-Api-Key"},
		{"XRequestID", "X-Request-ID"},
		{"XCSRFToken", "X-CSRF-Token"},
		{"XXSRFToken", "X-XSRF-TOKEN"},
		{"XHTTPMethodOverride", "X-HTTP-Method-Override"},
		{"XForwardedFor", "X-Forwarded-For"},
		{"XRealIP", "X-Real-IP"},
		{"XAmzDate", "X-Amz-Date"},
		{"XHubSignature256", "X-Hub-Signature-256"},
		{"XGitHubEvent", "X-GitHub-Event"},
		{"XSignature", "X-Signature"},
		{"XMLVersion", "XML-Version"},
		{"IfNoneMatch", "If-None-Match"},
		{"ETag", "ETag"},
		{"WWWAuthenticate", "WWW-Authenticate"},
		{"DNT", "DNT"},
		{"CFConnectingIP", "CF-Connecting-IP"},
		{"Authorization", "Authorization"},
	}
	fmt.Printf("| Go field | wire | as built | | X-prefix rule | |\n|---|---|---|---|---|---|\n")
	ok := func(got, want string) string {
		if strings.EqualFold(got, want) {
			return "ok"
		}
		return "**wrong**"
	}
	for _, c := range cases {
		b, x := built(c.goName), xprefix(c.goName)
		fmt.Printf("| `%s` | `%s` | `%s` | %s | `%s` | %s |\n", c.goName, c.wire, b, ok(b, c.wire), x, ok(x, c.wire))
	}
}
