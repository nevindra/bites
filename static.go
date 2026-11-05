package bites

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/router"
)

// Static is a directory of files served under a path prefix (ADR 0065): the assets of a web
// application, from an os.Root, an fs.FS or an embed.FS. App.Static and Group.Static register it.
//
// Every file is answered as a File is (ADR 0043, ADR 0064): Content-Length, the validators, 304,
// 412, a single Range, and the same 404 problem+json for a name that is missing, that is not a
// regular file or that tries to leave the directory. A path is matched as raw segments and a
// path with "..", "." or "//" is refused by the router before it gets here; the rest is
// percent-decoded and checked with fs.ValidPath, and an os.Root refuses a symbolic link out.
//
// A path that ends in a slash (the prefix itself, "/docs/") is answered with its Index file;
// a directory named without the slash is a 404, never a redirect (ADR 0018).
//
// Precompressed twins: a file app.js with app.js.br, app.js.zst or app.js.gz beside it is answered
// with the twin a client's Accept-Encoding weighs highest (RFC 9110 section 12.5.3; br, then zstd,
// then gzip on a tie), with Content-Encoding and the type of app.js. Every answer of a type that
// may have a twin says Vary: Accept-Encoding. Twins are looked for only for a compressible type
// (not an image, a video, a font or an archive) and only for an encoding the client accepts.
//
// Cache-Control: a name that holds a content hash (Immutable, below) is "max-age=31536000,
// immutable"; the Index and the Fallback are always "no-cache", because they name the hashed
// files and must never be stale; every other name is "no-cache", or "max-age=N" with MaxAge.
// no-cache lets a browser keep the file and ask with its ETag, which costs a 304 and no body.
//
// An embed.FS cannot change while the program runs, so it is read once at Registration: every
// file is hashed (SHA-256) into a strong ETag, since embedded files have no modification time,
// and its twins are known, so a request makes no stat. So is any fs.FS whose files have no
// modification time (an embed.FS reached through fs.Sub): such a source has no way to say it
// changed. Any other source, an os.Root above all, is looked at on each request and sees files
// as they are now.
type Static struct {
	// Root and FS: exactly one says where the files are.
	Root *os.Root
	// FS is the file system the files are read from, when Root is not set.
	FS fs.FS
	// Index is the file answered for a path that ends in a slash; "index.html" when empty. It is
	// a plain name, with no slash.
	Index string
	// Fallback is the file a single-page application answers for a path that names no file, so
	// that its own router can show the page: "index.html" for most. It is answered only for a
	// navigation: a GET or HEAD whose Accept lists text/html, whose last segment has no
	// extension, and whose first segment under the prefix is not one that another route of the
	// app uses. So /api/nothing is still the API's 404 when /api/... routes exist, and a missing
	// /app.js is a 404 and not the page. Empty is no fallback.
	Fallback string
	// MaxAge is the max-age of a name with no content hash; zero sends "no-cache". It is whole
	// seconds.
	MaxAge time.Duration
	// Immutable reports whether a name (slash separated, inside the directory) holds a content
	// hash, so it can be cached for a year. Nil is the default rule: one part of the base name,
	// between dots or dashes, of 8 to 64 characters that is lower-case hex with a digit and a
	// letter (webpack's [contenthash]), or letters and digits with an upper-case letter and a
	// digit (Vite's and esbuild's [hash]): app-3f2a1b9c.js, index-BxR3k9aQ.js,
	// chunk-ABCD1234.js, main.3f2a1b9c.chunk.css.
	Immutable func(name string) bool
}

// staticRoute is a Static after Registration.
type staticRoute struct {
	root      *os.Root
	fsys      fs.FS
	prefix    string // the full prefix, "" for the root of the app
	index     string
	fallback  string
	cache     string // the Cache-Control of a name with no content hash
	immutable func(string) bool
	embedded  map[string]embedEntry // every file of an embed.FS or a source without times; nil for any other source
	rt        *route

	seen sync.Map // name -> *seenTwins, for a source that can change

	owned    map[string]bool // first segments under the prefix that another route uses, set when serving starts
	ownedAny bool            // another route has a wildcard as its first segment there
}

// embedEntry is what Registration learned about one file of an embed.FS.
type embedEntry struct {
	etag  string
	size  int64
	twins uint8 // the encodings it has a twin for: twinBr, twinZstd, twinGzip
}

