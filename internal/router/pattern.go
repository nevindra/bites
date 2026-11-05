package router

import (
	"fmt"
	"strings"
)

type segKind uint8

const (
	segLit    segKind = iota // literal text; "" only for {$}
	segSingle                // {name}: exactly one non-empty segment
	segMulti                 // {name...} or a trailing slash: the rest, possibly empty
)

// seg is one segment of a compiled pattern. For segLit text is the literal,
// for the wildcards it is the name ("" for the anonymous multi that a
// trailing slash means).
type seg struct {
	kind segKind
	text string
}

// parsePattern compiles pattern into segments and the names of the captured
// wildcards, in order. The error is the reason only; Add wraps it with the
// pattern and where it was registered.
func parsePattern(pattern string) (segs []seg, names []string, err error) {
	if pattern == "" || pattern[0] != '/' {
		return nil, nil, fmt.Errorf("must start with '/'")
	}
	rest := pattern[1:]
	if rest == "" {
		return []seg{{kind: segMulti}}, nil, nil
	}
	parts := strings.Split(rest, "/")
	for i, part := range parts {
		last := i == len(parts)-1
		if part == "" {
			if !last {
				return nil, nil, fmt.Errorf("empty segment (\"//\") never matches: such paths are refused as bad")
			}
			segs = append(segs, seg{kind: segMulti}) // trailing slash
			continue
		}
		if part == "." || part == ".." {
			return nil, nil, fmt.Errorf("segment %q never matches: such paths are refused as bad", part)
		}
		if !strings.ContainsAny(part, "{}") {
			segs = append(segs, seg{kind: segLit, text: part})
			continue
		}
		if !balanced(part) {
			return nil, nil, fmt.Errorf("unbalanced braces in segment %q", part)
		}
		if part[0] != '{' || part[len(part)-1] != '}' || strings.Count(part, "{") != 1 {
			return nil, nil, fmt.Errorf("a wildcard must be a whole segment, got %q", part)
		}
		inner := part[1 : len(part)-1]
		switch {
		case inner == "$":
			if !last {
				return nil, nil, fmt.Errorf("\"{$}\" must be the last segment")
			}
			segs = append(segs, seg{kind: segLit})
			continue
		case strings.HasSuffix(inner, "..."):
			if !last {
				return nil, nil, fmt.Errorf("%q must be the last segment", part)
			}
			inner = strings.TrimSuffix(inner, "...")
			if err := checkName(part, inner, names); err != nil {
				return nil, nil, err
			}
			names = append(names, inner)
			segs = append(segs, seg{kind: segMulti, text: inner})
		default:
			if err := checkName(part, inner, names); err != nil {
				return nil, nil, err
			}
			names = append(names, inner)
			segs = append(segs, seg{kind: segSingle, text: inner})
		}
		if len(names) > MaxParams {
			return nil, nil, fmt.Errorf("more than %d wildcards", MaxParams)
		}
	}
	return segs, names, nil
}

func balanced(s string) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
			if depth > 1 {
				return false
			}
		case '}':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

func checkName(part, name string, seen []string) error {
	if name == "" {
		return fmt.Errorf("empty wildcard name in %q", part)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return fmt.Errorf("invalid wildcard name %q in %q: a name is a Go identifier", name, part)
	}
	for _, s := range seen {
		if s == name {
			return fmt.Errorf("duplicate wildcard name %q", name)
		}
	}
	return nil
}

// rel is how the sets of requests two patterns match relate, from the first
// pattern's point of view. ServeMux's vocabulary, so the conflict rule is the
// same one.
type rel uint8

const (
	relDisjoint rel = iota
	relEqual
	relGeneral  // first matches a strict superset of second
	relSpecific // first matches a strict subset of second
	relOverlap  // they share some request, neither contains the other
)

func combine(a, b rel) rel {
	switch {
	case a == relEqual:
		return b
	case b == relEqual, a == b:
		return a
	case a == relDisjoint || b == relDisjoint:
		return relDisjoint
	}
	return relOverlap
}

// compareMethods follows ServeMux: GET also serves HEAD, so GET is more
// general than HEAD, and a method-less pattern is more general than any method.
func compareMethods(a, b string) rel {
	switch {
	case a == b:
		return relEqual
	case a == "":
		return relGeneral
	case b == "":
		return relSpecific
	case a == "GET" && b == "HEAD":
		return relGeneral
	case a == "HEAD" && b == "GET":
		return relSpecific
	}
	return relDisjoint
}

// comparePaths relates the sets of paths two segment lists match and, when
// they share some, returns one path both match (for the conflict message).
func comparePaths(a, b []seg) (rel, string) {
	r := relEqual
	var ex strings.Builder
	for k := 0; ; k++ {
		switch {
		case k >= len(a) && k >= len(b):
			return r, ex.String()
		case k >= len(a) || k >= len(b):
			return relDisjoint, ""
		}
		x, y := a[k], b[k]
		switch {
		case x.kind == segMulti && y.kind == segMulti:
			ex.WriteString("/")
			return r, ex.String()
		case x.kind == segMulti:
			renderRest(&ex, b[k:])
			return combine(r, relGeneral), ex.String()
		case y.kind == segMulti:
			renderRest(&ex, a[k:])
			return combine(r, relSpecific), ex.String()
		case x.kind == segLit && y.kind == segLit:
			if x.text != y.text {
				return relDisjoint, ""
			}
			ex.WriteString("/" + x.text)
		case x.kind == segLit: // y is single
			if x.text == "" {
				return relDisjoint, ""
			}
			r = combine(r, relSpecific)
			ex.WriteString("/" + x.text)
		case y.kind == segLit: // x is single
			if y.text == "" {
				return relDisjoint, ""
			}
			r = combine(r, relGeneral)
			ex.WriteString("/" + y.text)
		default:
			ex.WriteString("/x")
		}
	}
}

func renderRest(ex *strings.Builder, rest []seg) {
	for _, s := range rest {
		switch s.kind {
		case segLit:
			ex.WriteString("/" + s.text)
		case segSingle:
			ex.WriteString("/x")
		default:
			ex.WriteString("/")
			return
		}
	}
}

func validMethod(m string) bool {
	for i := 0; i < len(m); i++ {
		c := m[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0 {
			continue
		}
		return false
	}
	return true
}
