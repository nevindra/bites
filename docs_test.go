package bites_test

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// The documentation is held by these tests (ADR 0050, ADR 0033): every page of the guide, the
// reference and the design pages opens the same way, prose is one paragraph a line with no em
// dash, every relative link and anchor resolves, the map links every page, every ADR is joined to
// a design page, every exported name has its reference heading, and every Go block marked
// "compiles" is compiled. Run with -update to rewrite the reference's list of headings.

// layers are the three folders of the documentation, in the order line 5 of a page names them.
var layers = []struct{ dir, label string }{
	{"docs/guide", "Guide"},
	{"docs/reference", "Reference"},
	{"docs/design", "Design"},
}

// mdPage is a Markdown file read once.
type mdPage struct {
	path  string // relative to the repository root, with forward slashes
	lines []string
}

func readPage(t *testing.T, path string) mdPage {
	t.Helper()
	b, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	return mdPage{path: path, lines: strings.Split(strings.TrimRight(string(b), "\n"), "\n")}
}

// layerPages lists the pages of the three layers, README files excluded: a folder's README is
// its index, not a page of a topic.
func layerPages(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, l := range layers {
		files, _ := filepath.Glob(filepath.Join(filepath.FromSlash(l.dir), "*.md"))
		for _, f := range files {
			if filepath.Base(f) != "README.md" {
				out = append(out, filepath.ToSlash(f))
			}
		}
	}
	return out
}

// proseFiles are the Markdown files held to one paragraph a line, no em dash, and resolving links.
func proseFiles(t *testing.T) []string {
	t.Helper()
	out := []string{"README.md", "GLOSSARY.md", "CLAUDE.md", "docs/README.md", "docs/roadmap.md"}
	for _, g := range []string{"docs/adr/*.md", "docs/guide/*.md", "docs/reference/*.md", "docs/design/*.md"} {
		files, _ := filepath.Glob(filepath.FromSlash(g))
		for _, f := range files {
			out = append(out, filepath.ToSlash(f))
		}
	}
	var have []string
	for _, f := range out {
		if _, err := os.Stat(filepath.FromSlash(f)); err == nil {
			have = append(have, f)
		}
	}
	return have
}

// eachProseLine calls fn for every line outside a fenced code block, with its 1-based number.
func eachProseLine(p mdPage, fn func(n int, line string)) {
	fence := ""
	for i, l := range p.lines {
		trim := strings.TrimLeft(l, " ")
		if fence == "" && (strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~")) {
			fence = trim[:3]
			continue
		}
		if fence != "" {
			if strings.HasPrefix(trim, fence) {
				fence = ""
			}
			continue
		}
		fn(i+1, l)
	}
}

func TestEveryDocPageOpensWithItsTitleWhatItIsAndItsOtherLayers(t *testing.T) {
	pages := layerPages(t)
	if len(pages) == 0 {
		t.Fatal("no pages under docs/guide, docs/reference or docs/design")
	}
	for _, path := range pages {
		t.Run(path, func(t *testing.T) {
			p := readPage(t, path)
			line := func(n int) string {
				if n <= len(p.lines) {
					return p.lines[n-1]
				}
				return ""
			}
			if !strings.HasPrefix(line(1), "# ") {
				t.Errorf("%s:1: the first line is the title, \"# ...\"; got %q", path, line(1))
			}
			if line(2) != "" || line(4) != "" {
				t.Errorf("%s: lines 2 and 4 are empty", path)
			}
			if l := line(3); !strings.HasPrefix(l, "**") || !strings.HasSuffix(l, "**") || strings.Count(l, "**") != 2 {
				t.Errorf("%s:3: line 3 is one bold sentence saying what the page is about; got %q", path, l)
			}
			own := ""
			for _, l := range layers {
				if strings.HasPrefix(path, l.dir+"/") {
					own = l.label
				}
			}
			l5 := line(5)
			var want []string
			for _, l := range layers {
				if l.label != own {
					want = append(want, "**"+l.label+":**")
				}
			}
			if !strings.HasPrefix(l5, want[0]) || !strings.Contains(l5, want[1]) {
				t.Errorf("%s:5: line 5 links the same topic in the other two layers, starting %s and naming %s (a link, or \"none\"); got %q", path, want[0], want[1], l5)
			}
		})
	}
}

