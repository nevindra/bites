package site

import (
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// This file renders the small Markdown subset the documentation is written in (ADR 0073):
// ATX headings, paragraphs, fenced code, flat and nested lists, tables, block quotes, rules,
// code spans, emphasis, links, images and autolinks. A one-line HTML comment is dropped. Raw HTML
// is escaped, never passed through, so a page cannot inject markup. Everything else is text.

// linkFunc rewrites a link target; ok false marks it unresolved, and the text then renders as a
// plain span carrying the target in a title.
type linkFunc func(href string) (to string, ok bool)

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	fenceRe   = regexp.MustCompile("^(```+|~~~+)\\s*([A-Za-z0-9_+-]*)")
	listRe    = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	ruleRe    = regexp.MustCompile(`^\s{0,3}([-*_])(\s*[-*_]){2,}\s*$`)
	tableSep  = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)
)

// Slug is the anchor a heading gets: lower case, punctuation other than '-' and '_' dropped,
// spaces turned into '-' (the rule docs_test.go holds the links to).
func Slug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// plain removes the inline markup of a heading, so its slug and its entry in a listing read as
// the text does.
func plain(s string) string {
	s = strings.NewReplacer("`", "", "**", "").Replace(s)
	return linkRe.ReplaceAllString(s, "$1")
}

var linkRe = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// Heading is one heading of a page.
type Heading struct {
	Level int
	Text  string // without markup
	ID    string
}

// Headings lists the headings of a document with the anchors they get, duplicates numbered as
// GitHub does.
func Headings(md string) []Heading {
	var out []Heading
	seen := map[string]int{}
	inFence := ""
	for _, l := range strings.Split(md, "\n") {
		if m := fenceRe.FindStringSubmatch(l); m != nil {
			switch {
			case inFence == "":
				inFence = m[1][:3]
			case strings.HasPrefix(m[1], inFence):
				inFence = ""
			}
			continue
		}
		if inFence != "" {
			continue
		}
		if m := headingRe.FindStringSubmatch(l); m != nil {
			t := plain(m[2])
			id := Slug(t)
			if k := seen[id]; k > 0 {
				seen[id]++
				id += "-" + strconv.Itoa(k)
			} else {
				seen[id] = 1
			}
			out = append(out, Heading{len(m[1]), t, id})
		}
	}
	return out
}

// Render turns Markdown into HTML. Link targets go through link.
func Render(md string, link linkFunc) string {
	r := &renderer{link: link, seen: map[string]int{}}
	r.blocks(strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n"))
	return r.b.String()
}

type renderer struct {
	b    strings.Builder
	link linkFunc
	seen map[string]int
}

func (r *renderer) blocks(lines []string) {
	for i := 0; i < len(lines); {
		l := lines[i]
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			i++
		case strings.HasPrefix(t, "<!--") && strings.HasSuffix(t, "-->"):
			i++
		case fenceRe.MatchString(l):
			m := fenceRe.FindStringSubmatch(l)
			j := i + 1
			for j < len(lines) && !(strings.HasPrefix(strings.TrimSpace(lines[j]), m[1][:3]) && strings.Trim(strings.TrimSpace(lines[j]), m[1][:1]) == "") {
				j++
			}
			cls := ""
			if m[2] != "" {
				cls = ` class="language-` + html.EscapeString(m[2]) + `"`
			}
			r.b.WriteString("<pre><code" + cls + ">" + html.EscapeString(strings.Join(lines[i+1:min(j, len(lines))], "\n")) + "\n</code></pre>\n")
			i = j + 1
		case headingRe.MatchString(l):
			m := headingRe.FindStringSubmatch(l)
			id := Slug(plain(m[2]))
			if k := r.seen[id]; k > 0 {
				r.seen[id]++
				id += "-" + strconv.Itoa(k)
			} else {
				r.seen[id] = 1
			}
			n := strconv.Itoa(len(m[1]))
			r.b.WriteString("<h" + n + ` id="` + html.EscapeString(id) + `">` + r.inline(m[2]) + `<a class="anchor" href="#` + html.EscapeString(id) + `" aria-label="link to this section">#</a></h` + n + ">\n")
			i++
		case ruleRe.MatchString(l) && !listRe.MatchString(l):
			r.b.WriteString("<hr>\n")
			i++
		case strings.HasPrefix(t, ">"):
			j := i
			var q []string
			for j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), ">") {
				q = append(q, strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(lines[j]), ">"), " "))
				j++
			}
			r.b.WriteString("<blockquote>\n")
			r.blocks(q)
			r.b.WriteString("</blockquote>\n")
			i = j
		case strings.HasPrefix(t, "|") && i+1 < len(lines) && tableSep.MatchString(lines[i+1]) && strings.Contains(lines[i+1], "-"):
			i = r.table(lines, i)
		case listRe.MatchString(l):
			i = r.list(lines, i)
		default:
			j := i
			var p []string
			for j < len(lines) && strings.TrimSpace(lines[j]) != "" && !r.startsBlock(lines[j], j > i) {
				p = append(p, strings.TrimSpace(lines[j]))
				j++
			}
			if j == i {
				j++
				p = append(p, t)
			}
			r.b.WriteString("<p>" + r.inline(strings.Join(p, " ")) + "</p>\n")
			i = j
		}
	}
}

