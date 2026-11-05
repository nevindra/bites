package bites

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unsafe"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/mpart"
	"github.com/nevindra/bites/internal/wire"
)

// Multipart is a Body read as multipart/form-data and decoded into T before the handler runs
// (ADR 0044). T is a struct whose fields are the form's fields: a text field is decoded exactly
// like a Form field (ADR 0042), and a file field is a FormFile, an Opt of one, or a slice of one.
//
//	type NewListing struct {
//		Body bites.Multipart[struct {
//			Title   bites.NonEmpty
//			Cover   Photo            // exactly one file, required
//			Gallery []Photo          // zero or more files under one name
//			Floor   bites.Opt[Photo] // at most one file
//		}]
//	}
//
// Choose Upload instead when the handler must see the bytes while they arrive (a file larger than
// it can keep, a signature checked on the way); choose Multipart when it wants the fields and the
// files together as values. A Multipart route must be given MaxBody, on the route or on a group
// above it; the most a route may receive is the application's decision, so there is no default and
// a route without one is refused at Registration. A mistake is located as body.title, a list
// element as body.tags[1], a file as body.cover. Every FormFile in Value is valid only while the
// handler runs.
type Multipart[T any] struct {
	// Value is the form decoded into T.
	Value T
}

// FormFile is one file received in a Multipart body. It is the type of a file field, or the
// type a named struct embeds when the field must say what it accepts: a method Accept() []string
// on that type lists the media types allowed (image/png, image/*), and MaxSize() int64 the most
// bytes of the file. A bare FormFile accepts any type and is bounded by the route's MaxBody.
//
//	type Photo struct{ bites.FormFile }
//
//	func (Photo) Accept() []string { return []string{"image/png", "image/jpeg"} }
//	func (Photo) MaxSize() int64   { return 5 << 20 }
//
// The media type Accept checks is the one the client declared for the part, which is data and can
// lie: it guards against a mistake, not an attacker. A handler that must know a file is an image
// reads its first bytes.
//
// A FormFile is valid only while the handler runs. Once it has returned, Open and every reader
// from it fail with ErrUploadClosed, whether the file lives in memory or on disk, so a goroutine
// that kept one fails the same way for a small file and a large one; a handler that wants the
// file after it returns copies it first. The zero FormFile (an Opt that was not sent) has no
// file: its Open fails.
type FormFile struct {
	// Name is the form field the file was sent under, as the Input names it.
	Name string
	// FileName is the file name the client sent, as data: never a path, and never opened by the
	// framework (see Part).
	FileName string
	// ContentType is the media type the client declared for the part, as data.
	ContentType string
	// Size is the bytes actually received, not the bytes declared.
	Size int64
	f    *engine.UploadFile
}

var errNoFile = errors.New("bites: FormFile.Open on a FormFile that holds no file; an Opt[FormFile] that was not sent is the zero value")

// Open returns an independent reader over the file each time it is called. The reader is valid
// while the handler runs; after that its Read returns ErrUploadClosed.
func (f FormFile) Open() (io.ReadSeekCloser, error) {
	if f.f == nil {
		return nil, errNoFile
	}
	return f.f.Open()
}

var formFileType = reflect.TypeFor[FormFile]()

// fileKind is how a field holds files.
type fileKind uint8

const (
	fileNone fileKind = iota
	fileOne           // F: exactly one, required
	fileOpt           // Opt[F]: at most one
	fileMany          // []F: any number under one name
)

// fileSpec is what Registration found about a file field.
type fileSpec struct {
	kind    fileKind
	elem    reflect.Type // F, the field's file type
	accept  []string     // lower-cased media types the type allows; nil when any
	maxSize int64        // most bytes; 0 when the type does not say (MaxBody alone)
	flagOff uintptr      // Opt: where the presence flag is, relative to the section
	valOff  uintptr      // Opt: where the file is, relative to the section
	elemSz  uintptr
}

