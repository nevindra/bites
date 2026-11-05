package ws

import (
	"encoding/binary"
	"time"
	"unicode/utf8"
)

// Kind says what a call to Feed produced.
type Kind uint8

const (
	KindNeedMore Kind = iota // no event yet: feed more bytes
	KindData                 // a chunk of a text or binary message
	KindPing                 // a complete ping
	KindPong                 // a complete pong
	KindClose                // a complete close frame, after which the Reader discards input
	KindFail                 // a refusal: close the socket with Err.Code
)

// Event is the result of one call to Feed.
type Event struct {
	Kind Kind

	// Opcode is the message's opcode (OpText or OpBinary) for KindData. The codec reports it and
	// decides nothing about binary frames.
	Opcode Opcode

	// Payload aliases the slice given to Feed, unmasked in place, and is valid until the engine
	// reuses that slice. For KindData it is a chunk of the message (empty only with End). For
	// KindPing and KindPong it is the whole payload. For KindClose it is the reason, already
	// checked as UTF-8. A control frame split across reads is assembled in a buffer the Reader
	// owns instead, valid until the next call to Feed.
	Payload []byte

	Start bool // KindData: the first chunk of a message
	End   bool // KindData: the last chunk of a message; a text message is valid UTF-8 as a whole

	Code uint16 // KindClose: the status the client sent, or CloseNoStatus when it sent none
	Err  *Error // KindFail
}

type state uint8

const (
	stHeader state = iota
	stPayload
	stClosed
)

// Reader decodes the frames a client sends, incrementally, over whatever the engine has read. The
// zero value is not ready: call Reset. It does no I/O and holds no message buffer: payload bytes
// come back as chunks of the slice it was fed (see Event.Payload), so the engine decides where a
// message is assembled and the limit of ADR 0046 on its size is judged here before a byte of an
// oversized frame is read.
type Reader struct {
	lim *Limits
	st  state
	err *Error

	hdr   [14]byte // the frame header being read: 2, +2 or +8 of length, +4 of mask
	hn    uint8
	hneed uint8

	fin     bool
	op      Opcode
	key     [4]byte
	pos     uint8 // bytes of this frame's payload consumed, modulo 4: where the mask key is
	remain  uint64
	ctlHave uint8

	inMsg   bool   // a data message is open: the last data frame had FIN clear
	msgOp   Opcode // opcode of the open message
	started bool   // a KindData event of this message was emitted
	frags   int
	msgLen  uint64
	utf     utf8State

	ctl []byte // control frame split across reads; allocated the first time, then kept

	pingAt   int64 // UnixNano of the last ping
	pingDebt int64 // nanoseconds of burst allowance in use; see ping

	echo uint16 // the status the server has itself closed with: the client's echo of it is accepted, see AllowEcho
}

// Reset readies r for a new connection with limits lim, which must stay unchanged while r uses
// them. It panics on a limit that is not positive, or a control limit over 125.
func (r *Reader) Reset(lim *Limits) {
	if lim == nil || !lim.valid() {
		panic("ws: Reset with a missing or non-positive limit")
	}
	ctl := r.ctl
	*r = Reader{lim: lim, hneed: 2, ctl: ctl}
}

// AllowEcho tells the Reader that the server has sent a Close frame with code, so a client Close
// frame carrying the same code is accepted whatever the code is: a browser answers a Close with
// the status it was given, and the server's own 1012 or 1013 (ADR 0046) would otherwise be
// refused with 1002 as a status no client may send. Every other status keeps the rules of
// ClientCloseCode, and an unprompted 1012 to 1014 is still refused.
func (r *Reader) AllowEcho(code uint16) { r.echo = code }

func (r *Reader) fail(e *Error) (Event, bool) {
	r.err = e
	return Event{Kind: KindFail, Err: e}, true
}