// startsBlock reports whether a line ends the paragraph above it.
func (r *renderer) startsBlock(l string, inPara bool) bool {
	t := strings.TrimSpace(l)
	return fenceRe.MatchString(l) || headingRe.MatchString(l) || strings.HasPrefix(t, ">") || listRe.MatchString(l) && inPara || strings.HasPrefix(t, "<!--")
}

func (r *renderer) list(lines []string, i int) int {
	base := indentOf(lines[i])
	ordered := listRe.FindStringSubmatch(lines[i])[2][0] >= '0' && listRe.FindStringSubmatch(lines[i])[2][0] <= '9'
	tag := "ul"
	if ordered {
		tag = "ol"
	}
	r.b.WriteString("<" + tag + ">\n")
	for i < len(lines) {
		m := listRe.FindStringSubmatch(lines[i])
		if m == nil || indentOf(lines[i]) != base {
			break
		}
		item := []string{m[3]}
		i++
		var nested []string
		for i < len(lines) {
			l := lines[i]
			if strings.TrimSpace(l) == "" {
				// a blank line continues the item only if an indented line follows
				if i+1 < len(lines) && indentOf(lines[i+1]) > base && strings.TrimSpace(lines[i+1]) != "" {
					i++
					continue
				}
				break
			}
			if indentOf(l) <= base {
				break
			}
			if listRe.MatchString(l) || len(nested) > 0 {
				nested = append(nested, l)
			} else {
				item = append(item, strings.TrimSpace(l))
			}
			i++
		}
		r.b.WriteString("<li>" + r.inline(strings.Join(item, " ")))
		if len(nested) > 0 {
			r.b.WriteString("\n")
			r.blocks(nested)
		}
		r.b.WriteString("</li>\n")
	}
	r.b.WriteString("</" + tag + ">\n")
	return i
}

