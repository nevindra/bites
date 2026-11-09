package bites

import (
	"bytes"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/reqctx"
	"github.com/nevindra/bites/internal/router"
	"github.com/nevindra/bites/internal/wire"
)

// reqState is the list of input errors one request collects. It is pooled because the JSON
// codec keeps the list it is given in its own pooled state, which makes a list on the stack
// escape; it never reaches user code (ADR 0008).
type reqState struct{ errs jsoncodec.Errors }

var statePool = sync.Pool{New: func() any { return new(reqState) }}

func getState() *reqState { return statePool.Get().(*reqState) }

func putState(st *reqState) {
	st.errs.Reset()
	statePool.Put(st)
}

// addError records one input error, within the shared cap of ADR 0012.
func addError(errs *jsoncodec.Errors, loc, msg string) { addRule(errs, loc, msg, "", 0, 0) }

// addRule is addError with the rule code of ADR 0012 and, for "length" and "range", its bounds.
func addRule(errs *jsoncodec.Errors, loc, msg, rule string, lo, hi int64) {
	if len(errs.List) >= maxErrors {
		errs.Truncated = true
		return
	}
	errs.List = append(errs.List, jsoncodec.FieldError{Location: loc, Message: msg, Rule: rule, Min: lo, Max: hi})
}

// addRequired records a member the request did not send.
func addRequired(errs *jsoncodec.Errors, loc string) {
	addRule(errs, loc, msgRequired, wire.RuleRequired, 0, 0)
}

// addText records the error a text decoder returned: its sentence and rule, or the generic
// sentence for an error that is not the wire's own.
func addText(errs *jsoncodec.Errors, loc string, err error) {
	var te *wire.TextError
	if errors.As(err, &te) {
		if te.Float || te.Format != "" {
			if len(errs.List) >= maxErrors {
				errs.Truncated = true
				return
			}
			errs.List = append(errs.List, jsoncodec.FieldError{Location: loc, Message: te.Error(), Rule: te.Rule, Min: te.Min, Float: te.Float, MinF: te.MinF, MaxF: te.MaxF, ExMin: te.ExMin, ExMax: te.ExMax, Format: te.Format})
			return
		}
		addRule(errs, loc, te.Error(), te.Rule, te.Min, te.Max)
		return
	}
	addRule(errs, loc, "expected a valid value", wire.RuleType, 0, 0)
}

const (
	msgRequired  = "required"
	msgRepeated  = "sent more than once"
	msgEscape    = "expected a valid percent-encoded value"
	msgNested    = "nested too deeply"
	msgUnsupport = "expected a JSON content type"
)

// decode fills the Input at in from the request, in the order of ADR 0048: path, query, header,
// cookie, then the body. Every mistake is collected and answered as one 400; the exceptions are
// the ones about the request's form (415, 413, 408), which are answered at once. It reports
// whether the handler should run.
func (p *inputPlan) decode(a *App, ex *engine.Exchange, ps *router.Params, in unsafe.Pointer, st *reqState, cfg *routeConfig) bool {
	head := ex.Head()
	slab := ex.Slab()
	errs := &st.errs
	if s := p.path; s != nil {
		decodePath(s, ps, in, slab, errs)
	}
	if s := p.query; s != nil {
		decodeQuery(s, head.RawQuery, in, slab, errs)
	}
	if s := p.header; s != nil {
		decodeHeaders(s, head, in, slab, errs)
	}
	if s := p.cookie; s != nil {
		decodeCookies(s, head, in, slab, errs)
	}
	if b := p.body; b != nil {
		before := len(errs.List)
		if !b.read(a, ex, in, errs, cfg) {
			return false
		}
		// A Submitted form keeps its own mistakes for the handler to show (ADR 0078), unless
		// another section has one: then the 400 lists them all.
		if b.sub && before == 0 {
			b.keepProblems(in, errs)
		}
	}
	if len(errs.List) > 0 {
		invalidInput(ex, errs)
		return false
	}
	return true
}

// Each section is decoded in a function of its own, never inlined: a textRun is a couple of
// hundred bytes of counters, and four of them in one frame would push every request's deepest
// call past the 2 KiB a goroutine starts with. A connection whose stack has grown keeps the bigger
// stack while it idles, which is memory per idle connection, a guarded axis (ADR 0003, ADR 0005).