// The precompressed twins, in the server's order of preference.
const (
	twinBr = 1 << iota
	twinZstd
	twinGzip
)

// twinKind is one encoding a twin may have: its bit, the extension of its file and its
// Content-Encoding.
type twinKind struct {
	bit      uint8
	ext, enc string
}

var twinExt = [...]twinKind{{twinBr, ".br", "br"}, {twinZstd, ".zst", "zstd"}, {twinGzip, ".gz", "gzip"}}

const (
	cacheImmutable = "max-age=31536000, immutable"
	cacheNone      = "no-cache"
)

// Static serves the files of s under prefix (ADR 0065): "/" for the root of the app, or a path
// such as "/assets" (a slash first, none last, no wildcard). The route is GET (and HEAD) of
// prefix + "/{path...}", so a more specific route of the app wins over it, and it runs the app's
// middleware and any given with Use. It is left out of the API document. A mistake panics.
func (a *App) Static(prefix string, s Static, opts ...RouteOption) {
	where := callerWhere()
	a.seal("Static "+prefix, where)
	a.mu.Lock()
	enclosing := a.provOpts
	a.mu.Unlock()
	registerStatic(a, "", prefix, s, scoped(enclosing, opts), where)
}

// Static is App.Static under the group's prefix, with the group's options.
func (g *Group) Static(prefix string, s Static, opts ...RouteOption) {
	where := callerWhere()
	g.seal("Static "+g.prefix+prefix, where)
	registerStatic(g.app, g.prefix, prefix, s, scoped(g.opts, opts), where)
}

func registerStatic(a *App, outer, prefix string, s Static, opts []RouteOption, where string) {
	label := "Static " + outer + prefix
	fail := func(format string, args ...any) {
		panic(&RegistrationError{msg: label + ": " + fmt.Sprintf(format, args...) + "\n\tregistered at " + where})
	}
	if prefix != "/" && (!strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") || strings.ContainsAny(prefix, "{}")) {
		fail("the prefix is \"/\" or starts with a slash, does not end with one, and has no wildcard")
	}
	if (s.Root == nil) == (s.FS == nil) {
		fail("give exactly one of Root and FS")
	}
	st := &staticRoute{root: s.Root, fsys: s.FS, index: s.Index, fallback: s.Fallback, immutable: s.Immutable}
	if st.index == "" {
		st.index = "index.html"
	}
	if !fs.ValidPath(st.index) || st.index == "." || strings.Contains(st.index, "/") {
		fail("Index %q is not a plain file name: give a name with no slash, such as index.html", s.Index)
	}
	switch {
	case s.MaxAge < 0 || (s.MaxAge > 0 && s.MaxAge < time.Second):
		fail("MaxAge(%v) is not a cache lifetime: give whole seconds, or zero for no-cache", s.MaxAge)
	case s.MaxAge > 0:
		st.cache = "max-age=" + strconv.FormatInt(int64(s.MaxAge/time.Second), 10)
	default:
		st.cache = cacheNone
	}
	if st.immutable == nil {
		st.immutable = fingerprinted
	}
	if fsys, ok := embeddedFS(s.FS); ok || (s.FS != nil && timeless(s.FS)) {
		if !ok {
			fsys = s.FS
		}
		m, err := indexEmbedded(fsys)
		if err != nil {
			fail("reading the embedded files: %v", err)
		}
		st.embedded = m
	}
	if s.Fallback != "" {
		if !fs.ValidPath(s.Fallback) || s.Fallback == "." {
			fail("Fallback %q is not a file name inside the directory", s.Fallback)
		}
		if _, ok := st.lookup(s.Fallback); !ok {
			fail("Fallback %q is not a regular file in the directory: a fallback that does not exist would answer every navigation with a 404", s.Fallback)
		}
	}
	full := outer + prefix
	if prefix == "/" {
		full = outer
	}
	st.prefix = full

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.serving {
		panic(&RegistrationError{msg: label + " registered at " + where + " after the app started serving: routes can be added only before it does"})
	}
	cfg := routeConfig{maxBody: a.limits.MaxBody, bodyTimeout: a.limits.BodyTimeout}
	if err := applyOptions(&cfg, opts); err != nil {
		panic(registrationFailure(label, err, where))
	}
	if cfg.summary != "" || cfg.description != "" || cfg.operationID != "" || len(cfg.returns) > 0 {
		fail("Summary, Description, OperationID and Returns describe an operation of the API document, which leaves static files out; Tag is accepted and ignored")
	}
	if cfg.setsLocation {
		fail("SetsLocation is given to a static route, which answers files and has no Created to carry a Location")
	}
	if name := cfg.answerOption(); name != "" {
		fail("%s states answers in the API document, which leaves static files out: give it to the typed routes instead", name)
	}
	info := RouteInfo{Method: "GET", Pattern: full + "/{path...}", Tags: slices.Clone(cfg.tags), Deprecated: cfg.deprecated, Static: true}
	a.noteRoute(info)
	if len(cfg.each) > 0 {
		cfg.addEach(info, label, where)
	}
	rt := newRoute("")
	if err := a.router.Add("GET", full+"/{path...}", rt, where); err != nil {
		panic(&RegistrationError{msg: err.Error()})
	}
	a.methods["GET"] = true
	st.rt = rt
	rt.serve = st.serve
	if err := a.bindMiddleware(cfg.mw); err != nil {
		panic(registrationFailure(label, err, where))
	}
	a.useOn(rt, cfg.mw)
	rt.chain = len(cfg.mw) > 0
	a.statics = append(a.statics, st)
}