// startsBlock reports whether a line starts something of its own (a heading, a list item, a
// table row, a quote, a comment, a fence, a glossary term or its _Avoid_ line) rather than
// continuing the paragraph above it.
var startsBlock = regexp.MustCompile(`^\s*(#|[-*+] |\d+[.)] |\||>|<!--|<|` + "```" + `|~~~|\[\^|\*\*[^*]+:\*\*|\*\*[^*]+\*\*:\s*$|_[A-Za-z]+_:)`)

func TestProseIsOneParagraphALineWithNoEmDash(t *testing.T) {
	for _, path := range proseFiles(t) {
		t.Run(path, func(t *testing.T) {
			p := readPage(t, path)
			prev, prevN := "", 0
			eachProseLine(p, func(n int, l string) {
				if strings.ContainsRune(l, '—') {
					t.Errorf("%s:%d: an em dash; use a comma, a colon, a full stop or parentheses", path, n)
				}
				if prevN == n-1 && strings.TrimSpace(prev) != "" && strings.TrimSpace(l) != "" &&
					!startsBlock.MatchString(prev) && !startsBlock.MatchString(l) &&
					!strings.HasPrefix(l, " ") && !strings.HasSuffix(prev, "  ") {
					t.Errorf("%s:%d: a paragraph continues on a second line; prose is one paragraph a line", path, n)
				}
				prev, prevN = l, n
			})
		})
	}
}

// slug is the anchor a heading gets on GitHub: lower case, punctuation other than '-' and '_'
// dropped, spaces turned into '-'.
func slug(heading string) string {
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

var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)

// anchors are the anchors a Markdown file's headings produce, duplicates numbered as GitHub does.
func anchors(t *testing.T, path string) map[string]bool {
	p := readPage(t, path)
	out := map[string]bool{}
	seen := map[string]int{}
	eachProseLine(p, func(_ int, l string) {
		m := headingRe.FindStringSubmatch(l)
		if m == nil {
			return
		}
		s := slug(m[2])
		if k := seen[s]; k > 0 {
			out[s+"-"+strconv.Itoa(k)] = true
		} else {
			out[s] = true
		}
		seen[s]++
	})
	return out
}

var linkRe = regexp.MustCompile(`\]\(([^)\s]+)\)`)

func TestEveryRelativeLinkAndAnchorInTheDocsResolves(t *testing.T) {
	cache := map[string]map[string]bool{}
	anchorsOf := func(path string) map[string]bool {
		if a, ok := cache[path]; ok {
			return a
		}
		a := anchors(t, path)
		cache[path] = a
		return a
	}
	for _, path := range proseFiles(t) {
		t.Run(path, func(t *testing.T) {
			p := readPage(t, path)
			eachProseLine(p, func(n int, l string) {
				l = regexp.MustCompile("`[^`]*`").ReplaceAllString(l, "")
				for _, m := range linkRe.FindAllStringSubmatch(l, -1) {
					target := m[1]
					if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
						continue
					}
					file, frag, _ := strings.Cut(target, "#")
					dest := path
					if file != "" {
						dest = filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(path), file)))
						st, err := os.Stat(filepath.FromSlash(dest))
						if err != nil {
							t.Errorf("%s:%d: a link to %s, which does not exist", path, n, target)
							continue
						}
						if st.IsDir() {
							continue
						}
					}
					if frag != "" && strings.HasSuffix(dest, ".md") && !anchorsOf(dest)[frag] {
						t.Errorf("%s:%d: a link to %s, but no heading of %s makes the anchor #%s", path, n, target, dest, frag)
					}
				}
			})
		})
	}
}

func TestTheDocsMapLinksEveryPageOfTheThreeLayers(t *testing.T) {
	b, err := os.ReadFile(filepath.FromSlash("docs/README.md"))
	if err != nil {
		t.Fatal(err)
	}
	linked := map[string]bool{}
	for _, m := range linkRe.FindAllStringSubmatch(string(b), -1) {
		file, _, _ := strings.Cut(m[1], "#")
		linked[filepath.ToSlash(filepath.Clean(filepath.Join("docs", file)))] = true
	}
	for _, path := range layerPages(t) {
		if !linked[path] {
			t.Errorf("docs/README.md does not link %s", path)
		}
	}
}