//go:noinline
func decodePath(s *textSection, ps *router.Params, in unsafe.Pointer, slab *wire.Slab, errs *jsoncodec.Errors) {
	r := textRun{sec: s, base: unsafe.Add(in, s.off), slab: slab, errs: errs, phase: 1}
	for pi := 0; pi < ps.N && pi < len(s.pathMap); pi++ {
		r.offer(s.pathMap[pi], ps.Values[pi])
	}
}

//go:noinline
func decodeQuery(s *textSection, raw []byte, in unsafe.Pointer, slab *wire.Slab, errs *jsoncodec.Errors) {
	r := textRun{sec: s, base: unsafe.Add(in, s.off), slab: slab, errs: errs}
	for r.phase = 0; r.phase < 2; r.phase++ {
		if r.phase == 0 && s.lists == 0 {
			continue
		}
		wire.EachQuery(raw, r.queryPair)
	}
	r.finish()
}

//go:noinline
func decodeHeaders(s *textSection, head *h1.Request, in unsafe.Pointer, slab *wire.Slab, errs *jsoncodec.Errors) {
	r := textRun{sec: s, base: unsafe.Add(in, s.off), slab: slab, errs: errs}
	for r.phase = 0; r.phase < 2; r.phase++ {
		if r.phase == 0 && s.lists == 0 {
			continue
		}
		for i := range head.Headers {
			r.headerLine(&head.Headers[i])
		}
	}
	r.finish()
}

//go:noinline
func decodeCookies(s *textSection, head *h1.Request, in unsafe.Pointer, slab *wire.Slab, errs *jsoncodec.Errors) {
	r := textRun{sec: s, base: unsafe.Add(in, s.off), slab: slab, errs: errs}
	for r.phase = 0; r.phase < 2; r.phase++ {
		if r.phase == 0 && s.lists == 0 {
			continue
		}
		for i := range head.Headers {
			if h := &head.Headers[i]; equalFold(h.Name, "cookie") {
				r.cookieLine(h.Value)
			}
		}
	}
	r.finish()
}

// textRun walks one text section's source (the query, the headers, the cookies) and fills the
// Input. A list is filled in two passes so its backing array is made once, sized to what the
// request sent: the first pass counts, the second decodes (ADR 0003 counts that one allocation).
type textRun struct {
	sec    *textSection
	base   unsafe.Pointer
	slab   *wire.Slab
	errs   *jsoncodec.Errors
	phase  int
	seen   uint64
	dup    uint64
	counts [maxLists]int32
	filled [maxLists]int32
	bad    uint8 // lists whose Items bound failed: their values are not stored
	badEl  uint8 // lists with an item that did not decode, which Unique does not compare
	data   [maxLists]unsafe.Pointer
}

// offer hands the value raw to field idx. Percent-decoding goes into the slab, once.
func (r *textRun) offer(idx int, raw []byte) {
	f := &r.sec.fields[idx]
	if f.list >= 0 && r.phase == 0 {
		r.counts[f.list]++
		return
	}
	if r.phase == 0 {
		return
	}
	var s string
	switch r.sec.kind {
	case secPath:
		v, err := r.slab.Unescape(raw, false)
		if err != nil {
			addError(r.errs, f.loc, msgEscape)
			return
		}
		s = v
	case secQuery:
		v, err := r.slab.Unescape(raw, true)
		if err != nil {
			addError(r.errs, f.loc, msgEscape)
			return
		}
		s = v
	case secForm:
		// The body is this request's own and nothing reads it again: decode where it lies.
		v, err := wire.UnescapeInPlace(raw, true)
		if err != nil {
			addError(r.errs, f.loc, msgEscape)
			return
		}
		s = v
	default:
		s = r.slab.Copy(bytes.TrimSpace(raw))
	}
	bit := uint64(1) << idx
	if f.list < 0 {
		if r.seen&bit != 0 {
			if r.dup&bit == 0 {
				r.dup |= bit
				addError(r.errs, f.loc, msgRepeated)
			}
			return
		}
		r.seen |= bit
		if f.csv {
			r.csv(f, s)
			return
		}
		if err := f.dec(unsafe.Add(r.base, f.off), s); err != nil {
			addText(r.errs, f.loc, err)
		}
		return
	}
	li := f.list
	if r.bad&(1<<li) != 0 {
		return
	}
	if r.filled[li] == 0 {
		n := int(r.counts[li])
		if f.items != nil && !f.items.OK(n) {
			// Counted before anything is allocated or decoded, so a long list costs nothing.
			r.bad |= 1 << li
			r.seen |= bit
			addRule(r.errs, f.loc, f.items.Message, wire.RuleItems, int64(f.items.Min), int64(f.items.Max))
			return
		}
		// Grown in place: reflect.MakeSlice would also allocate the slice header it returns.
		v := reflect.NewAt(f.sliceT, unsafe.Add(r.base, f.listOff)).Elem()
		v.Grow(n)
		v.SetLen(n)
		r.data[li] = v.UnsafePointer()
		if f.hasFlag {
			*(*bool)(unsafe.Add(r.base, f.flagOff)) = true
		}
	}
	i := r.filled[li]
	r.filled[li]++
	r.seen |= bit
	if err := f.dec(unsafe.Add(r.data[li], uintptr(i)*f.elemSize), s); err != nil {
		r.badEl |= 1 << li
		addText(r.errs, f.loc+"["+strconv.Itoa(int(i))+"]", err)
	}
	if i+1 == r.counts[li] && r.badEl&(1<<li) == 0 {
		r.unique(f, r.data[li], int(i)+1)
	}
}

