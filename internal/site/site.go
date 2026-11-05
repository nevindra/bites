// Package site renders the repository's documentation (docs/, GLOSSARY.md and README.md) to static
// HTML with the standard library only (ADR 0073). A link between pages, layers and ADRs is
// rewritten to the page it names and every anchor is checked, so a broken link fails the build.
// It is driven by cmd/bites-site and publishes nothing.
package site

import (
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Options says what to read and where to write.
type Options struct {
	// Src is the repository root.
	Src string
	// Out is the directory to write; it is created, and files in it are overwritten.
	Out string
	// RepoURL, when set, is the base of the repository on a code host ("https://github.com/o/r");
	// a link to a file that is not documentation (a source file, a benchmark result) points
	// there. Without it such a link is shown as text with its target in a title.
	RepoURL string
	// Title is the site's name; "bites" if empty.
	Title string
	// Branch is the branch RepoURL links to; "main" if empty.
	Branch string
}

// Report is what a build found.
type Report struct {
	Pages int
	// Broken are links to a page or an anchor that does not exist; a build with any fails.
	Broken []string
	// Unlinked are links to files that are not documentation, left as text because no RepoURL
	// was given. Their number is information, not a failure.
	Unlinked int
}

type page struct {
	src, out string // slash paths relative to the root
	md       string
	title    string
	heads    []Heading
	ids      map[string]bool
	gen      bool // a listing made by the builder
}

// Build renders the site.
func Build(o Options) (*Report, error) {
	if o.Title == "" {
		o.Title = "bites"
	}
	if o.Branch == "" {
		o.Branch = "main"
	}
	pages := map[string]*page{} // by source path
	dirs := map[string]bool{}
	root := os.DirFS(o.Src)
	err := fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && p != "docs" && !strings.HasPrefix(p, "docs/") {
				return fs.SkipDir
			}
			dirs[p] = true
			return nil
		}
		inDocs := strings.HasPrefix(p, "docs/")
		if !strings.HasSuffix(p, ".md") || !(inDocs || p == "README.md" || p == "GLOSSARY.md") {
			return nil
		}
		b, err := fs.ReadFile(root, p)
		if err != nil {
			return err
		}
		pg := &page{src: p, md: string(b)}
		pg.out = outPath(p)
		pages[p] = pg
		return nil
	})
	if err != nil {
		return nil, err
	}
	if pages["README.md"] == nil {
		return nil, errors.New("site: no README.md in " + o.Src)
	}
	// A listing for each folder of docs that has pages and no README.
	for d := range dirs {
		if d == "." || pages[path.Join(d, "README.md")] != nil {
			continue
		}
		var kids []string
		for p := range pages {
			if path.Dir(p) == d {
				kids = append(kids, p)
			}
		}
		if len(kids) == 0 {
			continue
		}
		sort.Strings(kids)
		var b strings.Builder
		b.WriteString("# " + path.Base(d) + "\n\n")
		for _, k := range kids {
			b.WriteString("- [" + titleOf(pages[k]) + "](./" + path.Base(k) + ")\n")
		}
		src := path.Join(d, "README.md")
		pages[src] = &page{src: src, out: outPath(src), md: b.String(), gen: true}
	}
	for _, pg := range pages {
		pg.heads = Headings(pg.md)
		pg.ids = map[string]bool{}
		for _, h := range pg.heads {
			pg.ids[h.ID] = true
		}
		pg.title = titleOf(pg)
	}
	// The order of a layer's pages is the order its README links them.
	order := map[string][]string{}
	for _, layer := range []string{"docs/guide", "docs/reference", "docs/design"} {
		order[layer] = layerOrder(pages, layer)
	}

	rep := &Report{}
	for _, pg := range sortedPages(pages) {
		link := func(href string) (string, bool) {
			to, ok, why := resolve(pages, o, pg, href)
			switch why {
			case "broken":
				rep.Broken = append(rep.Broken, fmt.Sprintf("%s: %s", pg.src, href))
			case "unlinked":
				rep.Unlinked++
			}
			return to, ok
		}
		body := Render(pg.md, link)
		doc := layout(o, pg, pages, order, body)
		dst := filepath.Join(o.Out, filepath.FromSlash(pg.out))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, []byte(doc), 0o644); err != nil {
			return nil, err
		}
		rep.Pages++
	}
	if err := os.MkdirAll(filepath.Join(o.Out, "assets"), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(o.Out, "assets", "style.css"), []byte(css), 0o644); err != nil {
		return nil, err
	}
	sort.Strings(rep.Broken)
	if len(rep.Broken) > 0 {
		return rep, fmt.Errorf("site: %d broken links", len(rep.Broken))
	}
	return rep, nil
}

func outPath(src string) string {
	if path.Base(src) == "README.md" {
		return path.Join(path.Dir(src), "index.html")
	}
	return strings.TrimSuffix(src, ".md") + ".html"
}

func titleOf(p *page) string {
	for _, h := range Headings(p.md) {
		if h.Level == 1 {
			return h.Text
		}
	}
	return strings.TrimSuffix(path.Base(p.src), ".md")
}