// isFormFileType reports whether t is FormFile or a struct that embeds one.
func isFormFileType(t reflect.Type) bool {
	if t == formFileType {
		return true
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := range t.NumField() {
		if f := t.Field(i); f.Anonymous && f.Type == formFileType {
			return true
		}
	}
	return false
}

// holdsFile reports whether t is, or holds at any depth, a file type.
func holdsFile(t reflect.Type, seen map[reflect.Type]bool) bool {
	if isFormFileType(t) {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return holdsFile(t.Elem(), seen)
	case reflect.Map:
		return holdsFile(t.Key(), seen) || holdsFile(t.Elem(), seen)
	case reflect.Struct:
		for i := range t.NumField() {
			if holdsFile(t.Field(i).Type, seen) {
				return true
			}
		}
	}
	return false
}

// firstFile returns the Go path of the first file field inside t, or "".
func firstFile(path string, t reflect.Type, seen map[reflect.Type]bool) string {
	if isFormFileType(t) {
		return path
	}
	if seen[t] {
		return ""
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return firstFile(path, t.Elem(), seen)
	case reflect.Map:
		return firstFile(path, t.Elem(), seen)
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			if p := firstFile(path+"."+f.Name, f.Type, seen); p != "" {
				return p
			}
		}
	}
	return ""
}

func isMultipart(t reflect.Type) bool { return isOwn(t, "Multipart[") }

// containsMultipart reports whether t is, or holds at any depth, a Multipart.
func containsMultipart(t reflect.Type, seen map[reflect.Type]bool) bool {
	if isMultipart(t) {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return containsMultipart(t.Elem(), seen)
	case reflect.Map:
		return containsMultipart(t.Key(), seen) || containsMultipart(t.Elem(), seen)
	case reflect.Struct:
		for i := range t.NumField() {
			if containsMultipart(t.Field(i).Type, seen) {
				return true
			}
		}
	}
	return false
}

// submittedHasFile reports whether the form struct of a Submitted[T] has a file field.
func submittedHasFile(t reflect.Type) bool {
	return holdsFile(t.Field(0).Type, map[reflect.Type]bool{})
}

const (
	msgFileNeedsName   = "a file part needs a filename"
	msgTextSentAsFile  = "a text field was sent as a file"
	msgMultipartType   = "the body must be sent as multipart/form-data"
	fileRefusalTail    = "a file field is a FormFile, an Opt of one, or a slice of one"
	fileHeaderSentence = "(bites has no multipart.FileHeader; FormFile.Open returns the reader)"
)

// multipartBody compiles a Multipart[T], or a Submitted[T] whose T has a file field: T becomes a
// section whose location prefix is body, with files beside the text.
func (c *compiler) multipartBody(goPath string, t reflect.Type, off uintptr) (*bodyPlan, error) {
	kind := "Multipart"
	if isSubmitted(t) {
		kind = "Submitted"
	}
	vt := t.Field(0).Type
	if vt.Kind() != reflect.Struct {
		return nil, refusalf("%s is a %s of %s, but a %s wraps a struct whose fields are the form fields and files", goPath, kind, typeLabel(vt), kind)
	}
	if k, _, _, _ := wire.PresenceOf(vt); k != wire.None {
		return nil, refusalf("%s is a %s of %s, but a %s wraps a struct whose fields are the form fields and files", goPath, kind, typeLabel(vt), kind)
	}
	if !c.cfg.maxBodySet {
		return nil, refusalf("%s has no MaxBody: how much a route may receive is the application's decision (an avatar wants 2 MiB, a video 2 GiB), so there is no default; give the route or its group MaxBody", goPath)
	}
	s, err := c.section(secMultipart, goPath+".Value", vt, off+t.Field(0).Offset)
	if err != nil {
		return nil, err
	}
	s.unknown = c.cfg.unknown
	b := &bodyPlan{kind: bodyMultipart, off: off, form: s}
	if kind == "Submitted" {
		b.sub, b.subOff = true, t.Field(1).Offset
	}
	return b, nil
}

// fileField compiles a field of a Multipart section that holds files, or reports ok false for a
// field that is text. A field of a type that cannot hold a file the framework would receive is
// refused with the sentence of ADR 0044.
func (c *compiler) fileField(tf textField, t reflect.Type, off uintptr) (textField, bool, error) {
	bad := func() (textField, bool, error) {
		msg := tf.goPath + " is a " + strings.ReplaceAll(wire.PublicName(t), "github.com/nevindra/bites.", "bites.") + "; " + fileRefusalTail
		if foreignFileType(t) {
			msg += " " + fileHeaderSentence
		}
		return tf, true, refusalf("%s", msg)
	}
	pk, elem, vOff, fOff := wire.PresenceOf(t)
	spec := &fileSpec{}
	switch {
	case pk == wire.Optional:
		if !isFormFileType(elem) {
			if holdsFile(elem, map[reflect.Type]bool{}) || foreignFileType(elem) {
				return bad()
			}
			return tf, false, nil
		}
		spec.kind, spec.elem = fileOpt, elem
		spec.flagOff, spec.valOff = off+fOff, off+vOff
	case isFormFileType(t):
		spec.kind, spec.elem = fileOne, t
	case t.Kind() == reflect.Slice && isFormFileType(t.Elem()):
		spec.kind, spec.elem = fileMany, t.Elem()
		spec.elemSz = t.Elem().Size()
	case foreignFileType(t) || holdsFile(t, map[reflect.Type]bool{}):
		return bad()
	default:
		return tf, false, nil
	}
	if spec.elem != formFileType {
		if spec.elem.NumField() != 1 {
			return tf, true, refusalf("%s is a %s with fields besides the embedded FormFile; a file type is a FormFile or a struct whose only field is an embedded FormFile", tf.goPath, typeLabel(spec.elem))
		}
	}
	if err := c.fileRules(tf.goPath, spec); err != nil {
		return tf, true, err
	}
	tf.file = spec
	tf.required = spec.kind == fileOne
	tf.typ = t
	if spec.kind == fileMany {
		tf.list = 0 // assigned by the section
		tf.listOff, tf.sliceT, tf.elemSize = off, t, spec.elemSz
	}
	return tf, true, nil
}

