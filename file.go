package bites

import (
	"errors"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/nevindra/bites/internal/engine"
)

// File is a 200 whose body is a file (ADR 0043), sent with its Content-Length and its
// Last-Modified and read in chunks as it is written: a file is never held whole in memory.
//
// Exactly one of Root and FS says where the file is, and Name is the path inside it, slash
// separated. A Root is an os.Root, which cannot leave its directory: a Name with "..", a symbolic
// link that points out, and an absolute Name are refused by the operating system call itself
// (Go 1.24). An FS is checked with fs.ValidPath first, so a Name with "..", a leading slash or an
// empty element never reaches it. Either way the file's name is data from the client at worst, and
// it cannot name anything outside the directory. A Name that is not valid, a file that does not
// exist or that is not a regular file (a directory, a device) is a 404 problem+json, the same for
// each, so an answer does not say which files exist. Both Root and FS set, or neither, is a 500
// and logged, because it can only be a mistake. Any other failure to read the file is a 500,
// logged.
//
// ContentType is used when given; otherwise the type of the extension of Name (the system's MIME
// table and a built-in one), and application/octet-stream when that has none. A Content-Type in
// Header replaces both. A HEAD request, or the implicit HEAD of a GET route, gets the head with the
// same Content-Length and no body.
//
// The length and the modification time are those the first stat saw. A file that changes between
// that and the end of the answer is not sent whole: if its size differs when it is opened the
// answer is a 500, and if it shrinks while being sent the connection is closed after the bytes that
// existed, so the client sees a short body and not a wrong one.
//
// Validators, conditional and range requests (ADR 0064). A file with a modification time is sent
// with Last-Modified and a strong ETag made of that time in nanoseconds and the size, both in hex
// ("18c3b2a1f0e4d2c0-2a"); a file with none (an embed.FS) gets neither. An ETag or a
// Last-Modified in Header replaces the made one, in the answer and in every comparison below.
// If-Match, If-Unmodified-Since, If-None-Match and If-Modified-Since are evaluated in the order of
// RFC 9110 section 13.2.2: a 412 problem+json when a precondition fails, a 304 with the validators
// and no body when the client's copy is current. A GET with a Range of one byte range is a 206
// with Content-Range, or a 416 problem+json with "Content-Range: bytes */size" when the range
// starts past the end; a Range of several ranges, of another unit, or that does not parse is
// ignored and the whole file sent, and so is one whose If-Range no longer names the file. Every
// 200 says Accept-Ranges: bytes; an Accept-Ranges of "none" in Header turns ranges off.
//
// Middleware sees a File as a Response with Streaming() true and Size() 0 (ADR 0030): it is
// written by the same path as a Stream, with a declared length.
type File struct {
	// Root is the directory the file is read from; exactly one of Root and FS is set.
	Root *os.Root
	// FS is the file system the file is read from; exactly one of Root and FS is set.
	FS fs.FS
	// Name is the slash separated path of the file inside Root or FS.
	Name string
	// ContentType is the media type sent; empty infers it from the extension of Name, or uses application/octet-stream.
	ContentType string
	// Attachment, when it is not empty, makes the answer a download saved under this name: the
	// answer carries Content-Disposition: attachment, with the name written by the standard
	// library's mime.FormatMediaType, which quotes it and encodes a name outside ASCII, a line
	// break or invalid UTF-8 as filename* (RFC 2231), so no name can split the response. Only the
	// part after the last "/" or "\" is used, so Name itself can be given ("reports/q3.pdf" is
	// saved as "q3.pdf"); a name with nothing usable left ("dir/", ".", "..") sends "attachment"
	// alone and the browser names the file. It is sent on a 200 and a 206, not on a 304. A
	// Content-Disposition in Header replaces it (an "inline" one, for instance). Empty sends no
	// Content-Disposition, and the browser shows the file when it can.
	Attachment string
	// Header holds the response headers; a Content-Type in it replaces ContentType.
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
}

func isFileType(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && t.PkgPath() == ownPkg && t.Name() == "File"
}

