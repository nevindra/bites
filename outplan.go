package bites

import (
	"context"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"unsafe"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/jsoncodec"
)

type wrapKind uint8

const (
	wrapNone wrapKind = iota // a plain value: 200, JSON
	wrapOK
	wrapCreated
	wrapAccepted
	wrapNoContent
	wrapNotModified
	wrapSeeOther
	wrapRedirect
	wrapText
	wrapBytes
	wrapPage   // Page: HTML for a browser, with its own status (ADR 0078)
	wrapFile   // File: a body of a known length from a file (ADR 0043)
	wrapStream // Stream, SSE and Events, last: wk >= wrapStream is "a stream" (ADR 0030)
	wrapSSE    // wk >= wrapSSE is "server-sent events"
	wrapEvents
)

var wrapStatus = [...]int{wrapOK: 200, wrapCreated: 201, wrapAccepted: 202, wrapNoContent: 204, wrapNotModified: 304, wrapSeeOther: 303, wrapText: 200, wrapBytes: 200, wrapPage: 200}

// outPlan is how an Output type becomes a response (ADR 0009, ADR 0013). A wrapper's status,
// headers and cookies are read at fixed offsets and its body is encoded by a plan compiled for
// the type inside it, so a wrapper costs what a plain value costs. A handler that returns an
// interface (several success statuses) has one plan per type its route declared with Returns,
// all compiled at Registration.
type outPlan struct {
	dynamic  bool
	variants []variant // dynamic: the types the route declared with Returns
	alts     []alt     // a OneOf: its variants, in order (oneof.go)
	oneOf    string    // a OneOf: its name, OneOf2 to OneOf4
	wk       wrapKind
	enc      *jsoncodec.Encoder // the value, or a wrapper's Body, or an Events' T; nil when there is no body
	// sizeHint is the length of this route's last large JSON body, with an eighth of slack, or 0:
	// the next answer reserves it at once rather than growing by doubling (engine.Response.Reserve).
	sizeHint atomic.Int64
	events   eventsType // Events[T]: builds its runner and names T
	bodyOff  uintptr
	hdrOff   uintptr
	cookOff  uintptr
	locOff   uintptr
	statOff  uintptr
	ctOff    uintptr // Bytes: where the content type is
	hasLoc   bool
	mustLoc  bool    // SetsLocation: an empty Location is a logged 500, and the document requires the header
	funcOff  uintptr // a stream's function
	liveOff  uintptr // a Stream's Live flag
	rootOff  uintptr // File: the os.Root
	fsOff    uintptr // File: the fs.FS
	nameOff  uintptr // File: the name inside either
	attOff   uintptr // File, Bytes, Stream: the Attachment name
	hasAtt   bool
}

var wrapperNames = []struct {
	prefix string
	kind   wrapKind
}{
	{"OK[", wrapOK}, {"Created[", wrapCreated}, {"Accepted[", wrapAccepted},
	{"NoContent", wrapNoContent}, {"NotModified", wrapNotModified},
	{"SeeOther", wrapSeeOther}, {"Redirect", wrapRedirect}, {"Text", wrapText}, {"Bytes", wrapBytes}, {"Page", wrapPage},
	{"File", wrapFile}, {"Stream", wrapStream}, {"SSE", wrapSSE}, {"Events[", wrapEvents},
}