// Feed consumes bytes from in and returns how many it used and at most one event. Call it again
// with in[n:] until the input is used up; an event with n < len(in) is not the end of the
// input. now is read only to count pings (ADR 0046). Payload bytes are unmasked in place in in.
func (r *Reader) Feed(in []byte, now time.Time) (n int, ev Event) {
	if r.err != nil {
		return 0, Event{Kind: KindFail, Err: r.err}
	}
	if r.st == stClosed {
		return len(in), Event{}
	}
	for {
		if r.st == stHeader {
			for r.hn < r.hneed && n < len(in) {
				r.hdr[r.hn] = in[n]
				n++
				r.hn++
				if r.hn == 2 {
					if e := r.firstTwo(); e != nil {
						return n, Event{Kind: KindFail, Err: e}
					}
				}
			}
			if r.hn < r.hneed {
				return n, Event{}
			}
			if e := r.header(); e != nil {
				return n, Event{Kind: KindFail, Err: e}
			}
			// A zero-length frame has no payload bytes to wait for.
			if r.remain == 0 {
				if ev, ok := r.finishFrame(nil, now); ok {
					return n, ev
				}
				continue
			}
			r.st = stPayload
		}
		// stPayload
		if n == len(in) {
			return n, Event{}
		}
		take := uint64(len(in) - n)
		if take > r.remain {
			take = r.remain
		}
		chunk := in[n : n+int(take)]
		n += int(take)
		unmask(chunk, r.key, r.pos)
		r.pos = uint8((uint64(r.pos) + take) & 3)
		r.remain -= take
		if r.op.control() {
			ev, ok := r.controlChunk(chunk, now)
			if ok {
				return n, ev
			}
			continue
		}
		if ev, ok := r.dataChunk(chunk); ok {
			return n, ev
		}
	}
}

// firstTwo judges what the first two bytes show: reserved bits, opcode, mask bit, and for a
// control frame that it is whole and short. It is what fails a bad frame before its length is read.
func (r *Reader) firstTwo() *Error {
	b0, b1 := r.hdr[0], r.hdr[1]
	if b0&0x70 != 0 {
		return r.setErr(ErrRSV)
	}
	r.fin = b0&0x80 != 0
	r.op = Opcode(b0 & 0x0f)
	switch r.op {
	case OpContinuation, OpText, OpBinary, OpClose, OpPing, OpPong:
	default:
		return r.setErr(ErrOpcode)
	}
	if b1&0x80 == 0 {
		return r.setErr(ErrUnmasked)
	}
	code := b1 & 0x7f
	if r.op.control() {
		if !r.fin {
			return r.setErr(ErrControlFrag)
		}
		if code > 125 || int(code) > r.lim.MaxControl {
			return r.setErr(ErrControlSize)
		}
	} else if r.op == OpContinuation {
		if !r.inMsg {
			return r.setErr(ErrContinuation)
		}
	} else if r.inMsg {
		return r.setErr(ErrInterleaved)
	}
	need := uint8(2 + 4)
	switch code {
	case 126:
		need += 2
	case 127:
		need += 8
	}
	r.hneed = need
	return nil
}

func (r *Reader) setErr(e *Error) *Error {
	r.err = e
	return e
}

// header reads the length and mask from the complete header and judges the length.
func (r *Reader) header() *Error {
	code := r.hdr[1] & 0x7f
	off := 2
	var length uint64
	switch code {
	case 126:
		length = uint64(binary.BigEndian.Uint16(r.hdr[2:4]))
		if length < 126 {
			return r.setErr(ErrNonMinimal)
		}
		off = 4
	case 127:
		length = binary.BigEndian.Uint64(r.hdr[2:10])
		if length>>63 != 0 {
			return r.setErr(ErrLengthMSB)
		}
		if length < 1<<16 {
			return r.setErr(ErrNonMinimal)
		}
		off = 10
	default:
		length = uint64(code)
	}
	copy(r.key[:], r.hdr[off:off+4])
	r.pos = 0
	r.remain = length
	r.hn = 0
	r.hneed = 2
	if r.op.control() {
		r.ctlHave = 0
		return nil
	}
	// A data frame: count the fragment and judge the message size before reading any of it.
	if r.op != OpContinuation {
		r.msgOp = r.op
		r.msgLen = 0
		r.frags = 0
		r.started = false
		r.utf.reset()
	}
	r.frags++
	if r.frags > r.lim.MaxFragments {
		return r.setErr(ErrFragments)
	}
	if r.msgLen+length > uint64(r.lim.MaxMessage) {
		return r.setErr(ErrMessageSize)
	}
	r.inMsg = !r.fin
	return nil
}

// dataChunk handles bytes of a data frame's payload, already unmasked.
func (r *Reader) dataChunk(chunk []byte) (Event, bool) {
	if r.msgOp == OpText && !r.utf.write(chunk) {
		return r.fail(ErrTextUTF8)
	}
	r.msgLen += uint64(len(chunk))
	return r.dataEvent(chunk)
}

