package wire

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// PatternMessage is the sentence of a value that does not match its type's Pattern. It names
// neither the value (ADR 0012: nothing the client sent is reflected) nor the pattern: the
// pattern is public in the API document, where the client reads it, and a message that carried
// it would be as long as the author's regular expression and break the short-message bound.
const PatternMessage = "must match the pattern"

// Pattern is what a string type demands of its text: its Pattern method, read once at
// Registration, checked to be in the subset Go's regexp (RE2) and ECMA-262 (the dialect of JSON
// Schema "pattern") read alike, and compiled once. Matching is unanchored in both, so an author
// who wants the whole value writes ^ and $.
type Pattern struct {
	Src string
	Re  *regexp.Regexp
}

// PatternOf reports the Pattern of t, and whether t has one. The method is read on a named
// string type with the signature func() string, on a zero receiver. A method of that exact
// signature on a type that is not a string is an error (it promises a check nothing would
// make); a method of that name with another signature on a string type is an error too, and on
// any other type it is the domain's own method and is left alone. An empty pattern, one that
// does not compile, and one that uses a construct outside the common subset are errors that
// name the construct (docs/research/pattern-dialect).
func PatternOf(t reflect.Type) (*Pattern, error) {
	if t.Name() == "" {
		return nil, nil
	}
	m, ok := methodOf(t, "Pattern")
	if !ok {
		return nil, nil
	}
	mt := m.Type
	exact := mt.NumIn() == 1 && mt.NumOut() == 1 && mt.Out(0) == reflect.TypeFor[string]()
	if t.Kind() != reflect.String {
		if exact {
			return nil, fmt.Errorf("%s has a Pattern method, but Pattern is read on a string type, and %s is a %s", t, t, t.Kind())
		}
		return nil, nil
	}
	if !exact {
		return nil, fmt.Errorf("%s has a Pattern method, so it carries a pattern, but Pattern must be func() string: rename the method if it means something else", t)
	}
	src := m.Func.Call([]reflect.Value{reflect.New(t)})[0].String()
	if src == "" {
		return nil, fmt.Errorf("%s.Pattern returns an empty string, which every value matches: remove the method or write a pattern", t)
	}
	if why := dialectRefusal(src); why != "" {
		return nil, fmt.Errorf("%s.Pattern %q %s", t, src, why)
	}
	re, err := regexp.Compile(src)
	if err != nil {
		return nil, fmt.Errorf("%s.Pattern %q does not compile: %v", t, src, err)
	}
	return &Pattern{Src: src, Re: re}, nil
}

// dialectRefusal returns why src is outside the subset of RE2 syntax that ECMA-262 (compiled
// with the u flag) reads the same way, or "" when src is inside it. The subset is: literals;
// . excluded; [...] classes of literals, ranges and \d \D \w \W; the escapes \d \D \w \W \b \B
// \t \n \r \f \v \xHH, \p{X} and \P{X} of a general category, and a backslash before one of
// ^$\.*+?()[]{}|/ (and - inside a class); the groups (...), (?:...) and (?<name>...); the
// quantifiers * + ? {n} {n,} {n,m}, each with an optional lazy ?; | and the anchors ^ and $.
func dialectRefusal(src string) string {
	inClass := false
	classStart := -1      // index just after [ or [^ of the open class
	prevClassEsc := false // the last item in the open class was \d \D \w or \W
	atomBefore := false   // a quantifier may follow: the last item is not an assertion or an opening
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inClass {
			switch {
			case c == ']':
				if i == classStart {
					return "has a ] first in a class, which ECMA-262 reads as an empty class: write \\]"
				}
				inClass, atomBefore = false, true
			case c == '[':
				if strings.HasPrefix(src[i:], "[:") {
					return "uses a POSIX class like [[:alpha:]], which ECMA-262 does not have: write the range, as a-zA-Z"
				}
				prevClassEsc = false
			case c == '-':
				if prevClassEsc && i+1 < len(src) && src[i+1] != ']' {
					return "uses a range with \\d, \\D, \\w or \\W as its start, which ECMA-262 refuses"
				}
				if i+2 < len(src) && src[i+1] == '\\' && strings.ContainsRune("dDwW", rune(src[i+2])) {
					return "uses a range with \\d, \\D, \\w or \\W as its end, which ECMA-262 refuses"
				}
				prevClassEsc = false
			case c == '\\':
				n, why := escape(src, i, true)
				if why != "" {
					return why
				}
				prevClassEsc = i+1 < len(src) && strings.ContainsRune("dDwW", rune(src[i+1]))
				i += n - 1
			default:
				prevClassEsc = false
			}
			continue
		}
		switch c {
		case '\\':
			n, why := escape(src, i, false)
			if why != "" {
				return why
			}
			atomBefore = !(src[i+1] == 'b' || src[i+1] == 'B')
			i += n - 1
		case '[':
			inClass, prevClassEsc = true, false
			classStart = i + 1
			if i+1 < len(src) && src[i+1] == '^' {
				i++
				classStart = i + 1
			}
		case ']':
			return "has a ] outside a class, which ECMA-262 refuses: write \\]"
		case '}':
			return "has a } outside a repeat count, which ECMA-262 refuses: write \\}"
		case '{':
			n := repeatLen(src[i:])
			if n == 0 {
				return "has a { that does not begin a repeat count, which ECMA-262 refuses: write \\{"
			}
			if !atomBefore {
				return "repeats an anchor, which ECMA-262 refuses"
			}
			i += n - 1
			i = lazy(src, i)
		case '*', '+', '?':
			if !atomBefore {
				return "repeats an anchor, which ECMA-262 refuses"
			}
			i = lazy(src, i)
		case '(':
			atomBefore = false
			if strings.HasPrefix(src[i:], "(?") {
				switch {
				case strings.HasPrefix(src[i:], "(?:"):
					i += 2
				case strings.HasPrefix(src[i:], "(?<"):
					j := i + 3
					for j < len(src) && (src[j] == '_' || src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z' || j > i+3 && src[j] >= '0' && src[j] <= '9') {
						j++
					}
					if j == i+3 || j >= len(src) || src[j] != '>' {
						return "has a group name outside letters, digits and _"
					}
					i = j
				case strings.HasPrefix(src[i:], "(?P"):
					return "uses a (?P<name>...) group, which ECMA-262 does not have: write (?<name>...) or (...)"
				default:
					return "uses a flag or lookaround group " + groupText(src[i:]) + ", which ECMA-262 does not read as RE2 does: only (?:...) and (?<name>...) are allowed"
				}
			}
		case ')':
			atomBefore = true
		case '^', '$':
			atomBefore = false
		case '.':
			return "uses ., which RE2 reads as any character but \\n and ECMA-262 as any but \\n, \\r, U+2028 and U+2029: write the class, as [^\\n\\r]"
		default:
			atomBefore = true
		}
	}
	return ""
}