// compileOutput builds the plan for the Output type t.
func (a *App) compileOutput(t reflect.Type) (*outPlan, error) {
	if t.Kind() == reflect.Interface {
		return &outPlan{dynamic: true}, nil
	}
	p := &outPlan{}
	if t.Kind() == reflect.Pointer && isOneOf(t.Elem()) {
		return nil, refusalf("the handler returns a pointer to a %s; a OneOf is returned by value, its variant chosen with the method of its letter", oneOfName(t.Elem()))
	}
	if t.Kind() == reflect.Pointer && isFileType(t.Elem()) {
		return nil, refusalf("the handler returns *File; a File is returned by value, because the answer is built from its fields once")
	}
	if t.Kind() == reflect.Pointer && isStreamType(t.Elem()) {
		return nil, refusalf("the handler returns *%s; a stream is returned by value, because its function and its writer live for one response", streamLabel(t.Elem()))
	}
	opts := jsoncodec.Options{Names: a.naming, Formats: a.formats, JSONSchemas: a.jsonSchemas}
	encode := func(et reflect.Type) (*jsoncodec.Encoder, error) {
		if o := findOneOf(et, map[reflect.Type]bool{}); o != nil {
			return nil, refusalf("the output type %s holds a %s; a OneOf is the handler's whole return type, not a value written as JSON", typeLabel(et), oneOfName(o))
		}
		e, err := jsoncodec.CompileEncoder(et, opts)
		if err != nil {
			return nil, refusalf("the output type cannot be written as JSON: %v", err)
		}
		return e, nil
	}
	if t.Kind() == reflect.Struct && t.PkgPath() == ownPkg {
		for _, w := range wrapperNames {
			if t.Name() != w.prefix && !(strings.HasSuffix(w.prefix, "[") && strings.HasPrefix(t.Name(), w.prefix)) {
				continue
			}
			p.wk = w.kind
			if w.kind == wrapStream || w.kind == wrapSSE {
				f, _ := t.FieldByName("Func")
				p.funcOff = f.Offset
				if f, ok := t.FieldByName("Live"); ok {
					p.liveOff = f.Offset
				}
			}
			if f, ok := t.FieldByName("Header"); ok {
				p.hdrOff = f.Offset
			}
			if f, ok := t.FieldByName("Cookies"); ok {
				p.cookOff = f.Offset
			}
			if f, ok := t.FieldByName("Attachment"); ok {
				p.attOff, p.hasAtt = f.Offset, true
			}
			if w.kind == wrapEvents {
				if err := a.compileEvents(p, t); err != nil {
					return nil, err
				}
				return p, nil
			}
			if f, ok := t.FieldByName("Location"); ok {
				p.locOff, p.hasLoc = f.Offset, true
			}
			if f, ok := t.FieldByName("Status"); ok {
				p.statOff = f.Offset
			}
			if w.kind == wrapFile {
				for name, off := range map[string]*uintptr{"Root": &p.rootOff, "FS": &p.fsOff, "Name": &p.nameOff, "ContentType": &p.ctOff} {
					f, _ := t.FieldByName(name)
					*off = f.Offset
				}
				return p, nil
			}
			if w.kind == wrapText || w.kind == wrapBytes || w.kind == wrapPage {
				// A body that is written as it is, not encoded: no codec plan.
				f, _ := t.FieldByName("Body")
				p.bodyOff = f.Offset
				if cf, ok := t.FieldByName("ContentType"); ok {
					p.ctOff = cf.Offset
				}
				return p, nil
			}
			if f, ok := t.FieldByName("Body"); ok {
				if isOneOf(f.Type) {
					return nil, refusalf("%s is the Body of %s; a OneOf is the whole return type, and each of its variants is a wrapper of its own", oneOfName(f.Type), strings.SplitN(t.Name(), "[", 2)[0])
				}
				if isPageType(f.Type) {
					return nil, refusalf("Page is the Body of %s; a Page is the whole return type, not the body of a wrapper (its status, headers and cookies are its own fields)", strings.SplitN(t.Name(), "[", 2)[0])
				}
				if isFileType(f.Type) {
					return nil, refusalf("File is the Body of %s; a File is the whole return type, not the body of a wrapper (its status and headers are its own fields)", strings.SplitN(t.Name(), "[", 2)[0])
				}
				if isStreamType(f.Type) {
					return nil, refusalf("%s is the Body of %s; a stream is the whole return type, not the body of a wrapper (its status and headers are its own fields)", streamLabel(f.Type), strings.SplitN(t.Name(), "[", 2)[0])
				}
				p.bodyOff = f.Offset
				e, err := encode(f.Type)
				if err != nil {
					return nil, err
				}
				p.enc = e
			}
			return p, nil
		}
	}
	e, err := encode(t)
	if err != nil {
		return nil, err
	}
	p.enc = e
	return p, nil
}

// variant is one type a route declared with Returns (ADR 0013): its plan, found at run time by
// comparing the dynamic type of the interface the handler returned.
type variant struct {
	t        reflect.Type
	ptr      reflect.Type // *t, which a handler may return as well
	plan     *outPlan
	indirect bool // the interface holds a pointer to a copy of the value, not the value itself
}

// writeDynamic answers with a value a handler returned as an interface. The plan is found in the
// route's own list of declared types; a type the route did not declare is a logged 500, never
// compiled on the fly. A value the interface holds by pointer is written from that pointer, with
// no copy of it made (ADR 0003, ADR 0013).
func (p *outPlan) writeDynamic(a *App, ex *engine.Exchange, v any, ctx context.Context) {
	if v == nil {
		a.internalError(ex, refusalf("the handler returned a nil interface and no error"))
		return
	}
	t := reflect.TypeOf(v)
	data := (*[2]unsafe.Pointer)(unsafe.Pointer(&v))[1]
	for i := range p.variants {
		vr := &p.variants[i]
		switch t {
		case vr.t:
			if vr.indirect {
				vr.plan.write(a, ex, data, ctx)
			} else {
				c := reflect.New(t)
				c.Elem().Set(reflect.ValueOf(v))
				vr.plan.write(a, ex, c.UnsafePointer(), ctx)
			}
		case vr.ptr:
			if data == nil {
				a.internalError(ex, refusalf("the handler returned a nil pointer and no error"))
				return
			}
			vr.plan.write(a, ex, data, ctx)
		default:
			continue
		}
		runtime.KeepAlive(v)
		return
	}
	a.internalError(ex, refusalf("the handler returned %s, which its route does not declare: add Returns[%s]() to the route's options", t, t))
}