// embeddedFS reports whether fsys is an embed.FS, which cannot change while the program runs.
func embeddedFS(fsys fs.FS) (fs.FS, bool) {
	switch e := fsys.(type) {
	case embed.FS:
		return e, true
	case *embed.FS:
		return e, e != nil
	}
	return nil, false
}

// timeless reports whether the files of fsys have no modification time, as those of an embed.FS
// reached through fs.Sub have: such a source cannot say it changed, so it is taken not to change
// and is indexed like an embed.FS. The first regular file decides.
func timeless(fsys fs.FS) bool {
	zero := true
	fs.WalkDir(fsys, ".", func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			zero = fi.ModTime().IsZero()
		}
		return fs.SkipAll
	})
	return zero
}

// indexEmbedded hashes every file of an embed.FS once and notes its twins.
func indexEmbedded(fsys fs.FS) (map[string]embedEntry, error) {
	m := map[string]embedEntry{}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		f, err := fsys.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			return err
		}
		sum := h.Sum(nil)
		m[name] = embedEntry{etag: `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`, size: n}
		return nil
	})
	for name, e := range m {
		for _, t := range twinExt {
			if _, ok := m[name+t.ext]; ok {
				e.twins |= t.bit
			}
		}
		m[name] = e
	}
	return m, err
}

// prepare learns, when the app starts serving, which first segments under the prefix other routes
// use, so the fallback never answers for them.
func (st *staticRoute) prepare(a *App) {
	if st.fallback == "" {
		return
	}
	st.owned = map[string]bool{}
	a.router.Each(func(_, pattern string, rt *route) {
		if rt == st.rt {
			return
		}
		rest, ok := strings.CutPrefix(pattern, st.prefix+"/")
		if !ok {
			return
		}
		seg, _, _ := strings.Cut(rest, "/")
		switch {
		case seg == "" || seg == "{$}":
		case strings.HasPrefix(seg, "{"):
			st.ownedAny = true
		default:
			st.owned[seg] = true
		}
	})
}

// meta is what a request needs to know about a file before it is opened.
type meta struct {
	size  int64
	mod   time.Time
	etag  string
	twins uint8
}

// lookup finds a regular file: in the embedded index, or by a stat.
func (st *staticRoute) lookup(name string) (meta, bool) {
	if st.embedded != nil {
		e, ok := st.embedded[name]
		return meta{size: e.size, etag: e.etag, twins: e.twins}, ok
	}
	var fi fs.FileInfo
	var err error
	if st.root != nil {
		fi, err = st.root.Stat(name)
	} else {
		fi, err = fs.Stat(st.fsys, name)
	}
	if err != nil || !fi.Mode().IsRegular() {
		return meta{}, false
	}
	return meta{size: fi.Size(), mod: fi.ModTime()}, true
}

