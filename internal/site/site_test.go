package site_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevindra/bites/internal/site"
)

func keep(href string) (string, bool) { return href, true }

func TestTheMarkdownSubsetRendersAsHTML(t *testing.T) {
	for _, tc := range []struct{ name, md, want string }{
		{"a heading has an id and an anchor", "## The `App.Get` call", `<h2 id="the-appget-call">The <code>App.Get</code> call<a class="anchor" href="#the-appget-call"`},
		{"emphasis and code", "a **bold** and *it* and `x<y`", "<p>a <strong>bold</strong> and <em>it</em> and <code>x&lt;y</code></p>"},
		{"snake_case is not emphasis", "user_id and other_id", "<p>user_id and other_id</p>"},
		{"a link with code in its text", "[`Opt`](./input.md#opt)", `<p><a href="./input.md#opt"><code>Opt</code></a></p>`},
		{"raw HTML is escaped", "<script>alert(1)</script> & \"q\"", "<p>&lt;script&gt;alert(1)&lt;/script&gt; &amp; &#34;q&#34;</p>"},
		{"a fenced block keeps its text and language", "```go\nif a < b {\n}\n```", "<pre><code class=\"language-go\">if a &lt; b {\n}\n</code></pre>"},
		{"a comment line is dropped", "<!-- compiles: decls -->\ntext", "<p>text</p>"},
		{"a table", "| A | B |\n|---|--:|\n| `x\\|y` | 2 |", `<th>A</th><th style="text-align:right">B</th>`},
		{"a nested list", "- one\n  - inner\n- two", "<ul>\n<li>one\n<ul>\n<li>inner</li>\n</ul>\n</li>\n<li>two</li>\n</ul>"},
		{"an ordered list", "1. a\n2. b", "<ol>\n<li>a</li>\n<li>b</li>\n</ol>"},
		{"a block quote", "> quoted *text*", "<blockquote>\n<p>quoted <em>text</em></p>\n</blockquote>"},
		{"an autolink", "see <https://example.com/a?b=1&c=2>", `<a href="https://example.com/a?b=1&amp;c=2">`},
		{"a bracket that is not a link", "a [b] c", "<p>a [b] c</p>"},
		{"a duplicate heading is numbered as GitHub does", "# A\n# A", `id="a-1"`},
	} {
		got := site.Render(tc.md, keep)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s:\n%s\nwant it to contain\n%s", tc.name, got, tc.want)
		}
	}
}

func write(t *testing.T, root, name, s string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLinksBetweenLayersAreRewrittenAndABrokenOneFailsTheBuild(t *testing.T) {
	src := t.TempDir()
	write(t, src, "README.md", "# Home\n\n[docs](docs/README.md)\n")
	write(t, src, "GLOSSARY.md", "# Words\n")
	write(t, src, "docs/README.md", "# Docs\n\n[guide](./guide/a.md#part) [adr](./adr/) [gloss](../GLOSSARY.md) [src](../x.go)\n")
	write(t, src, "docs/guide/README.md", "# Guide\n\n[a](./a.md)\n")
	write(t, src, "docs/guide/a.md", "# A\n\n## Part\n\n[design](../design/a.md) [up](#part)\n")
	write(t, src, "docs/design/a.md", "# D\n")
	write(t, src, "docs/adr/0001-x.md", "# One\n")
	write(t, src, "x.go", "package x\n")
	out := filepath.Join(t.TempDir(), "out")
	rep, err := site.Build(site.Options{Src: src, Out: out})
	if err != nil {
		t.Fatalf("%v %v", err, rep.Broken)
	}
	read := func(n string) string {
		b, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(n)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	docs := read("docs/index.html")
	for _, want := range []string{`href="guide/a.html#part"`, `href="adr/index.html"`, `href="../GLOSSARY.html"`, `class="unlinked" title="../x.go"`} {
		if !strings.Contains(docs, want) {
			t.Errorf("docs/index.html lacks %s", want)
		}
	}
	if !strings.Contains(read("docs/guide/a.html"), `href="../design/a.html"`) {
		t.Error("a link between layers was not rewritten")
	}
	if !strings.Contains(read("docs/adr/index.html"), "One") {
		t.Error("the folder with no README has no listing")
	}
	if !strings.Contains(read("docs/guide/a.html"), "<aside>") {
		t.Error("a guide page has no sidebar")
	}
	if rep.Unlinked != 1 {
		t.Errorf("unlinked %d", rep.Unlinked)
	}
	rep, err = site.Build(site.Options{Src: src, Out: out, RepoURL: "https://example.com/r/"})
	if err != nil || !strings.Contains(read("docs/index.html"), `href="https://example.com/r/blob/main/x.go"`) {
		t.Errorf("with a repository URL: %v", err)
	}

	write(t, src, "docs/guide/a.md", "# A\n\n[gone](./gone.md) [bad](../design/a.md#nowhere)\n")
	rep, err = site.Build(site.Options{Src: src, Out: out})
	if err == nil || len(rep.Broken) != 3 {
		t.Fatalf("a broken link did not fail the build: %v %v", err, rep.Broken)
	}
}

func TestTheRepositorysOwnDocumentationBuildsWithNoBrokenLink(t *testing.T) {
	rep, err := site.Build(site.Options{Src: filepath.Join("..", ".."), Out: filepath.Join(t.TempDir(), "site")})
	if err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(rep.Broken, "\n"))
	}
	if rep.Pages < 100 {
		t.Fatalf("only %d pages", rep.Pages)
	}
}