func (r *Reader) dataEvent(chunk []byte) (Event, bool) {
	end := r.remain == 0 && r.fin
	if end && r.msgOp == OpText && !r.utf.complete() {
		return r.fail(ErrTextUTF8)
	}
	if r.remain == 0 {
		r.st = stHeader
	}
	if len(chunk) == 0 && !end {
		return Event{}, false
	}
	ev := Event{Kind: KindData, Opcode: r.msgOp, Payload: chunk, Start: !r.started, End: end}
	r.started = true
	return ev, true
}

// finishFrame completes a frame that has no payload bytes.
func (r *Reader) finishFrame(chunk []byte, now time.Time) (Event, bool) {
	if r.op.control() {
		return r.controlDone(chunk, now)
	}
	r.st = stHeader
	return r.dataEvent(nil)
}

// controlChunk takes bytes of a control frame. A frame that arrives whole in one read is
// reported straight from the caller's slice; one that is split is gathered in r.ctl.
func (r *Reader) controlChunk(chunk []byte, now time.Time) (Event, bool) {
	if r.ctlHave == 0 && r.remain == 0 {
		return r.controlDone(chunk, now)
	}
	if r.ctl == nil {
		r.ctl = make([]byte, 125)
	}
	r.ctlHave += uint8(copy(r.ctl[r.ctlHave:], chunk))
	if r.remain > 0 {
		return Event{}, false
	}
	return r.controlDone(r.ctl[:r.ctlHave], now)
}

func (r *Reader) controlDone(payload []byte, now time.Time) (Event, bool) {
	r.st = stHeader
	switch r.op {
	case OpPing:
		if !r.pingAllowed(now) {
			return r.fail(ErrPingRate)
		}
		return Event{Kind: KindPing, Payload: payload}, true
	case OpPong:
		// A pong is a legal keep-alive and never an error by itself, but it drains the same bucket
		// as a ping (ADR 0046), so a flood of either closes the socket the same way.
		if !r.pingAllowed(now) {
			return r.fail(ErrPingRate)
		}
		return Event{Kind: KindPong, Payload: payload}, true
	}
	// OpClose
	r.st = stClosed
	switch len(payload) {
	case 0:
		return Event{Kind: KindClose, Code: CloseNoStatus}, true
	case 1:
		return r.fail(ErrCloseLength)
	}
	code := binary.BigEndian.Uint16(payload)
	if !ClientCloseCode(code) && (r.echo == 0 || code != r.echo) {
		return r.fail(ErrCloseCode)
	}
	if !utf8.Valid(payload[2:]) {
		return r.fail(ErrCloseReason)
	}
	return Event{Kind: KindClose, Code: code, Payload: payload[2:]}, true
}

// pingAllowed is a token bucket of PingsPerSecond tokens refilled at that rate: a client may burst
// that many pings and then one per 1/PingsPerSecond of a second. debt is the time the pings sent
// so far would take to pay back; it leaks as time passes and may not exceed one second.
func (r *Reader) pingAllowed(now time.Time) bool {
	t := now.UnixNano()
	if r.pingAt != 0 && t > r.pingAt {
		r.pingDebt -= t - r.pingAt
		if r.pingDebt < 0 {
			r.pingDebt = 0
		}
	}
	r.pingAt = t
	r.pingDebt += int64(time.Second) / int64(r.lim.PingsPerSecond)
	return r.pingDebt <= int64(time.Second)
}

// unmask XORs b with key, where b starts pos bytes into the frame's payload.
func unmask(b []byte, key [4]byte, pos uint8) {
	if len(b) == 0 {
		return
	}
	if pos &= 3; pos != 0 {
		key = [4]byte{key[pos], key[(pos+1)&3], key[(pos+2)&3], key[(pos+3)&3]}
	}
	k := uint64(binary.LittleEndian.Uint32(key[:]))
	k |= k << 32
	// 32 bytes an iteration: one bounds check for four words, and four independent XORs.
	for len(b) >= 32 {
		w := b[:32:32]
		binary.LittleEndian.PutUint64(w[0:], binary.LittleEndian.Uint64(w[0:])^k)
		binary.LittleEndian.PutUint64(w[8:], binary.LittleEndian.Uint64(w[8:])^k)
		binary.LittleEndian.PutUint64(w[16:], binary.LittleEndian.Uint64(w[16:])^k)
		binary.LittleEndian.PutUint64(w[24:], binary.LittleEndian.Uint64(w[24:])^k)
		b = b[32:]
	}
	for len(b) >= 8 {
		binary.LittleEndian.PutUint64(b, binary.LittleEndian.Uint64(b)^k)
		b = b[8:]
	}
	for i := range b {
		b[i] ^= key[i&3]
	}
}