// foreignFileType reports a type another framework uses for a file: a []byte, a
// *multipart.FileHeader, an io.Reader.
func foreignFileType(t reflect.Type) bool {
	if t == bytesType {
		return true
	}
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	switch {
	case t.PkgPath() == "mime/multipart" && t.Name() == "FileHeader":
		return true
	case t.PkgPath() == "io" && (t.Name() == "Reader" || t.Name() == "ReadCloser" || t.Name() == "ReadSeeker"):
		return true
	}
	return false
}

// fileRules reads Accept and MaxSize of a file type, once, at Registration.
func (c *compiler) fileRules(goPath string, spec *fileSpec) error {
	z := reflect.New(spec.elem)
	name := typeLabel(spec.elem)
	if m := z.MethodByName("Accept"); m.IsValid() {
		a, ok := z.Interface().(interface{ Accept() []string })
		if !ok {
			return refusalf("%s: %s.Accept is %s; it must be func() []string, the media types the file may have", goPath, name, m.Type())
		}
		list := a.Accept()
		if len(list) == 0 {
			return refusalf("%s: %s.Accept returns an empty list; an entry is a media type such as image/png or image/*", goPath, name)
		}
		for _, e := range list {
			typ, sub, ok := strings.Cut(strings.ToLower(strings.TrimSpace(e)), "/")
			if !ok || typ == "" || sub == "" || strings.ContainsAny(typ+sub, " ;,") || typ == "*" || strings.Contains(typ, "*") || (strings.Contains(sub, "*") && sub != "*") {
				return refusalf("%s: %s.Accept returns %q; an entry is a media type such as image/png or image/*", goPath, name, e)
			}
			spec.accept = append(spec.accept, typ+"/"+sub)
		}
	}
	if m := z.MethodByName("MaxSize"); m.IsValid() {
		a, ok := z.Interface().(interface{ MaxSize() int64 })
		if !ok {
			return refusalf("%s: %s.MaxSize is %s; it must be func() int64, the most bytes of the file", goPath, name, m.Type())
		}
		n := a.MaxSize()
		if n < 1 || n > c.cfg.maxBody {
			return refusalf("%s: %s.MaxSize returns %d; it is the most bytes of the file, and at most MaxBody", goPath, name, n)
		}
		spec.maxSize = n
	}
	return nil
}

// acceptMessage is the sentence for a file whose declared type is not allowed.
func (f *fileSpec) acceptMessage() string {
	list := make([]string, len(f.accept))
	copy(list, f.accept)
	switch len(list) {
	case 1:
		return "the file must be sent as " + list[0]
	default:
		return "the file must be sent as " + strings.Join(list[:len(list)-1], ", ") + " or " + list[len(list)-1]
	}
}

