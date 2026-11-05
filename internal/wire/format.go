package wire

import "net/netip"

// FormatRule is a JSON Schema format that the framework also checks (ADR 0021): a string type
// whose Format method names one of these has every value decoded into it checked. A format
// outside this table stays an annotation of the API document. Every check allocates nothing
// when the value passes, and none of them reflects the client's value in its sentence.
type FormatRule struct {
	// Name is the format as the Format method and the API document spell it.
	Name string
	// Message is the sentence of the 400 entry for a value that is not of the format.
	Message string
	// OK reports whether s is of the format.
	OK func(s string) bool
}

// RuleFormat is the rule code of a value that is not of its type's Format. The entry also
// carries a "format" member naming the format.
const RuleFormat = "format"

var formatRules = map[string]*FormatRule{
	"email":     {"email", "must be an email address", isEmail},
	"uri":       {"uri", "must be an absolute URI", isURI},
	"hostname":  {"hostname", "must be a host name", isHostname},
	"ipv4":      {"ipv4", "must be an IPv4 address", isIPv4},
	"ipv6":      {"ipv6", "must be an IPv6 address", isIPv6},
	"date":      {"date", "must be a date (YYYY-MM-DD)", isDate},
	"date-time": {"date-time", "must be a date and time (RFC 3339)", isDateTime},
	"uuid":      {"uuid", "must be a UUID", isUUID},
}

// FormatRuleNamed returns the rule of a format the framework checks, nil for any other name.
func FormatRuleNamed(name string) *FormatRule { return formatRules[name] }

// CheckedFormats lists the format names the framework checks, in the order the documents state them.
var CheckedFormats = []string{"email", "uri", "hostname", "ipv4", "ipv6", "date", "date-time", "uuid"}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// isHostname: RFC 1123 labels of letters, digits and '-', 1 to 63 bytes, not starting or ending
// with '-', at most 253 bytes in all. A trailing dot is refused.
func isHostname(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != '.' {
			c := s[i]
			if !isAlnum(c) && c != '-' {
				return false
			}
			continue
		}
		n := i - start
		if n < 1 || n > 63 || s[start] == '-' || s[i-1] == '-' {
			return false
		}
		start = i + 1
	}
	return true
}

func isAtext(c byte) bool {
	if isAlnum(c) {
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '/', '=', '?', '^', '_', '`', '{', '|', '}', '~':
		return true
	}
	return false
}

// isEmail: one '@', a local part of 1 to 64 bytes of atext and dots (no leading, trailing or
// doubled dot), a domain that is a host name of at least two labels, 254 bytes at most.
func isEmail(s string) bool {
	if len(s) > 254 {
		return false
	}
	at := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '@' {
			if at >= 0 {
				return false
			}
			at = i
		}
	}
	if at < 1 || at > 64 {
		return false
	}
	local, domain := s[:at], s[at+1:]
	if local[0] == '.' || local[len(local)-1] == '.' {
		return false
	}
	for i := 0; i < len(local); i++ {
		c := local[i]
		if c == '.' {
			if local[i-1] == '.' {
				return false
			}
		} else if !isAtext(c) {
			return false
		}
	}
	dot := false
	for i := 0; i < len(domain); i++ {
		if domain[i] == '.' {
			dot = true
		}
	}
	return dot && isHostname(domain)
}

// isURI: an absolute URI per RFC 3986: a scheme, ':', and a non-empty rest of printable ASCII
// without the characters RFC 3986 never allows unescaped (space, controls, non-ASCII and
// "<>\^`{|}) and with only %HH escapes.
func isURI(s string) bool {
	if len(s) == 0 || !(s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z') {
		return false
	}
	i := 1
	for ; i < len(s); i++ {
		c := s[i]
		if c == ':' {
			break
		}
		if !isAlnum(c) && c != '+' && c != '-' && c != '.' {
			return false
		}
	}
	if i >= len(s) || i == len(s)-1 {
		return false
	}
	rest := s[i+1:]
	for j := 0; j < len(rest); j++ {
		c := rest[j]
		switch {
		case c <= ' ' || c >= 0x7f:
			return false
		case c == '"' || c == '<' || c == '>' || c == '\\' || c == '^' || c == '`' || c == '{' || c == '|' || c == '}':
			return false
		case c == '%':
			if j+2 >= len(rest) || !isHex(rest[j+1]) || !isHex(rest[j+2]) {
				return false
			}
			j += 2
		}
	}
	return true
}

// isIPv4: netip.ParseAddr, which allocates nothing for a plain address, and an IPv4 one.
func isIPv4(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is4()
}

// isIPv6: netip.ParseAddr, an IPv6 address, with no zone (a zone allocates, and a zone names an
// interface of the sender's host, which no field of an API means), and not an IPv4 address
// written in dotted form after "::ffff:".
func isIPv6(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			return false
		}
	}
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is6() {
		return false
	}
	if a.Is4In6() {
		for i := 0; i < len(s); i++ {
			if s[i] == '.' {
				return false
			}
		}
	}
	return true
}

// isDate: YYYY-MM-DD, a day that exists in the proleptic Gregorian calendar.
func isDate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	y, ok1 := digits(s[:4])
	m, ok2 := digits(s[5:7])
	d, ok3 := digits(s[8:])
	if !ok1 || !ok2 || !ok3 || m < 1 || m > 12 || d < 1 {
		return false
	}
	days := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	max := days[m-1]
	if m == 2 && y%4 == 0 && (y%100 != 0 || y%400 == 0) {
		max = 29
	}
	return d <= max
}

// isDateTime: RFC 3339 as time.Parse reads it (a capital T and Z, seconds 00 to 59, an offset or
// Z), checked by hand because time.Parse allocates a zone for an offset.
func isDateTime(s string) bool {
	if len(s) < 20 || s[10] != 'T' || !isDate(s[:10]) || s[13] != ':' || s[16] != ':' {
		return false
	}
	h, ok1 := digits(s[11:13])
	mi, ok2 := digits(s[14:16])
	sec, ok3 := digits(s[17:19])
	if !ok1 || !ok2 || !ok3 || h > 23 || mi > 59 || sec > 59 {
		return false
	}
	i := 19
	if s[i] == '.' {
		i++
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	rest := s[i:]
	if rest == "Z" {
		return true
	}
	if len(rest) != 6 || (rest[0] != '+' && rest[0] != '-') || rest[3] != ':' {
		return false
	}
	oh, ok1 := digits(rest[1:3])
	om, ok2 := digits(rest[4:])
	return ok1 && ok2 && oh <= 23 && om <= 59
}

// isUUID: the canonical 8-4-4-4-12 form of hex digits, either case.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if s[i] != '-' {
				return false
			}
		} else if !isHex(s[i]) {
			return false
		}
	}
	return true
}