func TestEveryADRIsJoinedToADesignPage(t *testing.T) {
	adrs, _ := filepath.Glob(filepath.FromSlash("docs/adr/*.md"))
	design, _ := filepath.Glob(filepath.FromSlash("docs/design/*.md"))
	joined := map[string]string{}
	for _, d := range design {
		if filepath.Base(d) == "README.md" {
			continue
		}
		p := readPage(t, filepath.ToSlash(d))
		for _, h := range []string{"## Overview", "## Rules", "## Decisions", "## Open questions"} {
			if !slices.Contains(p.lines, h) {
				t.Errorf("%s: a design page has the sections Overview, Rules, Decisions and Open questions; %q is missing", p.path, h)
			}
		}
		for _, m := range linkRe.FindAllStringSubmatch(strings.Join(p.lines, "\n"), -1) {
			file, _, _ := strings.Cut(m[1], "#")
			if strings.HasPrefix(file, "../adr/") {
				joined[filepath.Base(file)] = filepath.Base(d)
			}
		}
	}
	for _, a := range adrs {
		if _, ok := joined[filepath.Base(a)]; !ok {
			t.Errorf("%s is linked from no page in docs/design", filepath.ToSlash(a))
		}
	}
}

// exported lists the names the reference must have a heading for: every exported top-level
// type, function, constant and variable of a package as `Name`, and every exported method of an
// exported type declared in it as `Type.Method`.
func exported(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	var names []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				if d.Recv == nil {
					names = append(names, d.Name.Name)
					continue
				}
				typ := d.Recv.List[0].Type
				for {
					switch x := typ.(type) {
					case *ast.StarExpr:
						typ = x.X
						continue
					case *ast.IndexExpr:
						typ = x.X
						continue
					case *ast.IndexListExpr:
						typ = x.X
						continue
					}
					break
				}
				if id, ok := typ.(*ast.Ident); ok && id.IsExported() {
					names = append(names, id.Name+"."+d.Name.Name)
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							names = append(names, s.Name.Name)
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								names = append(names, n.Name)
							}
						}
					}
				}
			}
		}
	}
	sort.Strings(names)
	return slices.Compact(names)
}

// referenceHeadings maps each code-span heading of the reference ("### `App.Get`") to its page
// and anchor.
func referenceHeadings(t *testing.T) (map[string]string, []string) {
	files, _ := filepath.Glob(filepath.FromSlash("docs/reference/*.md"))
	sort.Strings(files)
	where := map[string]string{}
	var index []string
	for _, f := range files {
		if filepath.Base(f) == "README.md" {
			continue
		}
		p := readPage(t, filepath.ToSlash(f))
		seen := map[string]int{}
		eachProseLine(p, func(_ int, l string) {
			m := headingRe.FindStringSubmatch(l)
			if m == nil || len(m[1]) < 2 {
				return
			}
			s := slug(m[2])
			anchor := s
			if k := seen[s]; k > 0 {
				anchor = s + "-" + strconv.Itoa(k)
			}
			seen[s]++
			index = append(index, fmt.Sprintf("- [%s](./%s#%s)", m[2], filepath.Base(f), anchor))
			if strings.HasPrefix(m[2], "`") && strings.HasSuffix(m[2], "`") && strings.Count(m[2], "`") == 2 {
				where[strings.Trim(m[2], "`")] = filepath.Base(f)
			}
		})
	}
	return where, index
}

func TestEveryExportedNameHasItsReferenceHeading(t *testing.T) {
	where, _ := referenceHeadings(t)
	for _, pkg := range []struct{ dir, prefix string }{{".", ""}, {"bitetest", "bitetest."}, {"stdhttp", "stdhttp."}} {
		for _, name := range exported(t, pkg.dir) {
			if where[pkg.prefix+name] == "" {
				t.Errorf("no heading \"### `%s`\" in docs/reference for the exported %s%s", pkg.prefix+name, pkg.prefix, name)
			}
		}
	}
}

const indexStart, indexEnd = "<!-- every heading: written by go test -run TestTheReferenceListsEveryHeadingOnce -update -->", "<!-- end of every heading -->"