// unique reports the first of the n items at data that repeats an earlier one, when the field's
// type says its items are distinct (ADR 0021). It is asked once the list is complete and every
// item decoded, since an item that failed is left as zero.
func (r *textRun) unique(f *textField, data unsafe.Pointer, n int) {
	if f.items == nil || f.items.Dup == nil {
		return
	}
	if i := f.items.Dup(data, n); i >= 0 {
		addRule(r.errs, f.loc+"["+strconv.Itoa(i)+"]", wire.UniqueMessage, wire.RuleUnique, 0, 0)
	}
}

// csv fills a comma-separated list from its one value (ADR 0042): the commas are counted first,
// so the backing array is made once, as for a list of repeated keys, and the elements are cut
// from s without a []string in between. An empty value is an empty list, and an empty element
// is decoded as the empty string, not dropped. A header's elements are trimmed of spaces, as
// HTTP's list syntax allows around a comma.
func (r *textRun) csv(f *textField, s string) {
	n := 0
	if s != "" {
		n = strings.Count(s, ",") + 1
	}
	if f.items != nil && !f.items.OK(n) {
		addRule(r.errs, f.loc, f.items.Message, wire.RuleItems, int64(f.items.Min), int64(f.items.Max))
		return
	}
	v := reflect.NewAt(f.sliceT, unsafe.Add(r.base, f.listOff)).Elem()
	v.Grow(n)
	v.SetLen(n)
	if f.hasFlag {
		*(*bool)(unsafe.Add(r.base, f.flagOff)) = true
	}
	data := v.UnsafePointer()
	bad := false
	for i := 0; i < n; i++ {
		part := s
		if j := strings.IndexByte(s, ','); j >= 0 {
			part, s = s[:j], s[j+1:]
		}
		if r.sec.kind == secHeader {
			part = strings.Trim(part, " \t")
		}
		if err := f.dec(unsafe.Add(data, uintptr(i)*f.elemSize), part); err != nil {
			bad = true
			addText(r.errs, f.loc+"["+strconv.Itoa(i)+"]", err)
		}
	}
	if !bad {
		r.unique(f, data, n)
	}
}

// finish fills a field the request did not send from its default (ADR 0041) and reports the
// required ones with none (ADR 0041).
func (r *textRun) finish() {
	for i := range r.sec.fields {
		f := &r.sec.fields[i]
		if r.seen&(1<<i) != 0 {
			continue
		}
		if f.hasDef {
			// The default is text, decoded as a sent value would be: no value is shared between
			// requests and the type's own rules run once more (ADR 0041).
			if err := f.dec(unsafe.Add(r.base, f.off), f.def); err != nil {
				addText(r.errs, f.loc, err)
			}
			continue
		}
		if f.required {
			addRequired(r.errs, f.loc)
		}
	}
}

func (r *textRun) queryPair(key, value []byte) bool {
	var idx int
	var ok bool
	var name string
	if bytes.IndexByte(key, '%') < 0 && bytes.IndexByte(key, '+') < 0 {
		if idx, ok = r.sec.lookup(key); !ok && r.sec.strict && r.phase == 1 {
			name = string(key)
		}
	} else if k, err := r.slab.Unescape(key, true); err == nil {
		idx, ok = r.sec.lookup(unsafe.Slice(unsafe.StringData(k), len(k)))
		name = k
	} else if r.sec.strict && r.phase == 1 {
		addError(r.errs, "query", msgEscape)
	}
	switch {
	case ok:
		r.offer(idx, value)
	case r.sec.strict && r.phase == 1 && name != "" && !slices.Contains(r.sec.elsewhere, name):
		// Reported in the second pass only, so a list's count pass does not report it twice.
		// The key's name is the one thing echoed, cut to 64 bytes as a form's is (ADR 0012).
		key := clipKey(name)
		addError(r.errs, "query."+key, r.sec.unknownQueryMessage(key))
	}
	return true
}