// accepts reports whether the media type the client declared is allowed. Parameters and case are
// ignored; a part with no type is the empty string and is allowed only when anything is.
func (f *fileSpec) accepts(declared []byte) bool {
	if f.accept == nil {
		return true
	}
	if i := indexByte(declared, ';'); i >= 0 {
		declared = declared[:i]
	}
	var buf [96]byte
	if len(declared) > len(buf) {
		return false
	}
	n := 0
	for _, c := range declared {
		if c == ' ' || c == '\t' {
			continue
		}
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		buf[n] = c
		n++
	}
	d := buf[:n]
	for _, a := range f.accept {
		if strings.HasSuffix(a, "/*") {
			if len(d) > len(a)-1 && string(d[:len(a)-1]) == a[:len(a)-1] {
				return true
			}
			continue
		}
		if string(d) == a {
			return true
		}
	}
	return false
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// mpRun is one multipart form being decoded.
type mpRun struct {
	textRun
	ex      *engine.Exchange
	m       *engine.Multipart
	cfg     *routeConfig
	a       *App
	sent    *submission
	text    int // bytes of text so far
	fileN   [maxSectionFields]uint8
	fileSeq int
}

// readMultipart reads the whole body as a multipart form and decodes it into the Input. It
// reports false when it has answered (415, 413, or a body that cannot be read).
func (b *bodyPlan) readMultipart(a *App, ex *engine.Exchange, in unsafe.Pointer, errs *jsoncodec.Errors, cfg *routeConfig) bool {
	if b.sub {
		*b.submittedForm(in) = &submission{}
	}
	head := ex.Head()
	if !head.Chunked && head.ContentLength == 0 {
		decodeMultipart(b, a, nil, ex, in, errs, cfg)
		return true
	}
	var boundary []byte
	var cerr error = mpart.ErrNotMultipart
	for i := range head.Headers {
		if h := &head.Headers[i]; equalFold(h.Name, "content-type") {
			boundary, cerr = mpart.ParseContentType(h.Value)
			break
		}
	}
	if cerr != nil {
		var he *h1.Error
		errors.As(cerr, &he)
		problem(ex, he.Status, he.Reason)
		return false
	}
	m, err := ex.OpenMultipart(engine.UploadOptions{
		Boundary: boundary, Max: cfg.maxBody, Timeout: cfg.bodyTimeout, MaxParts: cfg.maxParts,
		InFlight: cfg.inflight, Limit: int64(cfg.maxInFlight),
		Share: &a.uploadMem, ShareMax: a.limits.UploadMemory, MemRequest: uploadRequestMemory, Dir: a.limits.UploadDir,
	})
	if err != nil {
		var he *h1.Error
		if errors.As(err, &he) {
			r := problem(ex, he.Status, he.Reason)
			if he == engine.ErrUploadBusy {
				r.Header("Retry-After", "5")
			}
		} else {
			problem(ex, 400, "")
		}
		return false
	}
	return decodeMultipart(b, a, m, ex, in, errs, cfg)
}

// uploadRequestMemory is the file bytes one request may keep in memory (ADR 0044): a judgement,
// not a measurement of any workload.
const uploadRequestMemory = 256 << 10

// decodeMultipart reads the parts of m and fills the Input. It is a function of its own, never
// inlined, for the reason the other sections' decoders are.
//
//go:noinline
func decodeMultipart(b *bodyPlan, a *App, m *engine.Multipart, ex *engine.Exchange, in unsafe.Pointer, errs *jsoncodec.Errors, cfg *routeConfig) bool {
	s := b.form
	r := mpRun{ex: ex, m: m, cfg: cfg, a: a}
	r.textRun = textRun{sec: s, base: unsafe.Add(in, s.off), slab: ex.Slab(), errs: errs, phase: 1}
	if b.sub {
		r.sent = *b.submittedForm(in)
	}
	if m != nil && !r.parts() {
		return false
	}
	r.finishMP()
	return true
}

// fail answers a part the framework cannot read. It returns false for the caller to return.
func (r *mpRun) fail(err error) bool {
	var he *h1.Error
	if errors.As(err, &he) {
		problem(r.ex, he.Status, he.Reason)
	} else {
		problem(r.ex, 400, "")
	}
	return false
}

// tooLarge answers a 413 that names the field.
func (r *mpRun) tooLarge(loc, msg string) bool {
	addError(r.errs, loc, msg)
	invalidInputAs(r.ex, 413, r.errs)
	return false
}

func (r *mpRun) parts() bool {
	s := r.sec
	lim := &r.a.limits
	for {
		h, err := r.m.Next()
		if err == io.EOF {
			return true
		}
		if err != nil {
			return r.fail(err)
		}
		idx, ok := s.lookup(h.Name)
		if !ok {
			if !s.unknown {
				addError(r.errs, "body."+clipKey(string(h.Name)), msgUnknownField)
			}
			continue
		}
		f := &s.fields[idx]
		if f.file == nil {
			if h.HasFileName {
				addError(r.errs, f.loc, msgTextSentAsFile)
				r.seen |= 1 << idx
				continue
			}
			room := min(lim.MaxFieldBytes, lim.MaxFormBytes-r.text)
			text, err := r.m.ReadText(h.Gen, max(room, 0), r.slab)
			if err != nil {
				if err == engine.ErrPartTooLarge {
					if room < lim.MaxFieldBytes {
						return r.tooLarge(f.loc, "all the text of the request is over "+strconv.Itoa(lim.MaxFormBytes)+" bytes")
					}
					return r.tooLarge(f.loc, "the field is over "+strconv.Itoa(lim.MaxFieldBytes)+" bytes")
				}
				return r.fail(err)
			}
			r.text += len(text)
			if r.sent != nil {
				r.sent.sent = append(r.sent.sent, [2]string{f.wire, text})
			}
			r.storeText(idx, text)
			continue
		}
		spec := f.file
		if !h.HasFileName {
			addError(r.errs, f.loc, msgFileNeedsName)
			r.seen |= 1 << idx
			continue
		}
		if r.fileN[idx] < 255 {
			r.fileN[idx]++
		}
		if spec.kind != fileMany && r.fileN[idx] > 1 {
			continue
		}
		if !spec.accepts(h.ContentType) {
			addError(r.errs, f.loc, spec.acceptMessage())
			r.seen |= 1 << idx
			continue
		}
		bound := r.cfg.maxBody
		if spec.maxSize > 0 {
			bound = spec.maxSize
		}
		u, err := r.m.ReceiveFile(h.Gen, bound)
		if err != nil {
			if err == engine.ErrPartTooLarge {
				return r.tooLarge(f.loc, "the file is over "+strconv.FormatInt(bound, 10)+" bytes")
			}
			return r.fail(err)
		}
		var arr [256]byte
		nb := append(append(arr[:0], h.FileName...), h.ContentType...)
		str := r.slab.Copy(nb)
		r.storeFile(idx, FormFile{Name: f.wire, FileName: str[:len(h.FileName)], ContentType: str[len(h.FileName):], Size: u.Size(), f: u})
	}
}

// storeText hands one text value to its field, as a Form's field takes it.
func (r *mpRun) storeText(idx int, s string) {
	f := &r.sec.fields[idx]
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
		if err := f.dec(unsafe.Add(r.base, f.off), s); err != nil {
			addText(r.errs, f.loc, err)
		}
		return
	}
	v := reflect.NewAt(f.sliceT, unsafe.Add(r.base, f.listOff)).Elem()
	n := v.Len()
	v.Grow(1)
	v.SetLen(n + 1)
	if n == 0 && f.hasFlag {
		*(*bool)(unsafe.Add(r.base, f.flagOff)) = true
	}
	r.seen |= bit
	if err := f.dec(unsafe.Add(v.UnsafePointer(), uintptr(n)*f.elemSize), s); err != nil {
		addText(r.errs, f.loc+"["+strconv.Itoa(n)+"]", err)
	}
}