func TestTheReferenceListsEveryHeadingOnce(t *testing.T) {
	path := filepath.FromSlash("docs/reference/README.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i, j := strings.Index(s, indexStart), strings.Index(s, indexEnd)
	if i < 0 || j < i {
		t.Fatalf("docs/reference/README.md has no %q ... %q markers", indexStart, indexEnd)
	}
	_, index := referenceHeadings(t)
	want := indexStart + "\n\n" + strings.Join(index, "\n") + "\n\n"
	if s[i:j] == want {
		return
	}
	if *update {
		if err := os.WriteFile(path, []byte(s[:i]+want+s[j:]), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Error("docs/reference/README.md's list of headings is stale; run go test -run TestTheReferenceListsEveryHeadingOnce -update")
}

// A Go block marked to compile, and how: "compiles" is a whole file; "compiles: decls" is
// top-level declarations, joined with the page's earlier decls blocks into one file; "compiles:
// body" is statements, run inside a function that has app (*bites.App) and ctx in scope and that
// sees the page's decls. Imports are added for the packages a block names.
type snippet struct {
	page string
	line int // the line of the block's first line of code
	mode string
	code string
}

var compilesRe = regexp.MustCompile(`^<!--\s*compiles(?::\s*(decls|body))?\s*-->$`)

func snippets(t *testing.T) []snippet {
	var out []snippet
	files := []string{"README.md", "docs/README.md"}
	for _, g := range []string{"docs/guide/*.md", "docs/reference/*.md", "docs/design/*.md"} {
		f, _ := filepath.Glob(filepath.FromSlash(g))
		for _, x := range f {
			files = append(files, filepath.ToSlash(x))
		}
	}
	for _, path := range files {
		if _, err := os.Stat(filepath.FromSlash(path)); err != nil {
			continue
		}
		p := readPage(t, path)
		for i := 0; i < len(p.lines); i++ {
			m := compilesRe.FindStringSubmatch(strings.TrimSpace(p.lines[i]))
			if m == nil {
				continue
			}
			j := i + 1
			for j < len(p.lines) && strings.TrimSpace(p.lines[j]) == "" {
				j++
			}
			if j >= len(p.lines) || strings.TrimSpace(p.lines[j]) != "```go" {
				t.Errorf("%s:%d: a compiles mark is followed by a ```go block", path, i+1)
				continue
			}
			k := j + 1
			for k < len(p.lines) && strings.TrimSpace(p.lines[k]) != "```" {
				k++
			}
			mode := m[1]
			if mode == "" {
				mode = "file"
			}
			out = append(out, snippet{page: path, line: j + 2, mode: mode, code: strings.Join(p.lines[j+1:k], "\n")})
			i = k
		}
	}
	return out
}

// importPaths are the packages a snippet may name without importing them.
var importPaths = map[string]string{
	"bites": "github.com/nevindra/bites", "bitetest": "github.com/nevindra/bites/bitetest", "stdhttp": "github.com/nevindra/bites/stdhttp", "http": "net/http",
	"bytes": "bytes", "context": "context", "crypto/tls": "crypto/tls", "tls": "crypto/tls", "errors": "errors",
	"fmt": "fmt", "fs": "io/fs", "io": "io", "json": "encoding/json", "log": "log", "slog": "log/slog",
	"net": "net", "os": "os", "signal": "os/signal", "slices": "slices", "strconv": "strconv",
	"strings": "strings", "sync": "sync", "syscall": "syscall", "testing": "testing", "time": "time",
	"embed": "embed", "atomic": "sync/atomic", "rand": "math/rand/v2", "sql": "database/sql", "x509": "crypto/x509",
	"mime": "mime", "path": "path", "httptest": "net/http/httptest", "math": "math",
	"netip": "net/netip", "template": "html/template", "uuid": "uuid", "subtle": "crypto/subtle", "sha256": "crypto/sha256", "hex": "encoding/hex", "hmac": "crypto/hmac",
}

var selectorRe = regexp.MustCompile(`\b([a-z][a-z0-9]*)\.[A-Za-z_]`)

// quotedRe is an interpreted string literal; a selector inside one ("path.id") names no package.
var quotedRe = regexp.MustCompile(`"(?:[^"\\\n]|\\.)*"`)

func importsFor(code string) string {
	used := map[string]bool{}
	for _, m := range selectorRe.FindAllStringSubmatch(quotedRe.ReplaceAllString(code, `""`), -1) {
		if p, ok := importPaths[m[1]]; ok && m[1] != "crypto/tls" {
			used[p] = true
		}
	}
	paths := make([]string, 0, len(used))
	for p := range used {
		paths = append(paths, strconv.Quote(p))
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return ""
	}
	return "import (\n" + strings.Join(paths, "\n") + "\n)\n"
}

func TestEveryGoBlockMarkedCompilesCompiles(t *testing.T) {
	all := snippets(t)
	if len(all) == 0 {
		t.Skip("no block is marked compiles")
	}
	if testing.Short() {
		t.Skip("compiling the guide's blocks runs the go command")
	}
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name, s string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module snippets\n\ngo 1.27\n\nrequire github.com/nevindra/bites v0.0.0\n\nreplace github.com/nevindra/bites => "+root+"\n")
	// A page's decls and bodies are one package; each whole-file block is a package of its own.
	type pkg struct {
		decls, bodies strings.Builder
	}
	pages := map[string]*pkg{}
	var order []string
	pkgName := func(page string, i int) string {
		s := strings.NewReplacer("/", "_", ".", "_", "-", "_").Replace(strings.TrimSuffix(page, ".md"))
		if i >= 0 {
			s += "_" + strconv.Itoa(i)
		}
		return strings.ToLower(s)
	}
	for i, s := range all {
		lineDir := "//line " + filepath.Join(root, filepath.FromSlash(s.page)) + ":" + strconv.Itoa(s.line) + "\n"
		switch s.mode {
		case "file":
			code := s.code
			if !strings.HasPrefix(strings.TrimSpace(code), "package ") {
				t.Errorf("%s:%d: a block marked compiles is a whole file with its package clause; mark statements \"compiles: body\" and declarations \"compiles: decls\"", s.page, s.line)
				continue
			}
			pkgLine, rest, _ := strings.Cut(code, "\n")
			hasImports := strings.Contains(rest, "import ")
			imp := ""
			if !hasImports {
				imp = importsFor(rest)
			}
			// The package clause stays first; the line directive maps what follows to the page. A test
			// package is written as a test file, so that it is run as well as compiled (ADR 0050).
			file := "main.go"
			if strings.HasSuffix(strings.TrimSpace(strings.TrimPrefix(pkgLine, "package ")), "_test") {
				file = "main_test.go"
			}
			write(filepath.Join(pkgName(s.page, i), file), pkgLine+"\n"+imp+"//line "+filepath.Join(root, filepath.FromSlash(s.page))+":"+strconv.Itoa(s.line+1)+"\n"+rest+"\n")
		case "decls", "body":
			p := pages[s.page]
			if p == nil {
				p = &pkg{}
				pages[s.page] = p
				order = append(order, s.page)
			}
			if s.mode == "decls" {
				p.decls.WriteString(lineDir + s.code + "\n")
			} else {
				fmt.Fprintf(&p.bodies, "func _(app *bites.App, ctx context.Context) {\n_, _ = app, ctx\n{\n%s%s\n}\n}\n", lineDir, s.code)
			}
		}
	}
	for _, page := range order {
		p := pages[page]
		body := p.decls.String() + p.bodies.String()
		src := "package " + pkgName(page, -1) + "\n\n" + importsFor(body+" bites.X context.X") +
			"var _ = bites.New\nvar _ context.Context\n\n" + body
		write(filepath.Join(pkgName(page, -1), "page.go"), src)
	}
	// go test vets each package before it runs it, and runs the blocks that are test packages, so
	// an example test in a page that compiles but fails is caught too.
	cmd := exec.Command("go", "test", "-count=1", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	// Report each problem against its page, so a page's author can run only their own.
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	reported := false
	for sc.Scan() {
		l := strings.TrimPrefix(strings.ReplaceAll(sc.Text(), root+string(filepath.Separator), ""), "vet: ")
		if strings.Contains(l, ".md:") {
			t.Error(filepath.ToSlash(l))
			reported = true
		}
	}
	if !reported {
		t.Errorf("go test of the docs' blocks failed:\n%s", out)
	}
}