// headerLine matches a header field against the section's names without allocating: the name is
// lower-cased into a stack buffer and the map is looked up with it.
func (r *textRun) headerLine(h *h1.Header) {
	var buf [64]byte
	if len(h.Name) > len(buf) {
		return
	}
	for i, c := range h.Name {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		buf[i] = c
	}
	if idx, ok := r.sec.lookup(buf[:len(h.Name)]); ok {
		r.offer(idx, h.Value)
	}
}

// cookieLine reads one Cookie header: name=value pairs separated by semicolons, a value
// optionally in double quotes (RFC 6265). Values are not percent-decoded. The pair parsing is
// nextCookie, which Request.Cookie shares.
func (r *textRun) cookieLine(line []byte) {
	for len(line) > 0 {
		var name, val []byte
		var ok bool
		name, val, line, ok = nextCookie(line)
		if !ok {
			continue
		}
		if idx, found := r.sec.lookup(name); found {
			r.offer(idx, val)
		}
	}
}

// nextCookie takes the first name=value pair off a Cookie header's value and returns the rest
// after its semicolon. ok is false for a malformed pair (no '=', or an empty name), which the
// caller skips; rest is still where to go on from. White space (space, tab, CR, LF, VT, FF)
// around the pair is trimmed, and one pair of double quotes around the value is removed. It
// serves both the Cookie section (bytes) and Request.Cookie (a string), so the two cannot
// disagree on what a header holds.
func nextCookie[T ~string | ~[]byte](line T) (name, val, rest T, ok bool) {
	pair := line
	for i := 0; i < len(line); i++ {
		if line[i] == ';' {
			pair, rest = line[:i], line[i+1:]
			break
		}
	}
	if len(pair) == len(line) {
		rest = line[len(line):]
	}
	for len(pair) > 0 && isCookieSpace(pair[0]) {
		pair = pair[1:]
	}
	for len(pair) > 0 && isCookieSpace(pair[len(pair)-1]) {
		pair = pair[:len(pair)-1]
	}
	eq := -1
	for i := 0; i < len(pair); i++ {
		if pair[i] == '=' {
			eq = i
			break
		}
	}
	if eq <= 0 {
		return name, val, rest, false
	}
	name, val = pair[:eq], pair[eq+1:]
	if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
		val = val[1 : len(val)-1]
	}
	return name, val, rest, true
}

func isCookieSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f'
}

func equalFold(b []byte, lower string) bool {
	if len(b) != len(lower) {
		return false
	}
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lower[i] {
			return false
		}
	}
	return true
}

// read reads the body and fills the Body section (ADR 0044). It reports false when it has
// already answered: 415, or the 413, 408 or 400 of a body that could not be read.
func (b *bodyPlan) read(a *App, ex *engine.Exchange, in unsafe.Pointer, errs *jsoncodec.Errors, cfg *routeConfig) bool {
	if b.kind == bodyUpload {
		return b.readUpload(a, ex, in, errs, cfg)
	}
	if b.kind == bodyReader {
		return b.readBodyReader(ex, in, errs, cfg)
	}
	if b.kind == bodyMultipart {
		// The other sections are decoded first, and a mistake in one is answered before a byte of
		// the body is read (so a client that sent Expect: 100-continue never sends it).
		if len(errs.List) > 0 {
			return true
		}
		return b.readMultipart(a, ex, in, errs, cfg)
	}
	head := ex.Head()
	if b.kind == bodyForm {
		return b.readForm(ex, in, errs, cfg, a.limits.MaxFields)
	}
	hasBody := head.Chunked || head.ContentLength > 0
	if b.kind != bodyBytes {
		if !hasBody {
			if !b.opt {
				addRequired(errs, "body")
			}
			return true
		}
		if !isJSONContentType(head) {
			problem(ex, 415, "the body must be sent as application/json")
			return false
		}
	}
	body, err := ex.ReadBody(cfg.maxBody, cfg.bodyTimeout)
	if err != nil {
		var he *h1.Error
		if errors.As(err, &he) {
			problem(ex, he.Status, he.Reason)
		} else {
			problem(ex, 400, "")
		}
		return false
	}
	dst := unsafe.Add(in, b.off)
	if b.kind == bodyBytes {
		// Capped at its length: the slice shares the slab, and a handler that appends must not
		// write into the text after it (ADR 0008).
		*(*[]byte)(dst) = body[:len(body):len(body)]
		return true
	}
	val := dst
	if b.kind == bodyRaw {
		val = unsafe.Add(dst, b.valOff)
		*(*[]byte)(unsafe.Add(dst, b.rawOff)) = body[:len(body):len(body)]
	}
	if b.opt {
		*(*bool)(unsafe.Add(in, b.optOff)) = true
	}
	n0 := len(errs.List)
	err = b.dec.Decode(body, val, ex.Slab(), errs)
	for i := n0; i < len(errs.List); i++ {
		errs.List[i].Location = bodyLocation(errs.List[i].Location)
	}
	if err != nil {
		var se *jsoncodec.SyntaxError
		switch {
		case errors.As(err, &se):
			addError(errs, "body", se.Error())
		case errors.Is(err, jsoncodec.ErrDepth):
			addError(errs, "body", msgNested)
		default:
			addError(errs, "body", "expected valid JSON")
		}
	}
	return true
}

