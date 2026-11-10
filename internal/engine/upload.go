package engine

import (
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/mpart"
)

// An upload (ADR 0044): the body of an Upload route is read by the handler, part by part, while
// it arrives. The engine gives the framework a Multipart; every byte of the body reaches the
// handler through Multipart.ReadPart, which copies out of a window the user never sees. The
// window is pooled (ADR 0008 allows what no user holds), and a Multipart whose request is over
// fails every call, so a goroutine that kept a part reads nothing of another request.
//
// Where an upload waits for bytes (ADR 0005): in uploadSource.Fill, on the connection's goroutine,
// inside the handler's call; it is the one place, and its one read is c.nc.Read. Nothing else in an
// upload waits on the client, and it never parks: only a connection idle between requests does.

const (
	// UploadWindow is the size of the window an upload is parsed in.
	UploadWindow = 32 << 10
	// DefaultMaxParts is the most parts an upload has when the route gives no number.
	DefaultMaxParts = 128
)

// The refusals an upload adds to those of ReadBody.
var (
	ErrUploadBusy = &h1.Error{Status: 503, Close: true, Reason: "too many uploads in progress"}
	// ErrUploadClosed is what a Multipart returns once the request is over.
	ErrUploadClosed = errors.New("bites: read from an upload after its handler returned")
	// ErrPartGone is what a part returns once the upload has moved to another part.
	ErrPartGone = errors.New("bites: read from a part after the upload moved to the next one")
)

var windowPool = sync.Pool{New: func() any { return new([UploadWindow]byte) }}

// UploadOptions is what a route says about its uploads.
type UploadOptions struct {
	Boundary []byte
	Max      int64         // the most body bytes, required
	Timeout  time.Duration // the whole body, required
	MaxParts int
	InFlight *atomic.Int64 // the route's counter; nil when unlimited
	Limit    int64         // the most uploads in flight, when InFlight is set

	// A Multipart[T] receives its files (ADR 0044): the fields below say where they wait.
	// Share is the file bytes kept in memory by every request of the app and ShareMax its bound;
	// a file that would pass either that or MemRequest, the bound of one request, goes to a file
	// in Dir (os.TempDir when empty). All three are unused by an Upload.
	Share      *atomic.Int64
	ShareMax   int64
	MemRequest int64
	Dir        string
}

// Multipart is one upload in progress. Its methods are safe for concurrent use, though a handler
// has no reason to call them from two goroutines.
type Multipart struct {
	mu       sync.Mutex
	closed   atomic.Bool
	c        *conn
	ex       *Exchange
	win      *[UploadWindow]byte
	p        mpart.Parser
	src      uploadSource
	gen      uint32
	counter  *atomic.Int64
	started  bool
	released bool
	raw      bool // a BodyReader (ADR 0044): the body's bytes go to the handler as they are, with no parser
	timeout  time.Duration
	until    time.Time // the body's deadline

	// Files received by ReceiveFile (ADR 0044), closed when the request ends.
	opt     UploadOptions
	files   *UploadFile // newest first
	reqMem  int64       // bytes of Share this request holds
	scratch *[UploadWindow]byte
}

// uploadSource turns the connection's body, Content-Length or chunked, into the bytes the parser
// reads.
type uploadSource struct {
	m           *Multipart
	chunked     bool
	remain      int64 // Content-Length: body bytes not yet read
	remainStart int64 // Content-Length: the declared length, so the bytes read are remainStart-remain
	total       int64 // chunked: decoded bytes so far
	max         int64
	dec         h1.ChunkedDecoder
	in          []byte // chunked: raw bytes read and not yet decoded
	fromAux     bool
	done        bool
	err         error
}

// OpenMultipart prepares the request's body to be read as an upload. Nothing is read, and no
// 100 Continue is sent, until the first call of Next. It answers the refusals that need only the
// head: a Content-Length over Max (BodyTooLarge) and a route already at its limit of uploads
// in flight (ErrUploadBusy), both before the client is invited to send the body.
func (ex *Exchange) OpenMultipart(o UploadOptions) (*Multipart, error) {
	c := ex.c
	req := &c.sc.req
	if !req.Chunked && req.ContentLength > o.Max {
		ex.closeAfter = true
		return nil, BodyTooLarge(o.Max)
	}
	if o.InFlight != nil {
		if o.InFlight.Add(1) > o.Limit {
			o.InFlight.Add(-1)
			ex.closeAfter = true
			return nil, ErrUploadBusy
		}
	}
	m := &Multipart{c: c, ex: ex, counter: o.InFlight, timeout: o.Timeout, opt: o}
	m.win = windowPool.Get().(*[UploadWindow]byte)
	m.src = uploadSource{m: m, chunked: req.Chunked, remain: req.ContentLength, remainStart: req.ContentLength, max: o.Max}
	if err := m.p.Reset(o.Boundary, m.win[:], &m.src, o.MaxParts); err != nil {
		m.shut()
		return nil, err
	}
	c.sc.mp = m
	return m, nil
}

