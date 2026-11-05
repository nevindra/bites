// Package router is the route table of the engine: it matches a method and a
// raw request path to a handler, and says why when it cannot (ADR 0018).
//
// The syntax is http.ServeMux's, and so is the rule for two patterns that
// both match a request: the more specific one wins, and two where neither is
// are refused at Add, naming a path both match. The conflict check is
// ServeMux's algorithm (compare methods, compare paths, combine), so a table
// ServeMux accepts is accepted here and picks the same pattern for the same
// request; router_test.go holds that as a cross-check and lists every
// deliberate difference.
//
// What differs, on purpose: matching is on raw segments (%2F stays inside one
// segment, decoding is the caller's); no request is ever redirected, so
// "/users" and "/users/" are two paths and a path that is not normalised
// ("//", "/./", "/../", a trailing "/." or "/..") is BadPath, which the
// caller answers 400; there are no host patterns; and a method-less pattern
// exists only to mount a slow path (ADR 0019).
//
// The table is one segment trie. A literal child is tried before the {name}
// child, which is tried before a rest wildcard, and a branch that dead-ends
// is backtracked, which is what "more specific wins" means once conflicts
// are refused. Methods are not a level of the trie: a leaf holds one entry
// per method, and Lookup runs up to three passes over the same trie (the
// method, then GET for a HEAD, then the method-less mounts), the order
// ServeMux uses, so an explicit HEAD route wins over the GET one and a
// method-specific pattern wins over a mount whatever their paths.
//
// Lookup allocates nothing after Freeze: the walk is recursion over the
// caller's byte slice, the captured values are sub-slices of it, and every
// Allow value is precomputed for each set of methods (at most
// maxMethods distinct ones, so at most 4096 strings).
package router

import (
	"fmt"
	"sort"
	"strings"
)

// MaxParams is the most wildcards one pattern may have.
const MaxParams = 16

// maxMethods bounds the distinct methods in one table so every Allow value
// can be built at Freeze (2^maxMethods strings) and Lookup never allocates.
const maxMethods = 12

// Params holds the raw (still percent-encoded) path segments a match
// captured, in pattern order. The engine owns one per connection and reuses
// it; Lookup fills it without allocating. A {rest...} value is the whole
// remainder after the slash, slashes included.
type Params struct {
	Names  [MaxParams]string // from the pattern, set at Add time (shared, immutable)
	Values [MaxParams][]byte // slices of the path passed to Lookup
	N      int
}

// Result is what Lookup found.
type Result int

const (
	Found Result = iota
	NotFound
	MethodNotAllowed
	BadPath
)

type entry[H any] struct {
	method  string
	mi      int // index into Router.methods, -1 for a method-less mount
	pattern string
	where   string
	segs    []seg
	names   []string
	h       H
}

type lit[H any] struct {
	text string
	node *node[H]
}

type node[H any] struct {
	lits    []lit[H]
	single  *node[H]
	entries []*entry[H] // patterns ending exactly here
	rest    []*entry[H] // patterns whose last segment is a rest wildcard here
}

// Router is the route table, generic over the handler type.
type Router[H any] struct {
	root    *node[H]
	all     []*entry[H]
	methods []string
	frozen  bool
	allow   []string // indexed by a bit set of methods; built at Freeze
}

// New returns an empty table.
func New[H any]() *Router[H] { return &Router[H]{root: &node[H]{}} }

// Add registers pattern for method. method is a token like "GET"; "" means a
// method-less mount (only for the slow path, ADR 0018/0019). where is the
// caller's file:line, used in messages. An error is a Registration mistake;
// the caller turns it into the panic. Add after Freeze panics (ADR 0048).
func (r *Router[H]) Add(method, pattern string, h H, where string) error {
	if r.frozen {
		panic(fmt.Sprintf("route %s registered at %s after the app started serving: routes can be added only before it does", display(method, pattern), where))
	}
	if !validMethod(method) {
		return fmt.Errorf("invalid method %q registered at %s: a method is a token", method, where)
	}
	segs, names, err := parsePattern(pattern)
	if err != nil {
		return fmt.Errorf("invalid pattern %q registered at %s: %v", pattern, where, err)
	}
	e := &entry[H]{method: method, pattern: pattern, where: where, segs: segs, names: names, h: h}
	for _, o := range r.all {
		mr := compareMethods(method, o.method)
		if mr == relDisjoint {
			continue
		}
		pr, ex := comparePaths(segs, o.segs)
		switch combine(mr, pr) {
		case relEqual:
			return fmt.Errorf("pattern %q (registered at %s) conflicts with pattern %q (registered at %s): they match the same requests",
				display(method, pattern), where, display(o.method, o.pattern), o.where)
		case relOverlap:
			return fmt.Errorf("pattern %q (registered at %s) conflicts with pattern %q (registered at %s): both match %q, and neither is more specific",
				display(method, pattern), where, display(o.method, o.pattern), o.where, display(moreSpecificMethod(method, o.method), ex))
		}
	}
	e.mi = -1
	if method != "" {
		for i, m := range r.methods {
			if m == method {
				e.mi = i
			}
		}
		if e.mi < 0 {
			if len(r.methods) == maxMethods {
				return fmt.Errorf("pattern %q registered at %s: more than %d distinct methods in one table", display(method, pattern), where, maxMethods)
			}
			r.methods = append(r.methods, method)
			e.mi = len(r.methods) - 1
		}
	}
	r.all = append(r.all, e)
	n := r.root
	for _, s := range segs {
		switch s.kind {
		case segMulti:
			n.rest = append(n.rest, e)
			return nil
		case segSingle:
			if n.single == nil {
				n.single = &node[H]{}
			}
			n = n.single
		default:
			var next *node[H]
			for _, l := range n.lits {
				if l.text == s.text {
					next = l.node
				}
			}
			if next == nil {
				next = &node[H]{}
				n.lits = append(n.lits, lit[H]{s.text, next})
			}
			n = next
		}
	}
	n.entries = append(n.entries, e)
	return nil
}