// bodyLocation prefixes a location relative to the body root with the section's name.
func bodyLocation(loc string) string {
	switch {
	case loc == "":
		return "body"
	case loc[0] == '[':
		return "body" + loc
	}
	return "body." + loc
}

// isJSONContentType accepts application/json and application/*+json, any case, with parameters
// ignored. A missing Content-Type is not accepted (ADR 0044).
func isJSONContentType(head *h1.Request) bool {
	for i := range head.Headers {
		h := &head.Headers[i]
		if !equalFold(h.Name, "content-type") {
			continue
		}
		v := h.Value
		if i := bytes.IndexByte(v, ';'); i >= 0 {
			v = v[:i]
		}
		v = bytes.TrimSpace(v)
		if string(v) == "application/json" { // what nearly every client sends
			return true
		}
		var buf [64]byte
		if len(v) > len(buf) {
			return false
		}
		for j, c := range v {
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			buf[j] = c
		}
		s := buf[:len(v)]
		const prefix = "application/"
		if !bytes.HasPrefix(s, []byte(prefix)) {
			return false
		}
		sub := s[len(prefix):]
		return string(sub) == "json" || (len(sub) > len("+json") && bytes.HasSuffix(sub, []byte("+json")))
	}
	return false
}

// answerError writes the answer for an error a handler returned (ADR 0012, ADR 0049). A located
// error (Invalid) anywhere in the chain makes it the 400 that lists them, as from Validate, because
// a handler that finds a field wrong only after a lookup must answer as the decoder would. Otherwise
// the status is looked for through the whole chain (findStatus: a *StatusError or a HasStatus); when
// the chain has none, the app's error mappers are asked (WithErrorMapper); anything still without
// one is a 500 with no message, logged with the request.
func (a *App) answerError(ex *engine.Exchange, err error) {
	if ex.Context().Cause() == reqctx.ErrClientGone {
		// Nobody is waiting. The engine writes its own 500 if nothing is set; to a closed
		// connection that is a failed write and nothing else.
		return
	}
	ex.NoteError(err) // told to the Observers as Outcome.Err (ADR 0051)
	if a.answerLocated(ex, err) {
		return
	}
	if se := findStatus(err); se != nil {
		a.statusAnswer(ex, se)
		return
	}
	if se := a.mapError(err); se != nil {
		if se.Status < 400 || se.Status > 599 {
			a.log.Error("bites: an error mapper answered with a status that is not an error status; the answer is a 500", "err", err, "status", se.Status)
			problem(ex, 500, "")
			return
		}
		if se.Status >= 500 {
			// A mapped 4xx is the client's mistake and the mapper has said so; a mapped 5xx is ours,
			// and the real reason must not be lost behind the mapper's message.
			h := ex.Head()
			a.log.Error("bites: handler error", "err", err, "status", se.Status, "method", string(h.Method), "path", string(h.Path))
		}
		a.statusAnswer(ex, se)
		return
	}
	a.internalError(ex, err)
}

// internalError is the 500 with no detail, and the log line that carries the real reason.
func (a *App) internalError(ex *engine.Exchange, err error) {
	h := ex.Head()
	a.log.Error("bites: handler error", "err", err, "method", string(h.Method), "path", string(h.Path))
	problem(ex, 500, "")
}