// OpenBody prepares the request's body to be read as it is, by a BodyReader (ADR 0044). It is
// OpenMultipart without a boundary, a window or a parser: the same refusals before the client is
// invited to send the body, the same counter, the same single place that waits (Fill), and the
// bytes go straight into the buffer the handler passes to Read.
func (ex *Exchange) OpenBody(o UploadOptions) (*Multipart, error) {
	c := ex.c
	req := &c.sc.req
	if !req.Chunked && req.ContentLength > o.Max {
		ex.closeAfter = true
		return nil, BodyTooLarge(o.Max)
	}
	if o.InFlight != nil {
		if o.InFlight.Add(1) > o.Limit {
			o.InFlight.Add(-1)
			ex.closeAfter = true
			return nil, ErrUploadBusy
		}
	}
	m := &Multipart{c: c, ex: ex, counter: o.InFlight, timeout: o.Timeout, opt: o, raw: true}
	m.src = uploadSource{m: m, chunked: req.Chunked, remain: req.ContentLength, remainStart: req.ContentLength, max: o.Max}
	c.sc.mp = m
	return m, nil
}

// ReadRaw reads the next bytes of a BodyReader's body into b. It returns io.EOF at the end of the
// body, ErrUploadClosed once the request is over, and otherwise the *h1.Error the framework
// answers with (413, 408 or 400); after any error but io.EOF the connection is closed after the
// answer.
func (m *Multipart) ReadRaw(b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed.Load() {
		return 0, ErrUploadClosed
	}
	if len(b) == 0 {
		return 0, nil
	}
	n, err := m.src.Fill(b)
	if err != nil && err != io.EOF {
		m.ex.closeAfter = true
	}
	return n, err
}

// whole reports whether the source has delivered the whole body, so that the connection can carry
// on to the next request.
func (u *uploadSource) whole() bool {
	if u.err != nil {
		return false
	}
	if u.chunked {
		return u.done
	}
	return u.remain == 0
}

// start sends 100 Continue when the client asked for it and arms the body's deadline. It runs
// once, on the first read.
func (m *Multipart) start() error {
	m.started = true
	c := m.c
	s := c.srv
	req := &c.sc.req
	if req.Chunked || int64(c.r1-c.r0) < req.ContentLength {
		c.holdBody() // before the chunked input is taken from the buffer, and before the deadline
	}
	if req.Expect100 && !m.ex.sent100 {
		m.ex.sent100 = true
		c.setWriteDeadline(s.now().Add(s.o.WriteTimeout))
		if _, err := c.nc.Write(continueLine); err != nil {
			return ErrBodyShort
		}
	}
	m.until = s.now().Add(m.timeout)
	c.setReadDeadline(m.until)
	if req.Chunked {
		m.src.dec.Reset(s.o.Limits.MaxHead)
		m.src.in = c.sc.buf[c.r0:c.r1]
	}
	return nil
}

// Fill is the parser's source, and the one place an upload waits for the client.
func (u *uploadSource) Fill(dst []byte) (int, error) {
	m := u.m
	if !m.started {
		if err := m.start(); err != nil {
			return 0, u.fail(err)
		}
	}
	if u.err != nil {
		return 0, u.err
	}
	if u.chunked {
		return u.fillChunked(dst)
	}
	if u.remain == 0 {
		return 0, io.EOF
	}
	c := m.c
	dst = dst[:min(int64(len(dst)), u.remain)]
	var n int
	if c.r1 > c.r0 {
		n = copy(dst, c.sc.buf[c.r0:c.r1])
		c.r0 += n
	} else {
		var err error
		n, err = c.nc.Read(dst)
		if n == 0 {
			return 0, u.fail(err)
		}
	}
	u.remain -= int64(n)
	if u.remain == 0 {
		c.releaseBody(false) // the body is over: the watch may read what follows
	}
	return n, nil
}

