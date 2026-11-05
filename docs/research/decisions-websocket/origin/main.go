// Probe for R8: a candidate Origin comparison (authority of the Origin against the request Host)
// run over the cases a deployment meets. Run: go run ./origin
package main

import (
	"fmt"
	"strings"
)

// sameOrigin: origin is the Origin header (one value), host the request's Host, tls whether the
// connection under the engine is TLS. Authority text is compared without case; a port that is the
// default of the Origin's scheme is dropped from the Origin, and ":80" / ":443" is dropped from
// Host only when it is the default of the Origin's own scheme. No scheme is inferred from the
// connection (behind a TLS-terminating proxy the engine sees plain HTTP).
func sameOrigin(origin, host string) bool {
	scheme, rest, ok := strings.Cut(origin, "://")
	if !ok || (scheme != "http" && scheme != "https") || rest == "" || strings.ContainsAny(rest, "/?#@ ") {
		return false // "null", no scheme, a path, userinfo
	}
	def := map[string]string{"http": ":80", "https": ":443"}[scheme]
	return strings.EqualFold(strings.TrimSuffix(rest, def), strings.TrimSuffix(host, def))
}

func main() {
	cases := []struct{ origin, host string }{
		{"https://example.com", "example.com"},
		{"https://example.com", "EXAMPLE.com"},
		{"https://example.com:443", "example.com"},
		{"https://example.com", "example.com:443"},
		{"http://example.com:8080", "example.com:8080"},
		{"http://example.com:8080", "example.com"},
		{"http://localhost:3000", "localhost:8080"}, // dev page on another port: refused
		{"null", "example.com"},
		{"https://example.com/", "example.com"},
		{"https://evil.com", "example.com"},
		{"https://example.com.evil.com", "example.com"},
		{"https://user@example.com", "example.com"},
		{"https://[::1]:8443", "[::1]:8443"},
		{"ws://example.com", "example.com"},
		{"http://example.com", "example.com:443"}, // port 443 is not http's default: refused
	}
	for _, c := range cases {
		fmt.Printf("%-34q host %-20q -> %v\n", c.origin, c.host, sameOrigin(c.origin, c.host))
	}
}
