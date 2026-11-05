package wire

import (
	"strings"
	"testing"
	"time"
)

func TestEachCheckedFormatAcceptsItsValuesAndRefusesTheOthers(t *testing.T) {
	long := strings.Repeat("a", 64)
	label63, label64 := strings.Repeat("a", 63), strings.Repeat("a", 64)
	cases := map[string]struct{ ok, bad []string }{
		"email": {
			ok:  []string{"ada@example.com", "a.b+c@sub.example.co", "!#$%&'*+-/=?^_`{|}~@x.io", long + "@a.bc"},
			bad: []string{"", "ada", "@example.com", "ada@", "ada@example", "a@@b.co", "a@b@c.co", ".a@b.co", "a.@b.co", "a..b@b.co", "a b@b.co", `"a"@b.co`, "Ada <a@b.co>", "a@[127.0.0.1]", "a@b.c-", "a@-b.co", "a@b.co.", long + "a@b.co", strings.Repeat("a", 250) + "@b.co"},
		},
		"hostname": {
			ok:  []string{"a", "example.com", "a-b.c1", "1.2.3", label63 + ".com", "A.B"},
			bad: []string{"", ".", "a.", ".a", "a..b", "-a", "a-", "a_b", "a b", "ä.com", label64 + ".com", strings.Repeat(label63+".", 4)},
		},
		"uri": {
			ok:  []string{"https://example.com/a%20b?q=1#f", "mailto:ada@example.com", "urn:isbn:0451450523", "a+b-c.d:x", "HTTP://X"},
			bad: []string{"", "example.com", "/path", "1http://x", "http:", "http", "http://a b", "http://a\x00b", "http://a\tb", "http://x/%zz", "http://x/%2", "http://x/%", "http://ä", "ht tp://x", "http://x/<", `http://x/"`, "://x"},
		},
		"ipv4": {
			ok:  []string{"0.0.0.0", "192.168.0.1", "255.255.255.255"},
			bad: []string{"", "256.1.1.1", "1.2.3", "1.2.3.4.5", "01.2.3.4", "::1", "1.2.3.4%eth0", " 1.2.3.4"},
		},
		"ipv6": {
			ok:  []string{"::1", "::", "2001:db8::1", "fe80::1", "2001:0db8:0000:0000:0000:0000:0000:0001"},
			bad: []string{"", "1.2.3.4", "::ffff:1.2.3.4", "fe80::1%eth0", "2001:db8:::1", "12345::", "g::1", ":1"},
		},
		"date": {
			ok:  []string{"2026-11-03", "2024-02-29", "2000-02-29", "0001-01-01", "9999-12-31"},
			bad: []string{"", "2026-1-03", "2026-13-01", "2026-00-10", "2026-04-31", "2023-02-29", "1900-02-29", "2026-11-00", "2026/11/03", "20261103", "2026-11-03 ", "+026-11-03", "2026-11-3x"},
		},
		"date-time": {
			ok:  []string{"2026-11-03T10:20:30Z", "2026-11-03T10:20:30.5Z", "2026-11-03T23:59:59.123456789+07:00", "2026-11-03T00:00:00-12:30"},
			bad: []string{"", "2026-11-03", "2026-11-03T10:20:30", "2026-11-03 10:20:30Z", "2026-11-03t10:20:30Z", "2026-11-03T10:20:30z", "2026-11-03T24:00:00Z", "2026-11-03T10:60:00Z", "2026-11-03T10:20:60Z", "2026-11-03T10:20:30.Z", "2026-11-03T10:20:30+0700", "2026-11-03T10:20:30+24:00", "2026-02-30T10:20:30Z", "2026-11-03T10:20:30+07:60"},
		},
		"uuid": {
			ok:  []string{"123e4567-e89b-12d3-a456-426614174000", "123E4567-E89B-12D3-A456-426614174000"},
			bad: []string{"", "123e4567e89b12d3a456426614174000", "urn:uuid:123e4567-e89b-12d3-a456-426614174000", "{123e4567-e89b-12d3-a456-426614174000}", "123e4567-e89b-12d3-a456-42661417400", "123e4567-e89b-12d3-a456-42661417400g"},
		},
	}
	for _, name := range CheckedFormats {
		r := FormatRuleNamed(name)
		if r == nil || r.Name != name || r.Message == "" {
			t.Fatalf("%s: no rule", name)
		}
		c := cases[name]
		for _, s := range c.ok {
			if !r.OK(s) {
				t.Errorf("%s refused %q", name, s)
			}
		}
		for _, s := range c.bad {
			if r.OK(s) {
				t.Errorf("%s accepted %q", name, s)
			}
		}
	}
	if FormatRuleNamed("money") != nil || FormatRuleNamed("") != nil {
		t.Error("an unknown format is an annotation, not a rule")
	}
}

// An offset past 23:59 is the one place the check is stricter than time.Parse, as RFC 3339 is.
func TestADateTimeIsAcceptedExactlyWhenTimeParseAcceptsItsRFC3339(t *testing.T) {
	r := FormatRuleNamed("date-time")
	for _, s := range []string{
		"2026-11-03T10:20:30Z", "2026-11-03T10:20:30.5+07:00", "2026-11-03T10:20:30.123456789012Z", "2026-11-03T10:20:30-23:59",
		"2026-11-03T10:20:60Z", "2026-11-03T24:00:00Z", "2026-02-29T10:20:30Z", "2024-02-29T10:20:30Z", "2026-11-03t10:20:30Z", "2026-11-03T10:20:30.Z", "2026-11-03T10:20:30", "2026-11-03T10:20:30+0700",
	} {
		_, err := time.Parse(time.RFC3339Nano, s)
		if got := r.OK(s); got != (err == nil) {
			t.Errorf("%q: checked %v, time.Parse error %v", s, got, err)
		}
	}
}

func TestAPassingValueOfEveryCheckedFormatAllocatesNothing(t *testing.T) {
	samples := map[string]string{
		"email": "ada@example.com", "uri": "https://example.com/a%20b", "hostname": "api.example.com",
		"ipv4": "192.168.0.1", "ipv6": "2001:db8::1", "date": "2026-11-03",
		"date-time": "2026-11-03T10:20:30.5+07:00", "uuid": "123e4567-e89b-12d3-a456-426614174000",
	}
	for _, name := range CheckedFormats {
		r, s := FormatRuleNamed(name), samples[name]
		if !r.OK(s) {
			t.Fatalf("%s refused %q", name, s)
		}
		if n := testing.AllocsPerRun(100, func() { r.OK(s) }); n != 0 {
			t.Errorf("%s: %v allocations a check, want 0", name, n)
		}
	}
}