// fileAnswer is what a file answer is built from: the fields of a File, or what Static chose for
// a request (static.go).
type fileAnswer struct {
	root     *os.Root
	fsys     fs.FS
	name     string // the file that is opened
	typeName string // the name whose extension gives the type; name when empty (a twin's original)
	ctype    string
	hdr      Header
	cookies  []Cookie
	download string // the Attachment of a File; "" for Static
	etag     string // a tag known before the stat (Static over an embed.FS); "" makes one
	cache    string // the Cache-Control when hdr has none (Static)
	encoding string // the Content-Encoding of a precompressed twin (Static)
	vary     bool   // the answer depends on Accept-Encoding (Static, a type that may have twins)
}

// stat looks at the file without opening it.
func (fa *fileAnswer) stat() (fs.FileInfo, error) {
	if fa.root != nil {
		return fa.root.Stat(fa.name)
	}
	return fs.Stat(fa.fsys, fa.name)
}

// fileRun is what the engine runs after the handler and the middleware have returned: it opens
// the file and sends n bytes from off.
type fileRun struct {
	a    *App
	root *os.Root
	fsys fs.FS
	name string
	size int64 // the whole file, as the stat saw it
	off  int64
	n    int64
}

func (f *fileRun) Run(w *StreamWriter) error {
	var file fs.File
	var err error
	if f.root != nil {
		file, err = f.root.Open(f.name)
	} else {
		file, err = f.fsys.Open(f.name)
	}
	if err != nil {
		return err
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Size() != f.size {
		return errFileChanged
	}
	if f.off > 0 {
		if err := skipTo(file, f.off); err != nil {
			return err
		}
	}
	n, err := w.ReadFrom(file)
	if err == nil && n != f.n {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// skipTo moves a file to off: by Seek when it can (an os.File, an embed.FS file), else by reading
// and discarding, which an fs.FS of another kind may need.
func skipTo(file fs.File, off int64) error {
	if s, ok := file.(io.Seeker); ok {
		_, err := s.Seek(off, io.SeekStart)
		return err
	}
	_, err := io.CopyN(io.Discard, file, off)
	return err
}

var errFileChanged = errors.New("bites: the file changed between the stat that sized the answer and the open")

// Fail answers a file that was there at the stat and was not when it was opened.
func (f *fileRun) Fail(ex *engine.Exchange, err error) {
	if notThere(err) {
		problem(ex, 404, "")
		return
	}
	f.a.internalError(ex, err)
}

// notThere reports whether err means "no such file, or none this route may name".
func notThere(err error) bool {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) || errors.Is(err, fs.ErrPermission) {
		return true
	}
	// os.Root refuses a name that leaves the directory with an error that has no sentinel.
	var pe *fs.PathError
	return errors.As(err, &pe) && pe.Err != nil && pe.Err.Error() == "path escapes from parent"
}

// writeFile builds the answer of a File. It stats the file now, so a missing one is an ordinary
// 404 before anything is sent, and opens it only when the body is written, so a HEAD request, a
// 304 and a 412 open nothing and a file is never left open by a response that is not written.
func (p *outPlan) writeFile(a *App, ex *engine.Exchange, src unsafe.Pointer) {
	fa := fileAnswer{
		root:     *(**os.Root)(unsafe.Add(src, p.rootOff)),
		fsys:     *(*fs.FS)(unsafe.Add(src, p.fsOff)),
		name:     *(*string)(unsafe.Add(src, p.nameOff)),
		ctype:    *(*string)(unsafe.Add(src, p.ctOff)),
		hdr:      *(*Header)(unsafe.Add(src, p.hdrOff)),
		cookies:  *(*[]Cookie)(unsafe.Add(src, p.cookOff)),
		download: *(*string)(unsafe.Add(src, p.attOff)),
	}
	if (fa.root == nil) == (fa.fsys == nil) {
		a.internalError(ex, refusalf("a File needs exactly one of Root and FS"))
		return
	}
	if fa.ctype != "" && !validContentType(fa.ctype) {
		a.internalError(ex, refusalf("a File has the content type %q, which is not type/subtype", fa.ctype))
		return
	}
	if !fs.ValidPath(fa.name) || fa.name == "." {
		problem(ex, 404, "")
		return
	}
	fi, err := fa.stat()
	if err != nil {
		if notThere(err) {
			problem(ex, 404, "")
			return
		}
		a.internalError(ex, err)
		return
	}
	if !fi.Mode().IsRegular() {
		problem(ex, 404, "")
		return
	}
	a.sendFile(ex, &fa, fi.Size(), fi.ModTime())
}

// sendFile answers a file that exists and is regular, of size bytes modified at mod: the
// preconditions, the range, then the head and the function that sends the body (ADR 0064).
func (a *App) sendFile(ex *engine.Exchange, fa *fileAnswer, size int64, mod time.Time) {
	head := ex.Head()
	hs := head.Headers
	method := head.Method
	isGet := string(method) == "GET"
	v := validators{isGetOr: isGet || string(method) == "HEAD"}

	// The validators: the handler's own from Header, else the ones the file offers.
	userTag, userMod := headerValue(fa.hdr, "ETag"), headerValue(fa.hdr, "Last-Modified")
	var tagBuf [48]byte
	switch {
	case userTag != "":
		if !validETag(userTag) {
			a.internalError(ex, refusalf("a File has the ETag %q in its Header, which is not an entity-tag (a quoted string, W/ before it for a weak one)", userTag))
			return
		}
		v.etag = unsafe.Slice(unsafe.StringData(userTag), len(userTag)) // read, never written
	case fa.etag != "":
		v.etag = unsafe.Slice(unsafe.StringData(fa.etag), len(fa.etag))
	case mod.Unix() > 0:
		v.etag = appendETag(tagBuf[:0], mod, size)
	}
	if userMod != "" {
		v.mod, v.hasMod = parseHTTPDate(unsafe.Slice(unsafe.StringData(userMod), len(userMod)))
	} else if mod.Unix() > 0 {
		v.mod, v.hasMod = mod.UTC().Truncate(time.Second), true
	}

	switch v.evaluate(hs) {
	case condNotModified:
		a.notModified(ex, fa, &v, userTag != "", userMod != "")
		return
	case condFailed:
		problem(ex, 412, "")
		return
	}

	status, off, n := 200, int64(0), size
	rangesOn := !foldEq(headerValue(fa.hdr, "Accept-Ranges"), "none")
	if rv, ok := firstHeader(hs, "range"); ok && isGet && rangesOn {
		ir, hasIR := firstHeader(hs, "if-range")
		if !hasIR || v.ifRangeHolds(ir) {
			switch kind, o, k := parseRange(rv, size); kind {
			case rangeOne:
				status, off, n = 206, o, k
			case rangeUnsatisfiable:
				var buf [40]byte
				b := strconv.AppendInt(append(buf[:0], "bytes */"...), size, 10)
				problem(ex, 416, "").HeaderBytes("Content-Range", b)
				return
			}
		}
	}

	r := ex.Respond(status)
	needType := true
	writeFileHeaders(r, fa.hdr, false, &needType)
	writeAttachment(r, fa.hdr, fa.download)
	if needType {
		r.Header("Content-Type", fa.contentType())
	}
	if fa.encoding != "" {
		r.Header("Content-Encoding", fa.encoding)
	}
	a.writeValidators(r, fa, &v, userTag != "", userMod != "")
	if rangesOn && !hasHeader(fa.hdr, "Accept-Ranges") {
		r.Header("Accept-Ranges", "bytes")
	}
	if status == 206 {
		var buf [64]byte
		b := append(buf[:0], "bytes "...)
		b = strconv.AppendInt(b, off, 10)
		b = append(b, '-')
		b = strconv.AppendInt(b, off+n-1, 10)
		b = append(b, '/')
		b = strconv.AppendInt(b, size, 10)
		r.HeaderBytes("Content-Range", b)
	}
	if !a.writeCookies(ex, r, fa.cookies) {
		return
	}
	ex.StreamSized(n, &fileRun{a: a, root: fa.root, fsys: fa.fsys, name: fa.name, size: size, off: off, n: n})
}

// notModified writes the 304 of RFC 9110 section 15.4.5: the validators and the headers a cache
// updates its copy with, and no representation metadata, no body and nothing opened.
func (a *App) notModified(ex *engine.Exchange, fa *fileAnswer, v *validators, userTag, userMod bool) {
	r := ex.Respond(304)
	needType := false
	writeFileHeaders(r, fa.hdr, true, &needType)
	a.writeValidators(r, fa, v, userTag, userMod)
	a.writeCookies(ex, r, fa.cookies)
}

// writeValidators adds ETag, Last-Modified, Cache-Control and Vary where the handler's Header did
// not already give them.
func (a *App) writeValidators(r *engine.Response, fa *fileAnswer, v *validators, userTag, userMod bool) {
	if len(v.etag) > 0 && !userTag {
		r.HeaderBytes("ETag", v.etag)
	}
	if v.hasMod && !userMod {
		var buf [40]byte
		r.HeaderBytes("Last-Modified", appendHTTPDate(buf[:0], v.mod))
	}
	if fa.cache != "" && !hasHeader(fa.hdr, "Cache-Control") {
		r.Header("Cache-Control", fa.cache)
	}
	if fa.vary && !hasHeader(fa.hdr, "Vary") {
		r.Header("Vary", "Accept-Encoding")
	}
}

func (a *App) writeCookies(ex *engine.Exchange, r *engine.Response, cookies []Cookie) bool {
	for _, c := range cookies {
		v, err := appendSetCookie(nil, &c)
		if err != nil {
			a.internalError(ex, err)
			return false
		}
		r.Header("Set-Cookie", string(v))
	}
	return true
}

// contentType is the type the answer is sent with when Header has none: the File's own, else
// the extension's, else application/octet-stream.
func (fa *fileAnswer) contentType() string {
	if fa.ctype != "" {
		return fa.ctype
	}
	name := fa.typeName
	if name == "" {
		name = fa.name
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// writeFileHeaders writes a File's Header in name order, skipping the names the engine and the
// file answer own. For a 304 (notModified) only the headers a cache updates its stored copy with
// are written (RFC 9110 section 15.4.5).
func writeFileHeaders(r *engine.Response, h Header, notModified bool, needType *bool) {
	if len(h) == 0 {
		return
	}
	names := make([]string, 0, len(h))
	for n := range h {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		switch strings.ToLower(n) {
		case "content-length", "transfer-encoding", "connection", "date", "content-range":
			continue
		case "cache-control", "content-location", "expires", "vary", "etag", "last-modified":
		case "content-type":
			if notModified {
				continue
			}
			*needType = false
		default:
			if notModified {
				continue
			}
		}
		for _, v := range h[n] {
			r.Header(n, v)
		}
	}
}

// headerValue is the first value of the header called name in h, matched without regard to case.
func headerValue(h Header, name string) string {
	for n, vs := range h {
		if len(vs) > 0 && foldEq(n, name) {
			return vs[0]
		}
	}
	return ""
}

// writeAttachment adds the Content-Disposition of a download saved as name, the Attachment field
// of a File, a Bytes or a Stream (ADR 0043). It adds nothing for an empty name or when the
// handler's Header has a Content-Disposition of its own.
func writeAttachment(r *engine.Response, h Header, name string) {
	if name == "" || hasHeader(h, "Content-Disposition") {
		return
	}
	r.Header("Content-Disposition", contentDisposition(name))
}

// contentDisposition is the attachment header for name: its last element after any "/" or "\",
// written by mime.FormatMediaType, or "attachment" alone when no element is left. The type and
// the parameter name are constant tokens, so FormatMediaType never returns "" here.
func contentDisposition(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	return mime.FormatMediaType("attachment", map[string]string{"filename": name})
}