// isIndirect reports whether an interface holding a T keeps a pointer to a boxed copy. A boxed
// zero value of a type held directly (a pointer, a map, a struct of one of them) has a nil data
// word; any other has the address of its box.
func isIndirect(t reflect.Type) bool {
	v := reflect.Zero(t).Interface()
	return (*[2]unsafe.Pointer)(unsafe.Pointer(&v))[1] != nil
}

// compileReturns reads the Returns of a route against its output type (ADR 0013): an interface
// output needs each type it may hold declared, and nothing else may declare any.
func (a *App) compileReturns(out *outPlan, t reflect.Type, returns []reflect.Type, method string) error {
	if t.Kind() != reflect.Interface {
		if len(returns) > 0 && isOneOf(t) {
			return refusalf("Returns[%s] is given, but the handler returns a %s, whose type arguments already say what it may return: drop the Returns", returns[0], oneOfName(t))
		}
		if len(returns) > 0 {
			return refusalf("Returns[%s] is given, but the handler returns %s, which is not an interface: Returns declares what an interface return may hold", returns[0], t)
		}
		return nil
	}
	if len(returns) == 0 {
		return refusalf("the handler returns %s, an interface, and the route has no Returns: declare each type it may return with Returns[T](), so the API description has its statuses and a wrong type is refused now and not answered with a 500", t)
	}
	for _, rt := range returns {
		switch {
		case rt.Kind() == reflect.Interface:
			return refusalf("Returns[%s] names an interface; declare the concrete types the handler returns", rt)
		case isOneOf(rt):
			return refusalf("Returns[%s] names a OneOf; make the %s the handler's return type, which needs no Returns", rt, oneOfName(rt))
		case rt.Kind() == reflect.Pointer:
			return refusalf("Returns[%s] names a pointer; declare %s, which the handler may return by value or by pointer", rt, rt.Elem())
		case !reflect.PointerTo(rt).Implements(t):
			return refusalf("Returns[%s]: %s does not implement %s, the type the handler returns", rt, rt, t)
		}
		p, err := a.compileOutput(rt)
		if err != nil {
			return refusalf("Returns[%s]: %v", rt, err)
		}
		if p.wk >= wrapStream && method == "HEAD" {
			return refusalf("Returns[%s]: a HEAD route cannot return a stream: its answer has no body to stream", rt)
		}
		out.variants = append(out.variants, variant{t: rt, ptr: reflect.PointerTo(rt), plan: p, indirect: isIndirect(rt)})
	}
	return nil
}

// markLocation sets mustLoc on every Created plan of the output and reports whether there was
// one: a route can return a Created plainly, as a variant of a OneOf, or as a type declared with
// Returns.
func (p *outPlan) markLocation() bool {
	found := false
	if p.wk == wrapCreated && p.hasLoc {
		p.mustLoc, found = true, true
	}
	for i := range p.variants {
		found = p.variants[i].plan.markLocation() || found
	}
	for i := range p.alts {
		found = p.alts[i].plan.markLocation() || found
	}
	return found
}