// moreSpecificMethod picks the method to show in an example request: the one
// that is not the fallback of the other.
func moreSpecificMethod(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	case a == "GET" && b == "HEAD":
		return b
	}
	return a
}

func display(method, s string) string {
	if method == "" {
		return s
	}
	return method + " " + s
}

// Freeze ends registration and precomputes every Allow value. Call it once
// when the app starts serving; Add after it panics (ADR 0048).
func (r *Router[H]) Freeze() {
	if r.frozen {
		return
	}
	r.frozen = true
	r.allow = make([]string, 1<<len(r.methods))
	for mask := range r.allow {
		r.allow[mask] = r.buildAllow(uint32(mask))
	}
}

// buildAllow is sorted and comma separated, and names HEAD whenever it
// names GET, because a GET route answers HEAD.
func (r *Router[H]) buildAllow(mask uint32) string {
	var names []string
	hasHead := false
	for i, m := range r.methods {
		if mask&(1<<i) != 0 {
			names = append(names, m)
			hasHead = hasHead || m == "HEAD"
		}
	}
	for _, m := range names {
		if m == "GET" && !hasHead {
			names = append(names, "HEAD")
			break
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Lookup matches method and the raw request path. It never allocates once the
// router is frozen. On MethodNotAllowed, allow is the Allow header value. On
// BadPath the path was not normalised and the caller answers 400 (ADR 0018).
// The path is as given, without the query.
func (r *Router[H]) Lookup(method, rawPath []byte, p *Params) (h H, res Result, allow string) {
	p.N = 0
	if len(rawPath) == 0 || rawPath[0] != '/' {
		return h, NotFound, ""
	}
	if !normalised(rawPath) {
		return h, BadPath, ""
	}
	mi := -2 // a method nobody registered matches only mounts
	for i, m := range r.methods {
		if m == string(method) {
			mi = i
		}
	}
	m := matcher[H]{p: p}
	passes := [3]int{mi, -2, -1}
	if string(method) == "HEAD" {
		for i, name := range r.methods {
			if name == "GET" {
				passes[1] = i
			}
		}
	}
	for _, want := range passes {
		if want == -2 {
			continue
		}
		m.want = want
		if e := m.walk(r.root, rawPath, 1, 0); e != nil {
			p.N = len(e.names)
			copy(p.Names[:], e.names)
			return e.h, Found, ""
		}
	}
	m.want = collectAll
	m.walk(r.root, rawPath, 1, 0)
	p.N = 0
	if m.mask == 0 {
		return h, NotFound, ""
	}
	if r.allow != nil {
		return h, MethodNotAllowed, r.allow[m.mask]
	}
	return h, MethodNotAllowed, r.buildAllow(m.mask) // before Freeze only (tests)
}

// normalised reports whether the path has no empty segment but a last one
// (a trailing slash is a path of its own) and no "." or ".." segment.
func normalised(p []byte) bool {
	for s := 1; s <= len(p); {
		e := s
		for e < len(p) && p[e] != '/' {
			e++
		}
		switch e - s {
		case 0:
			if e != len(p) {
				return false
			}
		case 1:
			if p[s] == '.' {
				return false
			}
		case 2:
			if p[s] == '.' && p[s+1] == '.' {
				return false
			}
		}
		s = e + 1
	}
	return true
}

const collectAll = -3

// matcher is the state of one Lookup pass. want is a method index, -1 for
// mounts, or collectAll to gather the methods of every pattern the path
// matches (the 405 pass).
type matcher[H any] struct {
	p    *Params
	want int
	mask uint32
}

// walk matches the segment of path starting at i and below, n being the node
// the segments before it led to.
func (m *matcher[H]) walk(n *node[H], path []byte, i, depth int) *entry[H] {
	j := i
	for j < len(path) && path[j] != '/' {
		j++
	}
	seg := path[i:j]
	last := j == len(path)
	for k := range n.lits {
		if n.lits[k].text == string(seg) {
			if e := m.visit(n.lits[k].node, path, j+1, depth, last); e != nil {
				return e
			}
		}
	}
	if n.single != nil && len(seg) > 0 {
		m.p.Values[depth] = seg
		if e := m.visit(n.single, path, j+1, depth+1, last); e != nil {
			return e
		}
	}
	if len(n.rest) > 0 {
		if depth < MaxParams { // an anonymous rest after 16 wildcards captures nothing
			m.p.Values[depth] = path[i:]
		}
		if e := m.pick(n.rest); e != nil {
			return e
		}
	}
	return nil
}

func (m *matcher[H]) visit(c *node[H], path []byte, next, depth int, last bool) *entry[H] {
	if last {
		return m.pick(c.entries)
	}
	return m.walk(c, path, next, depth)
}

func (m *matcher[H]) pick(es []*entry[H]) *entry[H] {
	for _, e := range es {
		if m.want == collectAll {
			if e.mi >= 0 {
				m.mask |= 1 << e.mi
			}
		} else if e.mi == m.want {
			return e
		}
	}
	return nil
}

// Each calls fn for every pattern added, in the order added: the method ("" for a mount), the
// pattern as written, and its handler. The framework reads it once, when the app starts serving
// (a static route's fallback learns which paths other routes own, ADR 0065).
func (r *Router[H]) Each(fn func(method, pattern string, h H)) {
	for _, e := range r.all {
		fn(e.method, e.pattern, e.h)
	}
}