func sortedPages(m map[string]*page) []*page {
	out := make([]*page, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].src < out[j].src })
	return out
}

// layerOrder lists the pages of a layer in the order its README links them, then the rest.
func layerOrder(pages map[string]*page, layer string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if pages[p] != nil && !seen[p] && path.Dir(p) == layer {
			seen[p] = true
			out = append(out, p)
		}
	}
	readme := path.Join(layer, "README.md")
	add(readme)
	if r := pages[readme]; r != nil {
		for _, m := range linkRe2.FindAllStringSubmatch(r.md, -1) {
			h, _, _ := strings.Cut(m[1], "#")
			if strings.HasSuffix(h, ".md") {
				add(path.Join(layer, h))
			}
		}
	}
	for _, p := range sortedPages(pages) {
		add(p.src)
	}
	return out
}

var linkRe2 = linkTargetRe

// resolve maps a link in page from to its site address. why is "" for a good link, "broken" for a
// target that does not exist, and "unlinked" for a file outside the documentation.
func resolve(pages map[string]*page, o Options, from *page, href string) (to string, ok bool, why string) {
	if href == "" {
		return "", false, "broken"
	}
	if u, err := url.Parse(href); err == nil && u.Scheme != "" {
		return href, true, ""
	}
	target, frag, _ := strings.Cut(href, "#")
	if un, err := url.PathUnescape(target); err == nil {
		target = un
	}
	var tp *page
	if target == "" {
		tp = from
	} else {
		clean := path.Join(path.Dir(from.src), target)
		if clean == ".." || strings.HasPrefix(clean, "../") {
			return "", false, "broken"
		}
		switch {
		case pages[clean] != nil:
			tp = pages[clean]
		case pages[path.Join(clean, "README.md")] != nil:
			tp = pages[path.Join(clean, "README.md")]
		default:
			if _, err := os.Stat(filepath.Join(o.Src, filepath.FromSlash(clean))); err != nil {
				return "", false, "broken"
			}
			if o.RepoURL == "" {
				return href, false, "unlinked"
			}
			kind := "blob"
			if st, _ := os.Stat(filepath.Join(o.Src, filepath.FromSlash(clean))); st != nil && st.IsDir() {
				kind = "tree"
			}
			return strings.TrimRight(o.RepoURL, "/") + "/" + kind + "/" + o.Branch + "/" + clean, true, ""
		}
	}
	if frag != "" && !tp.ids[frag] {
		return "", false, "broken"
	}
	rel, err := filepath.Rel(filepath.FromSlash(path.Dir(from.out)), filepath.FromSlash(tp.out))
	if err != nil {
		return "", false, "broken"
	}
	to = filepath.ToSlash(rel)
	if target == "" {
		to = ""
	}
	if frag != "" {
		to += "#" + frag
	}
	return to, true, ""
}

func layout(o Options, pg *page, pages map[string]*page, order map[string][]string, body string) string {
	rel := func(to string) string {
		r, _ := filepath.Rel(filepath.FromSlash(path.Dir(pg.out)), filepath.FromSlash(to))
		return filepath.ToSlash(r)
	}
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>" + html.EscapeString(pg.title) + " | " + html.EscapeString(o.Title) + "</title>\n")
	b.WriteString(`<link rel="stylesheet" href="` + rel("assets/style.css") + "\">\n</head>\n<body>\n")
	b.WriteString(`<header class="top"><a class="brand" href="` + rel("index.html") + `">` + html.EscapeString(o.Title) + "</a><nav>")
	nav := [][2]string{{"Guide", "docs/guide/index.html"}, {"Reference", "docs/reference/index.html"}, {"Design", "docs/design/index.html"}, {"ADRs", "docs/adr/index.html"}, {"Roadmap", "docs/roadmap.html"}, {"Glossary", "GLOSSARY.html"}}
	for _, n := range nav {
		cur := ""
		if strings.HasPrefix(pg.out, strings.TrimSuffix(n[1], "index.html")) && strings.HasSuffix(n[1], "index.html") || pg.out == n[1] {
			cur = ` aria-current="page"`
		}
		b.WriteString(`<a href="` + rel(n[1]) + `"` + cur + ">" + n[0] + "</a>")
	}
	b.WriteString("</nav></header>\n<div class=\"wrap\">\n")
	layer := path.Dir(pg.src)
	if ord, ok := order[layer]; ok {
		b.WriteString("<aside><ul>\n")
		for _, p := range ord {
			cur := ""
			if p == pg.src {
				cur = ` aria-current="page"`
			}
			t := pages[p].title
			if path.Base(p) == "README.md" {
				t = "Overview"
			}
			b.WriteString(`<li><a href="` + rel(pages[p].out) + `"` + cur + ">" + html.EscapeString(t) + "</a></li>\n")
		}
		b.WriteString("</ul></aside>\n")
	}
	b.WriteString("<main>\n" + body + "</main>\n</div>\n")
	b.WriteString(`<footer>` + html.EscapeString(o.Title) + " documentation, built from <code>" + html.EscapeString(pg.src) + "</code>.</footer>\n</body>\n</html>\n")
	return b.String()
}