// storeFile puts one received file in its field.
func (r *mpRun) storeFile(idx int, ff FormFile) {
	f := &r.sec.fields[idx]
	spec := f.file
	r.seen |= uint64(1) << idx
	switch spec.kind {
	case fileMany:
		v := reflect.NewAt(f.sliceT, unsafe.Add(r.base, f.listOff)).Elem()
		n := v.Len()
		v.Grow(1)
		v.SetLen(n + 1)
		*(*FormFile)(unsafe.Add(v.UnsafePointer(), uintptr(n)*f.elemSize)) = ff
	case fileOpt:
		*(*FormFile)(unsafe.Add(r.base, spec.valOff)) = ff
		*(*bool)(unsafe.Add(r.base, spec.flagOff)) = true
	default:
		*(*FormFile)(unsafe.Add(r.base, f.off)) = ff
	}
}

// finishMP reports what the form lacked or held wrongly: a file field sent twice to a single
// file, the length of a list, and the required fields with none.
func (r *mpRun) finishMP() {
	for i := range r.sec.fields {
		f := &r.sec.fields[i]
		if f.file != nil {
			if f.file.kind != fileMany && r.fileN[i] > 1 {
				addError(r.errs, f.loc, fmt.Sprintf("expected one file, got %d", r.fileN[i]))
				r.seen |= 1 << i
			}
			continue
		}
		if f.list >= 0 && f.items != nil && r.seen&(1<<i) != 0 {
			n := reflect.NewAt(f.sliceT, unsafe.Add(r.base, f.listOff)).Elem().Len()
			if !f.items.OK(n) {
				addRule(r.errs, f.loc, f.items.Message, wire.RuleItems, int64(f.items.Min), int64(f.items.Max))
			}
		}
	}
	r.textRun.finish()
}