// serve answers one request for a static file.
func (st *staticRoute) serve(a *App, ex *engine.Exchange, ps *router.Params, _ context.Context, _ *Request) {
	raw := ps.Values[0]
	ex.AddSlab(len(raw))
	rel, err := ex.Slab().Unescape(raw, false)
	if err != nil {
		problem(ex, 404, "")
		return
	}
	name, entry := rel, false
	if rel == "" || strings.HasSuffix(rel, "/") {
		name, entry = rel+st.index, true
	}
	if !fs.ValidPath(name) {
		problem(ex, 404, "")
		return
	}
	m, ok := st.lookup(name)
	if !ok {
		if st.fallback == "" || !st.navigation(ex.Head().Headers, rel) {
			problem(ex, 404, "")
			return
		}
		name, entry = st.fallback, true
		if m, ok = st.lookup(name); !ok {
			problem(ex, 404, "")
			return
		}
	}
	fa := fileAnswer{root: st.root, fsys: st.fsys, name: name, etag: m.etag}
	switch {
	case entry:
		fa.cache = cacheNone
	case st.immutable(name):
		fa.cache = cacheImmutable
	default:
		fa.cache = st.cache
	}
	ctype := mime.TypeByExtension(path.Ext(name))
	if st.embedded != nil {
		fa.vary = m.twins != 0
	} else {
		fa.vary = !engine.Incompressible(ctype)
	}
	if fa.vary {
		if t, tm, ok := st.twin(ex.Head().Headers, name, m); ok {
			fa.name, fa.typeName, fa.encoding, m = name+t.ext, name, t.enc, tm
			fa.etag = tm.etag
		}
	}
	a.sendFile(ex, &fa, m.size, m.mod)
}

// twin picks the precompressed twin the client weighs highest, among those that exist.
func (st *staticRoute) twin(hs []h1.Header, name string, orig meta) (t twinKind, m meta, ok bool) {
	have, known := orig.twins, st.embedded != nil
	var w [len(twinExt) + 1]int // the weights of br, zstd, gzip, then identity
	if !acceptEncodingWeights(hs, &w) {
		return t, m, false
	}
	for range twinExt {
		best := -1
		for i := range twinExt {
			if w[i] > 0 && (best < 0 || w[i] > w[best]) {
				best = i
			}
		}
		if best < 0 || w[len(twinExt)] > w[best] {
			return t, m, false // nothing acceptable is left, or the client prefers no coding
		}
		cand := twinExt[best]
		w[best] = 0
		if st.embedded == nil && !known {
			have, known = st.twinsOf(name, orig), true
		}
		if have&cand.bit == 0 {
			continue
		}
		if st.embedded != nil {
			e := st.embedded[name+cand.ext]
			return cand, meta{size: e.size, etag: e.etag}, true
		}
		if tm, ok := st.lookup(name + cand.ext); ok {
			return cand, tm, true
		}
		st.seen.Delete(name) // the twin went away: look again next time
	}
	return t, m, false
}

// seenTwins is which twins a file of an os.Root or an fs.FS had, the last time it was looked at
// with this size and modification time.
type seenTwins struct {
	size  int64
	mod   time.Time
	twins uint8
}

// twinsOf says which twins name has, for a source that can change. They are looked for once per
// version of the file (its size and modification time) and remembered, so a file with no twin
// does not cost three failed stats on every request; the memory is one entry per file that exists
// and was asked for with an Accept-Encoding, never per name a client invents.
func (st *staticRoute) twinsOf(name string, orig meta) uint8 {
	if v, ok := st.seen.Load(name); ok {
		if s := v.(*seenTwins); s.size == orig.size && s.mod.Equal(orig.mod) {
			return s.twins
		}
	}
	var have uint8
	for _, k := range twinExt {
		if _, ok := st.lookup(name + k.ext); ok {
			have |= k.bit
		}
	}
	st.seen.Store(name, &seenTwins{size: orig.size, mod: orig.mod, twins: have})
	return have
}

