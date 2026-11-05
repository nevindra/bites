package engine

import (
	"errors"
	"io"
	"os"
	"sync"
	"unsafe"

	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/wire"
)

// Receiving a multipart form (ADR 0044): the framework reads every part before the handler runs.
// A text part becomes a string in the request's slab; a file part becomes an UploadFile, held in
// memory while the request and the app have room for it and in an unlinked temporary file past
// that. Every UploadFile is closed when the request ends, so a reader that outlives the handler
// fails with ErrUploadClosed and never reads another request's bytes (ADR 0008).

var (
	// ErrPartTooLarge is returned by ReadText and ReceiveFile when a part passes the bound the
	// caller gave: the caller words the 413, which names the field.
	ErrPartTooLarge = errors.New("bites: a part is over its bound")
	// ErrUploadStore is the 500 of a file that could not be written to the spill directory.
	ErrUploadStore = &h1.Error{Status: 500, Close: true, Reason: "the upload could not be stored"}
)

// memChunk is the first size of a buffer that holds a file in memory when the body does not say
// how much is coming.
const memChunk = 64 << 10

// UploadFile is one received file. It is closed when the request ends.
type UploadFile struct {
	mu     sync.RWMutex
	mem    []byte
	f      *os.File
	size   int64
	closed bool
	next   *UploadFile
}

// Size is the bytes received.
func (u *UploadFile) Size() int64 { return u.size }

// Open returns an independent reader over the file, or ErrUploadClosed once the request is over.
func (u *UploadFile) Open() (io.ReadSeekCloser, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.closed {
		return nil, ErrUploadClosed
	}
	return &fileReader{u: u}, nil
}

type fileReader struct {
	u      *UploadFile
	off    int64
	closed bool
}

func (r *fileReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, os.ErrClosed
	}
	u := r.u
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.closed {
		return 0, ErrUploadClosed
	}
	if r.off >= u.size {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	var n int
	var err error
	if u.f == nil {
		n = copy(p, u.mem[r.off:])
	} else {
		p = p[:min(int64(len(p)), u.size-r.off)]
		n, err = u.f.ReadAt(p, r.off)
		if err == io.EOF && n > 0 {
			err = nil
		}
	}
	r.off += int64(n)
	return n, err
}

func (r *fileReader) Seek(offset int64, whence int) (int64, error) {
	if r.closed {
		return 0, os.ErrClosed
	}
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = r.off
	case io.SeekEnd:
		base = r.u.size
	default:
		return 0, errors.New("bites: invalid whence in Seek")
	}
	if base+offset < 0 {
		return 0, errors.New("bites: negative position in Seek")
	}
	r.off = base + offset
	return r.off, nil
}

func (r *fileReader) Close() error { r.closed = true; return nil }

// shutFile makes the file unreadable and drops what holds its bytes.
func (u *UploadFile) shutFile() {
	u.mu.Lock()
	u.closed = true
	u.mem = nil
	if u.f != nil {
		u.f.Close()
		u.f = nil
	}
	u.mu.Unlock()
}

// closeFiles closes every file received, gives the request's memory back to the app's share and
// returns the scratch window to the pool. The upload's lock is held.
func (m *Multipart) closeFiles() {
	for u := m.files; u != nil; u = u.next {
		u.shutFile()
	}
	m.files = nil
	if m.reqMem > 0 && m.opt.Share != nil {
		m.opt.Share.Add(-m.reqMem)
	}
	m.reqMem = 0
	if m.scratch != nil {
		windowPool.Put(m.scratch)
		m.scratch = nil
	}
}

func (m *Multipart) scratchWindow() *[UploadWindow]byte {
	if m.scratch == nil {
		m.scratch = windowPool.Get().(*[UploadWindow]byte)
	}
	return m.scratch
}

// bound is an upper bound of the body bytes not yet handed out, or -1 when the body is chunked
// and nothing says.
func (m *Multipart) bound() int64 {
	if m.src.chunked {
		return -1
	}
	return m.src.remain + int64(m.p.Buffered())
}

// readInto reads the current part into b until it is full or the part ends. done reports the end.
// The upload's lock is held.
func (m *Multipart) readInto(b []byte) (n int, done bool, err error) {
	for n < len(b) {
		k, err := m.p.Read(b[n:])
		n += k
		if err == io.EOF {
			return n, true, nil
		}
		if err != nil {
			m.ex.closeAfter = true
			return n, false, err
		}
	}
	return n, false, nil
}

// ReadText reads the whole body of part gen as text, at most limit bytes. A part up to the size of
// the scratch window is copied into the slab (no allocation when it has room); a larger one is
// built in a block of its own. ErrPartTooLarge says the part passed limit.
func (m *Multipart) ReadText(gen uint32, limit int, slab *wire.Slab) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed.Load() {
		return "", ErrUploadClosed
	}
	if gen != m.gen {
		return "", ErrPartGone
	}
	sc := m.scratchWindow()
	n, done, err := m.readInto(sc[:min(limit+1, UploadWindow)])
	if err != nil {
		return "", err
	}
	if n > limit {
		return "", ErrPartTooLarge
	}
	if done {
		return slab.Copy(sc[:n]), nil
	}
	big := append(make([]byte, 0, 2*UploadWindow), sc[:n]...)
	for {
		if len(big) == cap(big) {
			big = append(big, 0)[:len(big)]
		}
		k, done, err := m.readInto(big[len(big):min(cap(big), limit+1)])
		big = big[:len(big)+k]
		if err != nil {
			return "", err
		}
		if len(big) > limit {
			return "", ErrPartTooLarge
		}
		if done {
			if len(big) == 0 {
				return "", nil
			}
			// The block is this part's alone and is never written again.
			return unsafe.String(&big[0], len(big)), nil
		}
	}
}

