package bites

import (
	"errors"
	"io"
	"reflect"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/mpart"
)

// Upload is a Body whose handler reads a multipart/form-data request part by part while it arrives
// (ADR 0044). Nothing is buffered whole: the handler asks for the next part, reads it as far as it
// wants, and asks again, so a 2 GiB upload costs the handler's own choices and a 32 KiB window.
//
//	type Avatar struct {
//		Path struct{ ID int64 }
//		Body bites.Upload
//	}
//
//	func setAvatar(ctx context.Context, in Avatar) (bites.NoContent, error) {
//		return bites.NoContent{}, in.Body.Each(func(part bites.Part) error {
//			if part.Name != "avatar" {
//				return nil
//			}
//			_, err := io.Copy(store, part)
//			return err
//		})
//	}
//
// Each is the loop over Next; a handler that wants the parts one call at a time uses Next.
//
// An Upload route must be given MaxBody, on the route or on a group above it: the most a route may
// receive is a decision of the application, so there is no default and a route without one is
// refused at Registration. BodyTimeout defaults to max(30 s, MaxBody / 128 KiB/s). MaxInFlight(n)
// bounds the uploads in flight and MaxParts(n) the parts of one (128 by default). A part header
// over 8 KiB or with more than 16 fields, and a boundary over 70 bytes, are refused.
//
// The error Next and Part.Read return when the body is refused (too large, too slow, cut short,
// malformed) is a *StatusError with the status the client should see, so returning it answers
// correctly; the connection is closed after the answer. The Section values of the Input are
// decoded, and Validate has run, before the handler is called; the body has not been read.
//
// An Upload, and every Part from it, is valid only while the handler runs. Once the handler has
// returned, Next and Read fail with ErrUploadClosed and touch nothing, so a goroutine that kept
// one cannot read another request's bytes (ADR 0008). A handler that returns before the last part
// leaves a body unread, and the connection is closed after the answer.
type Upload struct{ m *engine.Multipart }

// Part is one part of an Upload. Read reads its body and returns io.EOF at its end. It is valid
// until Next is called again: a Read after that fails with ErrPartClosed.
//
// Name, FileName and ContentType are what the client sent, as data. FileName in particular is
// never a path: it may be "../../etc/passwd" or a Windows path, and the framework never opens
// it. A handler that stores a file under a name of its choosing (a generated one) is safe; one that
// joins FileName to a directory has chosen to trust the client. FileName is empty for a plain form
// field.
type Part struct {
	// Name is the form field name the client sent, as data.
	Name string
	// FileName is the file name the client sent, as data: never trust it as a path. Empty for a plain form field.
	FileName string
	// ContentType is the media type the client sent for the part, as data.
	ContentType string
	m           *engine.Multipart
	gen         uint32
}

// ErrUploadClosed is returned by an Upload or a Part used after its handler returned.
var ErrUploadClosed = engine.ErrUploadClosed

// ErrPartClosed is returned by a Part read after Next moved the upload on.
var ErrPartClosed = engine.ErrPartGone

// Next moves to the next part, skipping what is left of the current one, and returns it. It
// returns io.EOF after the last part.
func (u Upload) Next() (Part, error) {
	if u.m == nil {
		return Part{}, ErrUploadClosed
	}
	h, err := u.m.Next()
	if err != nil {
		return Part{}, uploadError(err)
	}
	// One allocation holds the three strings: the bytes are views of the window, which is pooled.
	var arr [512]byte
	b := append(append(append(arr[:0], h.Name...), h.FileName...), h.ContentType...)
	s := string(b)
	return Part{
		Name:        s[:len(h.Name)],
		FileName:    s[len(h.Name) : len(h.Name)+len(h.FileName)],
		ContentType: s[len(h.Name)+len(h.FileName):],
		m:           u.m,
		gen:         h.Gen,
	}, nil
}

// Each calls fn with each part in turn and returns nil after the last. It is the loop over Next
// written once: what fn leaves unread of a part is skipped, so fn returns nil to pass a part by.
// The first error stops it and is returned as it is, whether fn returned it or reading the upload
// did (a *StatusError, so returning it answers with the right status); the parts after it are not
// read, and the connection is closed after the answer. The Part fn is given is valid until fn
// returns. Each adds no allocation to the loop it replaces (ADR 0003).
//
//	err := in.Body.Each(func(part bites.Part) error {
//		if part.Name != "avatar" {
//			return nil
//		}
//		_, err := io.Copy(store, part)
//		return err
//	})
func (u Upload) Each(fn func(Part) error) error {
	for {
		p, err := u.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(p); err != nil {
			return err
		}
	}
}