func groupText(s string) string {
	end := strings.IndexAny(s, ":)")
	if end < 0 {
		end = len(s)
	}
	if end > 8 {
		end = 8
	}
	return s[:end]
}

// lazy skips the ? that makes the quantifier ending at src[i] lazy, and returns the index of
// the last byte it consumed.
func lazy(src string, i int) int {
	if i+1 < len(src) && src[i+1] == '?' {
		return i + 1
	}
	return i
}

// repeatLen returns the length of a {n}, {n,} or {n,m} at the start of s, or 0.
func repeatLen(s string) int {
	i := 1
	d := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i, d = i+1, d+1
	}
	if d == 0 || i >= len(s) {
		return 0
	}
	if s[i] == ',' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i >= len(s) {
			return 0
		}
	}
	if s[i] != '}' {
		return 0
	}
	return i + 1
}

const syntaxChars = `^$\.*+?()[]{}|/`

// escape checks the escape at src[i] and returns its length, or why it is outside the subset.
func escape(src string, i int, inClass bool) (int, string) {
	if i+1 >= len(src) {
		return 0, "ends with a lone backslash"
	}
	c := src[i+1]
	switch {
	case strings.IndexByte("dDwWtnrfv", c) >= 0:
		return 2, ""
	case c == 'b' || c == 'B':
		if inClass {
			return 0, "uses \\" + string(c) + " in a class, where ECMA-262 means a backspace"
		}
		return 2, ""
	case c == 's' || c == 'S':
		return 0, "uses \\" + string(c) + ", which RE2 reads as [\\t\\n\\f\\r ] and ECMA-262 as that and \\v, U+00A0, U+FEFF and the Unicode spaces: write the class, as [ \\t\\n\\r\\f]"
	case c == 'x':
		if i+3 < len(src) && isHex(src[i+2]) && isHex(src[i+3]) {
			return 4, ""
		}
		return 0, "uses \\x without two hex digits, as \\x{...} which ECMA-262 does not have"
	case c == 'p' || c == 'P':
		if i+2 < len(src) && src[i+2] == '{' {
			if end := strings.IndexByte(src[i+2:], '}'); end > 0 && generalCategory[src[i+3:i+2+end]] {
				return 2 + end + 1, ""
			}
		}
		return 0, "uses \\" + string(c) + " other than \\" + string(c) + "{L} with a general category name: ECMA-262 has no one-letter \\" + string(c) + "L and reads scripts as \\p{Script=Greek}"
	case c == '-' && inClass, strings.IndexByte(syntaxChars, c) >= 0:
		return 2, ""
	case c >= '0' && c <= '9':
		return 0, "uses \\" + string(c) + ", a backreference in ECMA-262 and not in RE2"
	}
	return 0, "uses the escape \\" + string(c) + ", which RE2 and ECMA-262 (with the u flag) do not read alike: only \\d \\D \\w \\W \\b \\B \\t \\n \\r \\f \\v \\xHH, \\p{X} and a backslash before ^$\\.*+?()[]{}|/ are allowed"
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// generalCategory is the names \p{...} may take: the Unicode general categories, whose code
// points Go and ECMA-262 agree on (docs/research/pattern-dialect).
var generalCategory = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range strings.Fields("L Lu Ll Lt Lm Lo M Mn Mc Me N Nd Nl No P Pc Pd Ps Pe Pi Pf Po S Sm Sc Sk So Z Zs Zl Zp C Cc Cf Cs Co") {
		m[n] = true
	}
	return m
}()