func (u *uploadSource) fillChunked(dst []byte) (int, error) {
	m := u.m
	c := m.c
	sc := c.sc
	if u.done {
		return 0, io.EOF
	}
	for {
		if len(u.in) == 0 {
			if sc.aux == nil {
				sc.aux = make([]byte, bufSize)
			}
			n, err := c.nc.Read(sc.aux)
			if n == 0 {
				return 0, u.fail(err)
			}
			c.r0 = c.r1 // everything in buf is consumed from here on
			u.in, u.fromAux = sc.aux[:n], true
		}
		consumed, outN, done, err := u.dec.Decode(u.in, dst)
		u.in = u.in[consumed:]
		u.total += int64(outN)
		if u.total > u.max {
			return 0, u.fail(BodyTooLarge(u.max))
		}
		if err != nil {
			if outN > 0 {
				u.err = u.fail(err)
				return outN, nil
			}
			return 0, u.fail(err)
		}
		if done {
			u.done = true
			c.releaseBody(false)
			if u.fromAux {
				c.sc.auxRest = u.in
			} else {
				c.r0 = c.r1 - len(u.in)
			}
			if outN > 0 {
				return outN, nil
			}
			return 0, io.EOF
		}
		if outN > 0 {
			return outN, nil
		}
	}
}

// fail records that the body cannot be trusted any more and names why, as the *h1.Error the
// framework answers with.
func (u *uploadSource) fail(err error) error {
	m := u.m
	m.ex.closeAfter = true
	var he *h1.Error
	switch {
	case errors.As(err, &he):
		u.err = he
	case errors.Is(err, os.ErrDeadlineExceeded):
		if m.closed.Load() {
			u.err = ErrUploadClosed
		} else {
			u.err = ErrBodyTimeout
		}
	default:
		u.err = ErrBodyShort
		m.c.releaseBody(true) // the read saw the client go, which a held watch could not
	}
	return u.err
}

// PartInfo is the header of a part. The slices are views of the window and are valid only until
// the next call on the Multipart, so the caller copies what it keeps.
type PartInfo struct {
	Name, FileName, ContentType []byte
	HasFileName                 bool
	Gen                         uint32
}

// Next moves to the next part and returns its header, io.EOF after the last, or the reason the
// body cannot be read. Any error but io.EOF means the connection will be closed after the answer.
func (m *Multipart) Next() (PartInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed.Load() {
		return PartInfo{}, ErrUploadClosed
	}
	h, err := m.p.Next()
	if err != nil {
		if err != io.EOF {
			m.ex.closeAfter = true
		}
		return PartInfo{}, err
	}
	m.gen++
	return PartInfo{Name: h.Name, FileName: h.FileName, ContentType: h.ContentType, HasFileName: h.HasFileName, Gen: m.gen}, nil
}

// ReadPart reads the body of part gen. It fails with ErrPartGone once the upload moved on, and
// with ErrUploadClosed once the request is over.
func (m *Multipart) ReadPart(gen uint32, b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed.Load() {
		return 0, ErrUploadClosed
	}
	if gen != m.gen {
		return 0, ErrPartGone
	}
	n, err := m.p.Read(b)
	if err != nil && err != io.EOF {
		m.ex.closeAfter = true
	}
	return n, err
}

// lockKick marks the upload closed and takes its lock. A read in progress on another goroutine
// is cut short by moving the deadline to now, so the connection never waits for a goroutine the
// handler left behind.
func (m *Multipart) lockKick() {
	m.closed.Store(true)
	if !m.mu.TryLock() {
		m.c.nc.SetReadDeadline(m.c.srv.now()) // perhaps not the connection's goroutine: finishUpload forgets the hot wait's deadline
		m.mu.Lock()
	}
}

// shut ends the upload: later calls fail, the window goes back to the pool and the route's
// counter is released.
func (m *Multipart) shut() {
	m.lockKick()
	defer m.mu.Unlock()
	if m.released {
		return
	}
	if !m.raw {
		m.p.Release()
		windowPool.Put(m.win)
		m.win = nil
		m.closeFiles()
	}
	m.released = true
	if m.counter != nil {
		m.counter.Add(-1)
	}
}

// finishUpload runs when the handler has returned, panicked or not. It closes the upload, and
// decides what the rest of the body means for the connection: fully read, or drained after the
// closing delimiter, the connection can carry on; anything else closes it after the answer.
func (ex *Exchange) finishUpload() {
	m := ex.c.sc.mp
	ex.c.sc.mp = nil
	if m == nil {
		return
	}
	ex.c.sc.rdl = 0 // lockKick may have moved the read deadline from another goroutine
	complete := false
	if m.started {
		m.lockKick()
		if m.raw {
			complete = m.src.whole() && !ex.closeAfter
		} else if m.win != nil && m.p.Ended() && !ex.closeAfter {
			if !m.src.whole() {
				m.c.setReadDeadline(m.until) // a whole body drains from the window: the watch may be reading
			}
			complete = m.p.Drain() == nil
		}
		if m.src.chunked {
			ex.readN = m.src.total
		} else {
			ex.readN = m.src.remainStart - m.src.remain
		}
		m.mu.Unlock()
		ex.bodyState = bodyRead
		if !complete {
			ex.closeAfter = true
		}
	}
	ex.c.releaseBody(false) // a body the handler left unread: what is left is not read again
	m.shut()
}