// Read reads the part's body.
func (p Part) Read(b []byte) (int, error) {
	if p.m == nil {
		return 0, ErrUploadClosed
	}
	n, err := p.m.ReadPart(p.gen, b)
	if err != nil && err != io.EOF {
		err = uploadError(err)
	}
	return n, err
}

// uploadError turns what the engine and the parser refuse with into a StatusError, so a handler
// that returns it answers with the right status. Errors that are not refusals pass through.
func uploadError(err error) error {
	if err == io.EOF {
		return err
	}
	var he *h1.Error
	if errors.As(err, &he) {
		return &StatusError{Status: he.Status, Message: he.Reason}
	}
	return err
}

var uploadType = reflect.TypeFor[Upload]()

// containsUpload reports whether t is, or holds at any depth, an Upload.
func containsUpload(t reflect.Type, seen map[reflect.Type]bool) bool {
	return containsType(t, uploadType, seen)
}

// readUpload opens the request body as an upload. It reads none of it: the handler does. It
// reports false when it has answered (415, 413 or 503), and adds the 400 "required" for a request
// with no body at all.
func (b *bodyPlan) readUpload(a *App, ex *engine.Exchange, in unsafe.Pointer, errs *jsoncodec.Errors, cfg *routeConfig) bool {
	head := ex.Head()
	if !head.Chunked && head.ContentLength == 0 {
		addRequired(errs, "body")
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
	*(*Upload)(unsafe.Add(in, b.off)) = Upload{m}
	return true
}

// uploadRoute is what the start-up log says about an Upload route (ADR 0026).
type uploadRoute struct {
	label           string
	maxBody         int64
	timeout         time.Duration
	timeoutExplicit bool
	maxInFlight     int
	multipart       bool // a Multipart[T] route: its files wait in memory or on disk (ADR 0044)
	plain           bool // a route whose body is read whole: only its limits are logged, and only when MaxBody moved the timeout
}

// multiparts reports whether any route reads a Multipart body.
func (a *App) multiparts() bool {
	for _, u := range a.uploads {
		if u.multipart {
			return true
		}
	}
	return false
}

// uploadRate is the rate floor the default body timeout assumes (ADR 0044): a judgement, not a
// measurement.
const uploadRate = 128 << 10

// planUpload settles the limits of an Upload route at Registration: the body timeout is derived
// from MaxBody unless the route, a group or the app gave one, the parts get their default, and the
// route gets its counter.
func (a *App) planUpload(label string, cfg *routeConfig, multipart bool) {
	explicit := cfg.bodyTimeoutSet || a.given.BodyTimeout > 0
	if !explicit {
		cfg.bodyTimeout = max(engine.DefaultBodyTimeout, time.Duration(cfg.maxBody/uploadRate)*time.Second)
	}
	if cfg.maxParts == 0 {
		cfg.maxParts = engine.DefaultMaxParts
	}
	if cfg.maxInFlight > 0 {
		cfg.inflight = new(atomic.Int64)
	}
	a.uploads = append(a.uploads, uploadRoute{label, cfg.maxBody, cfg.bodyTimeout, explicit, cfg.maxInFlight, multipart, false})
}

// planBody settles the body timeout of a route whose body is read whole (JSON, Raw[T], []byte, a
// form). Like an Upload's it is derived from MaxBody unless the route, a group or the app gave one
// (ADR 0044, ADR 0024): a body of 8 MiB with the default 30 s would need 2.2 Mbit/s to arrive in
// time, and a slower link would get a 408 on every request. The derivation changes nothing at or
// below 3.75 MiB, where max(30 s, MaxBody / 128 KiB/s) is the default; a route above that is
// logged at start with the timeout it got.
func (a *App) planBody(label string, cfg *routeConfig) {
	if cfg.bodyTimeoutSet || a.given.BodyTimeout > 0 {
		return
	}
	d := max(engine.DefaultBodyTimeout, time.Duration(cfg.maxBody/uploadRate)*time.Second)
	if d <= cfg.bodyTimeout {
		return
	}
	cfg.bodyTimeout = d
	a.uploads = append(a.uploads, uploadRoute{label: label, maxBody: cfg.maxBody, timeout: d, plain: true})
}
