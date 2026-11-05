package ws

import (
	"encoding/binary"
	"errors"
)

// ErrControlTooLong is returned for a control frame payload over 125 bytes.
var ErrControlTooLong = errors.New("ws: control frame payload over 125 bytes")

// ErrCloseStatus is returned for a close status the server may not send.
var ErrCloseStatus = errors.New("ws: close status not allowed on the wire")

// HeaderLen is the size of the header of an unmasked frame with an n byte payload: 2, 4 or 10.
func HeaderLen(n int) int {
	switch {
	case n < 126:
		return 2
	case n < 1<<16:
		return 4
	}
	return 10
}

// AppendHeader appends the header of an unmasked frame whose payload is n bytes, with the
// minimal length encoding. For a continuation fragment pass OpContinuation.
func AppendHeader(dst []byte, fin bool, op Opcode, n int) []byte {
	b0 := byte(op) & 0x0f
	if fin {
		b0 |= 0x80
	}
	switch {
	case n < 126:
		return append(dst, b0, byte(n))
	case n < 1<<16:
		return append(dst, b0, 126, byte(n>>8), byte(n))
	}
	return binary.BigEndian.AppendUint64(append(dst, b0, 127), uint64(n))
}

// AppendFrame appends one unmasked frame with FIN set. dst is returned unchanged in capacity if
// it had room, so a caller that sized it allocates nothing.
func AppendFrame(dst []byte, op Opcode, payload []byte) []byte {
	return append(AppendHeader(dst, true, op, len(payload)), payload...)
}

// AppendFragment appends one unmasked frame of a fragmented message: OpText or OpBinary with
// fin false to start it, OpContinuation after that, fin true on the last.
func AppendFragment(dst []byte, fin bool, op Opcode, payload []byte) []byte {
	return append(AppendHeader(dst, fin, op, len(payload)), payload...)
}

// AppendPing appends a ping, or ErrControlTooLong.
func AppendPing(dst, payload []byte) ([]byte, error) { return appendControl(dst, OpPing, payload) }

// AppendPong appends a pong, or ErrControlTooLong.
func AppendPong(dst, payload []byte) ([]byte, error) { return appendControl(dst, OpPong, payload) }

func appendControl(dst []byte, op Opcode, payload []byte) ([]byte, error) {
	if len(payload) > 125 {
		return dst, ErrControlTooLong
	}
	return AppendFrame(dst, op, payload), nil
}

// AppendClose appends a close frame with a status and a reason. A status that may not appear
// on the wire (1005, 1006, 1015, and anything outside the ranges of ServerCloseCode) is
// ErrCloseStatus; a reason that would take the payload over 125 bytes is ErrControlTooLong. The
// caller keeps the reason valid UTF-8; the writer does not rescan it.
func AppendClose(dst []byte, code uint16, reason string) ([]byte, error) {
	if !ServerCloseCode(code) {
		return dst, ErrCloseStatus
	}
	if len(reason) > 123 {
		return dst, ErrControlTooLong
	}
	dst = AppendHeader(dst, true, OpClose, 2+len(reason))
	dst = binary.BigEndian.AppendUint16(dst, code)
	return append(dst, reason...), nil
}

// AppendClientFrame appends a masked frame as a client sends it, for tests and in-memory clients.
// The server never writes a masked frame.
func AppendClientFrame(dst []byte, fin bool, op Opcode, payload []byte, key [4]byte) []byte {
	b0 := byte(op) & 0x0f
	if fin {
		b0 |= 0x80
	}
	n := len(payload)
	switch {
	case n < 126:
		dst = append(dst, b0, 0x80|byte(n))
	case n < 1<<16:
		dst = append(dst, b0, 0x80|126, byte(n>>8), byte(n))
	default:
		dst = binary.BigEndian.AppendUint64(append(dst, b0, 0x80|127), uint64(n))
	}
	dst = append(dst, key[:]...)
	start := len(dst)
	dst = append(dst, payload...)
	unmask(dst[start:], key, 0)
	return dst
}