// acceptEncodingWeights reads the Accept-Encoding lines of a request into w, in thousandths:
// br, zstd, gzip, then identity (RFC 9110 section 12.5.3). A coding not listed takes the weight of
// "*" if that is listed, and 0 otherwise; identity not listed is acceptable at the lowest weight.
// It reports false when the request has no Accept-Encoding at all, which is answered identity.
func acceptEncodingWeights(hs []h1.Header, w *[len(twinExt) + 1]int) bool {
	listed := [len(twinExt) + 1]bool{}
	star, starListed, any := 0, false, false
	for i := range hs {
		if !bytes.EqualFold(hs[i].Name, []byte("accept-encoding")) {
			continue
		}
		any = true
		for v := hs[i].Value; len(v) > 0; {
			var item []byte
			if c := bytes.IndexByte(v, ','); c >= 0 {
				item, v = v[:c], v[c+1:]
			} else {
				item, v = v, nil
			}
			coding, params, _ := bytes.Cut(item, []byte(";"))
			coding = bytes.TrimSpace(coding)
			q := weight(params)
			k := -1
			switch {
			case bytes.EqualFold(coding, []byte("br")):
				k = 0
			case bytes.EqualFold(coding, []byte("zstd")):
				k = 1
			case bytes.EqualFold(coding, []byte("gzip")) || bytes.EqualFold(coding, []byte("x-gzip")):
				k = 2
			case bytes.EqualFold(coding, []byte("identity")):
				k = 3
			case len(coding) == 1 && coding[0] == '*':
				star, starListed = q, true
			}
			if k >= 0 && (!listed[k] || q < w[k]) { // listed twice: the lower weight wins
				w[k], listed[k] = q, true
			}
		}
	}
	if !any {
		return false
	}
	for k := range w {
		if !listed[k] && starListed {
			w[k] = star
		}
	}
	if !listed[3] && (!starListed || star > 0) {
		w[3] = 0 // acceptable, below any coding the client listed
	}
	return true
}

// weight reads q from the parameters after a coding or a media range, in thousandths: 1000 when
// there is none, 0 for one that does not parse (RFC 9110 section 12.4.2).
func weight(params []byte) int {
	for len(params) > 0 {
		var p []byte
		p, params, _ = bytes.Cut(params, []byte(";"))
		p = bytes.TrimSpace(p)
		if len(p) < 2 || (p[0] != 'q' && p[0] != 'Q') || p[1] != '=' {
			continue
		}
		v := p[2:]
		if len(v) == 0 || len(v) > 5 || (v[0] != '0' && v[0] != '1') {
			return 0
		}
		n := int(v[0]-'0') * 1000
		if len(v) > 1 {
			if v[1] != '.' {
				return 0
			}
			scale := 100
			for _, c := range v[2:] {
				if c < '0' || c > '9' {
					return 0
				}
				n += int(c-'0') * scale
				scale /= 10
			}
		}
		return min(n, 1000)
	}
	return 1000
}

// navigation reports whether a request for rel, which names no file, is a browser loading a page
// of the single-page application, which the Fallback answers.
func (st *staticRoute) navigation(hs []h1.Header, rel string) bool {
	if st.ownedAny {
		return false
	}
	if seg, _, _ := strings.Cut(rel, "/"); st.owned[seg] {
		return false
	}
	base := rel[strings.LastIndexByte(rel, '/')+1:]
	if strings.IndexByte(base, '.') >= 0 {
		return false // a missing file with an extension (app.js) is a 404, not the page
	}
	for i := range hs {
		if !bytes.EqualFold(hs[i].Name, []byte("accept")) {
			continue
		}
		for v := hs[i].Value; len(v) > 0; {
			var item []byte
			if c := bytes.IndexByte(v, ','); c >= 0 {
				item, v = v[:c], v[c+1:]
			} else {
				item, v = v, nil
			}
			mt, params, _ := bytes.Cut(item, []byte(";"))
			if bytes.EqualFold(bytes.TrimSpace(mt), []byte("text/html")) && weight(params) > 0 {
				return true
			}
		}
	}
	return false
}

// fingerprinted is the default rule of Static.Immutable: one part of the base name, between dots
// or dashes, looks like a content hash.
func fingerprinted(name string) bool {
	base := name[strings.LastIndexByte(name, '/')+1:]
	for part := range strings.FieldsFuncSeq(base, func(r rune) bool { return r == '.' || r == '-' }) {
		if len(part) < 8 || len(part) > 64 {
			continue
		}
		var digit, hexLetter, nonHex, upper, other bool
		for i := 0; i < len(part); i++ {
			switch c := part[i]; {
			case c >= '0' && c <= '9':
				digit = true
			case c >= 'a' && c <= 'f':
				hexLetter = true
			case c >= 'g' && c <= 'z' || c == '_':
				nonHex = true
			case c >= 'A' && c <= 'Z':
				upper = true
			default:
				other = true
			}
		}
		if other {
			continue
		}
		hexOnly := hexLetter && !nonHex && !upper
		if digit && (hexOnly || upper) {
			return true
		}
	}
	return false
}