// write answers with the value at src, a *T of the type the plan was compiled for. ctx is the
// context the handler received, which a stream's function receives too.
func (p *outPlan) write(a *App, ex *engine.Exchange, src unsafe.Pointer, ctx context.Context) {
	if p.alts != nil {
		p.writeOneOf(a, ex, src, ctx)
		return
	}
	if p.wk == wrapFile {
		p.writeFile(a, ex, src)
		return
	}
	if p.wk >= wrapStream {
		p.writeStream(a, ex, src, ctx)
		return
	}
	status := 200
	if p.wk != wrapNone {
		if p.wk == wrapRedirect {
			status = *(*int)(unsafe.Add(src, p.statOff))
			switch status {
			case 301, 302, 307, 308:
			default:
				a.internalError(ex, refusalf("a Redirect has status %d; it must be 301, 302, 307 or 308", status))
				return
			}
		} else if p.wk == wrapPage {
			st, why := pageStatus(*(*int)(unsafe.Add(src, p.statOff)))
			if why != "" {
				a.internalError(ex, refusalf("%s", why))
				return
			}
			if hasContentType(*(*Header)(unsafe.Add(src, p.hdrOff))) {
				a.internalError(ex, refusalf("a Page has a Content-Type in its Header, but a Page is text/html; send another media type as a Bytes"))
				return
			}
			status = st
		} else {
			status = wrapStatus[p.wk]
		}
	}
	raw := p.wk == wrapText || p.wk == wrapBytes || p.wk == wrapPage
	ctype := "text/plain; charset=utf-8"
	if p.wk == wrapPage {
		ctype = pageType
	}
	if p.wk == wrapBytes {
		ctype = *(*string)(unsafe.Add(src, p.ctOff))
		if !validContentType(ctype) {
			a.internalError(ex, refusalf("a Bytes has the content type %q, which is not type/subtype", ctype))
			return
		}
	}
	r := ex.Respond(status)
	jsonType := true
	if p.wk != wrapNone {
		hdr := *(*Header)(unsafe.Add(src, p.hdrOff))
		jsonType = p.enc != nil || raw
		if err := writeHeaders(r, hdr, &jsonType); err != nil {
			a.internalError(ex, err)
			return
		}
		if p.hasAtt {
			writeAttachment(r, hdr, *(*string)(unsafe.Add(src, p.attOff)))
		}
		if p.hasLoc {
			if loc := *(*string)(unsafe.Add(src, p.locOff)); loc != "" {
				r.Header("Location", loc)
			} else if p.mustLoc {
				a.internalError(ex, refusalf("a Created has an empty Location, but its route is registered with SetsLocation and the API document promises the header: set Location, or drop SetsLocation"))
				return
			}
		}
		for _, c := range *(*[]Cookie)(unsafe.Add(src, p.cookOff)) {
			v, err := appendSetCookie(nil, &c)
			if err != nil {
				a.internalError(ex, err)
				return
			}
			r.Header("Set-Cookie", string(v))
		}
	}
	if raw {
		if jsonType {
			r.Header("Content-Type", ctype)
		}
		body := r.Body()
		if p.wk == wrapText {
			r.Reserve(len(*(*string)(unsafe.Add(src, p.bodyOff))))
			*body = append(*body, *(*string)(unsafe.Add(src, p.bodyOff))...)
		} else { // Bytes, Page
			r.Reserve(len(*(*[]byte)(unsafe.Add(src, p.bodyOff))))
			*body = append(*body, *(*[]byte)(unsafe.Add(src, p.bodyOff))...)
		}
		return
	}
	if p.enc == nil {
		return
	}
	if jsonType {
		r.Header("Content-Type", "application/json")
	}
	if h := p.sizeHint.Load(); h > 0 {
		r.Reserve(int(h))
	}
	body := r.Body()
	start := len(*body)
	nb, err := p.enc.Append(*body, unsafe.Add(src, p.bodyOff))
	if err != nil {
		a.internalError(ex, err)
		return
	}
	*body = nb
	p.noteSize(len(nb) - start)
}

// largeBody is the size from which a JSON body's length is remembered: the scratch's own buffer
// holds anything smaller.
const largeBody = 32 << 10

// noteSize keeps the hint near the length of the last body, written only when it moves by more
// than an eighth, so a route whose answers are alike does not write the shared word each time.
func (p *outPlan) noteSize(n int) {
	h := p.sizeHint.Load()
	want := int64(0)
	if n >= largeBody {
		want = int64(n + n/8)
	}
	if d := want - h; d > h/8 || -d > h/8 {
		p.sizeHint.Store(want)
	}
}

// hasContentType reports whether h sets Content-Type, by any case of the name.
func hasContentType(h Header) bool {
	for n := range h {
		if strings.EqualFold(n, "Content-Type") {
			return true
		}
	}
	return false
}

// answersPage reports whether the plan, or one of its variants, is a Page.
func (p *outPlan) answersPage() bool {
	if p.wk == wrapPage {
		return true
	}
	for i := range p.variants {
		if p.variants[i].plan.answersPage() {
			return true
		}
	}
	for i := range p.alts {
		if p.alts[i].plan.answersPage() {
			return true
		}
	}
	return false
}

// writeHeaders writes a wrapper's Header in name order. Names the engine owns are skipped, and a
// Content-Type of the handler's replaces the JSON one (jsonType is then cleared).
func writeHeaders(r *engine.Response, h Header, jsonType *bool) error {
	if len(h) == 0 {
		return nil
	}
	names := make([]string, 0, len(h))
	for n := range h {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		switch strings.ToLower(n) {
		case "content-length", "transfer-encoding", "connection", "date":
			continue
		case "content-type":
			*jsonType = false
		}
		for _, v := range h[n] {
			r.Header(n, v)
		}
	}
	return nil
}