// reserve asks for n more bytes of memory for this request's files.
func (m *Multipart) reserve(n int64) bool {
	if n <= 0 {
		return true
	}
	if m.reqMem+n > m.opt.MemRequest {
		return false
	}
	if sh := m.opt.Share; sh != nil {
		if sh.Add(n) > m.opt.ShareMax {
			sh.Add(-n)
			return false
		}
	}
	m.reqMem += n
	return true
}

func (m *Multipart) release(n int64) {
	if n <= 0 {
		return
	}
	m.reqMem -= n
	if m.opt.Share != nil {
		m.opt.Share.Add(-n)
	}
}

// ReceiveFile reads the whole body of part gen into an UploadFile of at most max bytes
// (ErrPartTooLarge past it, as the max+1th byte arrives). It is kept in memory while the request
// and the app have room, and in an unlinked temporary file otherwise.
func (m *Multipart) ReceiveFile(gen uint32, max int64) (*UploadFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed.Load() {
		return nil, ErrUploadClosed
	}
	if gen != m.gen {
		return nil, ErrPartGone
	}
	u := &UploadFile{}
	sc := m.scratchWindow()
	n, done, err := m.readInto(sc[:])
	if err != nil {
		return nil, err
	}
	if int64(n) > max {
		return nil, ErrPartTooLarge
	}
	u.size = int64(n)
	if done {
		if m.reserve(int64(n)) {
			u.mem = make([]byte, n)
			copy(u.mem, sc[:n])
			m.attach(u)
			return u, nil
		}
		return m.spill(u, nil, sc[:n], true, max)
	}
	// More than a window: choose a buffer big enough to take what the body says is left, up to
	// what the request may keep; when none can be had the file goes to disk.
	want := int64(2 * n)
	if b := m.bound(); b >= 0 {
		want = int64(n) + b
	}
	want = min(want, m.opt.MemRequest-m.reqMem)
	if want <= int64(n) || !m.reserve(want) {
		return m.spill(u, nil, sc[:n], false, max)
	}
	buf := make([]byte, n, want)
	copy(buf, sc[:n])
	for {
		if len(buf) == cap(buf) {
			// Full: grow by doubling while the request and the app have room, else spill.
			grow := min(int64(cap(buf)), m.opt.MemRequest-m.reqMem)
			if grow <= 0 || !m.reserve(grow) {
				m.release(int64(cap(buf)))
				return m.spill(u, buf, nil, false, max)
			}
			nb := make([]byte, len(buf), cap(buf)+int(grow))
			copy(nb, buf)
			buf = nb
		}
		k, done, err := m.readInto(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+k]
		if err != nil {
			m.release(int64(cap(buf)))
			return nil, err
		}
		if int64(len(buf)) > max {
			m.release(int64(cap(buf)))
			return nil, ErrPartTooLarge
		}
		if done {
			u.mem, u.size = buf, int64(len(buf))
			m.attach(u)
			return u, nil
		}
	}
}

func (m *Multipart) attach(u *UploadFile) {
	u.next = m.files
	m.files = u
}

// spill moves the file to an unlinked temporary file: first mem, then pending (bytes already in
// the scratch window), then the rest of the part. complete says the part has no more bytes.
func (m *Multipart) spill(u *UploadFile, mem, pending []byte, complete bool, max int64) (*UploadFile, error) {
	f, err := os.CreateTemp(m.opt.Dir, "bites-upload-*")
	if err != nil {
		m.ex.closeAfter = true
		return nil, ErrUploadStore
	}
	// Unlinked at once: nothing is left in the directory by a handler that panics or a crash,
	// and the descriptor is the only thing to release.
	os.Remove(f.Name())
	u.f = f
	fail := func(e error) (*UploadFile, error) {
		f.Close()
		u.f = nil
		return nil, e
	}
	write := func(b []byte) error {
		if _, err := f.Write(b); err != nil {
			m.ex.closeAfter = true
			return ErrUploadStore
		}
		return nil
	}
	if len(mem) > 0 {
		if err := write(mem); err != nil {
			return fail(err)
		}
	}
	if len(pending) > 0 {
		if err := write(pending); err != nil {
			return fail(err)
		}
	}
	u.size = int64(len(mem) + len(pending))
	sc := m.scratchWindow()
	for !complete {
		n, done, err := m.readInto(sc[:])
		if err != nil {
			return fail(err)
		}
		u.size += int64(n)
		if u.size > max {
			return fail(ErrPartTooLarge)
		}
		if n > 0 {
			if err := write(sc[:n]); err != nil {
				return fail(err)
			}
		}
		complete = done
	}
	m.attach(u)
	return u, nil
}