func indentOf(l string) int {
	n := 0
	for _, c := range l {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

func (r *renderer) table(lines []string, i int) int {
	head := splitRow(lines[i])
	align := splitRow(lines[i+1])
	r.b.WriteString("<table>\n<thead><tr>")
	for k, c := range head {
		r.b.WriteString("<th" + alignAttr(align, k) + ">" + r.inline(c) + "</th>")
	}
	r.b.WriteString("</tr></thead>\n<tbody>\n")
	i += 2
	for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|") {
		r.b.WriteString("<tr>")
		for k, c := range splitRow(lines[i]) {
			r.b.WriteString("<td" + alignAttr(align, k) + ">" + r.inline(c) + "</td>")
		}
		r.b.WriteString("</tr>\n")
		i++
	}
	r.b.WriteString("</tbody>\n</table>\n")
	return i
}

func alignAttr(align []string, k int) string {
	if k >= len(align) {
		return ""
	}
	a := strings.TrimSpace(align[k])
	switch {
	case strings.HasPrefix(a, ":") && strings.HasSuffix(a, ":"):
		return ` style="text-align:center"`
	case strings.HasSuffix(a, ":"):
		return ` style="text-align:right"`
	}
	return ""
}

// splitRow splits a table row on the bars that are not in a code span and not escaped.
func splitRow(l string) []string {
	l = strings.TrimSpace(l)
	l = strings.TrimPrefix(l, "|")
	var cells []string
	var cur strings.Builder
	inCode := false
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch {
		case c == '\\' && i+1 < len(l) && l[i+1] == '|':
			cur.WriteByte('|')
			i++
		case c == '`':
			inCode = !inCode
			cur.WriteByte(c)
		case c == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		cells = append(cells, s)
	}
	return cells
}

// inline renders the span-level markup of s.
func (r *renderer) inline(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '`':
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			fence := strings.Repeat("`", n)
			if end := strings.Index(s[i+n:], fence); end >= 0 {
				code := s[i+n : i+n+end]
				if len(code) > 1 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.TrimSpace(code) != "" {
					code = code[1 : len(code)-1]
				}
				b.WriteString("<code>" + html.EscapeString(code) + "</code>")
				i += n + end + n
				continue
			}
			b.WriteString(fence)
			i += n
		case c == '\\' && i+1 < len(s) && strings.IndexByte("\\`*_{}[]()#+-.!|<>~", s[i+1]) >= 0:
			b.WriteString(html.EscapeString(s[i+1 : i+2]))
			i += 2
		case c == '!' && i+1 < len(s) && s[i+1] == '[':
			if text, href, n, ok := linkAt(s, i+1); ok {
				to, _ := r.link(href)
				b.WriteString(`<img src="` + html.EscapeString(to) + `" alt="` + html.EscapeString(plain(text)) + `">`)
				i += 1 + n
				continue
			}
			b.WriteByte('!')
			i++
		case c == '[':
			if text, href, n, ok := linkAt(s, i); ok {
				if to, found := r.link(href); found {
					b.WriteString(`<a href="` + html.EscapeString(to) + `">` + r.inline(text) + "</a>")
				} else {
					b.WriteString(`<span class="unlinked" title="` + html.EscapeString(href) + `">` + r.inline(text) + "</span>")
				}
				i += n
				continue
			}
			b.WriteByte('[')
			i++
		case c == '<' && (strings.HasPrefix(s[i:], "<http://") || strings.HasPrefix(s[i:], "<https://")):
			if end := strings.IndexByte(s[i:], '>'); end > 0 {
				u := s[i+1 : i+end]
				b.WriteString(`<a href="` + html.EscapeString(u) + `">` + html.EscapeString(u) + "</a>")
				i += end + 1
				continue
			}
			b.WriteString("&lt;")
			i++
		case c == '*' && strings.HasPrefix(s[i:], "**"):
			if end := strings.Index(s[i+2:], "**"); end > 0 {
				b.WriteString("<strong>" + r.inline(s[i+2:i+2+end]) + "</strong>")
				i += end + 4
				continue
			}
			b.WriteString("**")
			i += 2
		case c == '*' || c == '_':
			if end := emphasisEnd(s, i); end > 0 {
				b.WriteString("<em>" + r.inline(s[i+1:end]) + "</em>")
				i = end + 1
				continue
			}
			b.WriteByte(c)
			i++
		default:
			if c < 0x80 {
				switch c {
				case '<':
					b.WriteString("&lt;")
				case '>':
					b.WriteString("&gt;")
				case '&':
					b.WriteString("&amp;")
				case '"':
					b.WriteString("&#34;")
				default:
					b.WriteByte(c)
				}
			} else {
				b.WriteByte(c)
			}
			i++
		}
	}
	return b.String()
}

// emphasisEnd returns the index of the delimiter that closes the emphasis opened at s[i], or -1.
// An underscore only opens at a word start and closes at a word end, so snake_case stays text.
func emphasisEnd(s string, i int) int {
	d := s[i]
	if i+1 >= len(s) || s[i+1] == ' ' || s[i+1] == d {
		return -1
	}
	if d == '_' && i > 0 && isWord(s[i-1]) {
		return -1
	}
	for j := i + 1; j < len(s); j++ {
		switch {
		case s[j] == '`': // a code span inside: skip to its end
			if e := strings.IndexByte(s[j+1:], '`'); e >= 0 {
				j += e + 1
			}
		case s[j] == d && s[j-1] != ' ':
			if d == '_' && j+1 < len(s) && isWord(s[j+1]) {
				continue
			}
			return j
		}
	}
	return -1
}

func isWord(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// linkAt parses [text](href) at s[i] and returns the text, the target and the bytes consumed.
func linkAt(s string, i int) (text, href string, n int, ok bool) {
	depth := 0
	j := i
	for ; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '`':
			if e := strings.IndexByte(s[j+1:], '`'); e >= 0 {
				j += e + 1
			}
		case '[':
			depth++
		case ']':
			depth--
		}
		if depth == 0 {
			break
		}
	}
	if j >= len(s) || s[j] != ']' || j+1 >= len(s) || s[j+1] != '(' {
		return "", "", 0, false
	}
	k, d := j+2, 1
	for ; k < len(s) && d > 0; k++ {
		switch s[k] {
		case '(':
			d++
		case ')':
			d--
		}
	}
	if d != 0 {
		return "", "", 0, false
	}
	href = strings.TrimSpace(s[j+2 : k-1])
	if sp := strings.IndexByte(href, ' '); sp > 0 { // a title
		href = href[:sp]
	}
	return s[i+1 : j], strings.Trim(href, "<>"), k - i, true
}

// linkTargetRe finds the target of each inline link.
var linkTargetRe = regexp.MustCompile(`\]\(([^)\s]+)`)
