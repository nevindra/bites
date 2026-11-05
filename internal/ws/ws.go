package ws

import "time"

// Opcode is the 4-bit opcode of a frame (RFC 6455 section 5.2).
type Opcode uint8

// The opcodes. Every other value is reserved and refused with 1002.
const (
	OpContinuation Opcode = 0x0
	OpText         Opcode = 0x1
	OpBinary       Opcode = 0x2
	OpClose        Opcode = 0x8
	OpPing         Opcode = 0x9
	OpPong         Opcode = 0xA
)

func (o Opcode) control() bool { return o&0x8 != 0 }

// The close statuses of RFC 6455 section 7.4.1 that this codec names.
const (
	CloseNormal        uint16 = 1000
	CloseGoingAway     uint16 = 1001
	CloseProtocolError uint16 = 1002
	CloseUnsupported   uint16 = 1003
	CloseNoStatus      uint16 = 1005 // never on the wire: reported when a Close frame has no payload
	CloseAbnormal      uint16 = 1006 // never on the wire
	CloseInvalidData   uint16 = 1007
	ClosePolicy        uint16 = 1008
	CloseTooBig        uint16 = 1009
	CloseExtension     uint16 = 1010
	CloseInternal      uint16 = 1011
	CloseRestart       uint16 = 1012
	CloseTryAgain      uint16 = 1013
	CloseBadGateway    uint16 = 1014
	CloseTLSFailure    uint16 = 1015 // never on the wire
)

// ClientCloseCode reports whether a client may put code in a Close frame: 1000 to 1003, 1007 to
// 1011, and the application range 3000 to 4999. 1004, 1005, 1006 and 1015 are reserved and must
// never appear on the wire; 0 to 999, 1016 to 2999 are unassigned. 1012 to 1014 are refused as
// well, as the Autobahn suite does: they are server statuses (restart, try again, bad gateway).
func ClientCloseCode(code uint16) bool {
	switch {
	case code >= 1000 && code <= 1003, code >= 1007 && code <= 1011:
		return true
	}
	return code >= 3000 && code <= 4999
}

// ServerCloseCode reports whether the server may send code: the client set plus 1012 to 1014.
func ServerCloseCode(code uint16) bool {
	return ClientCloseCode(code) || (code >= 1012 && code <= 1014)
}

// Limit names which inbound limit of ADR 0046 a refusal came from. LimitNone is a plain
// protocol violation that no configurable limit governs.
type Limit uint8

const (
	LimitNone      Limit = iota
	LimitMessage         // a reassembled message over Limits.MaxMessage: 1009
	LimitFragments       // more than Limits.MaxFragments fragments in one message: 1009
	LimitControl         // a control frame over Limits.MaxControl, or fragmented: 1002
	LimitPingRate        // more than Limits.PingsPerSecond pings in a second: 1008
	LimitCloseTime       // a close handshake over Limits.CloseTimeout: the connection is dropped; judged by the engine on the wheel
)

var limitNames = [...]string{"none", "message size", "fragment count", "control frame", "ping rate", "close handshake time"}

func (l Limit) String() string {
	if int(l) < len(limitNames) {
		return limitNames[l]
	}
	return "unknown"
}

// Limits are the inbound limits of ADR 0046. A Reader holds a pointer to one, so the engine
// shares one value per route; it must not change while a Reader uses it.
type Limits struct {
	MaxMessage     int           // bytes of one message, reassembled; default 64 KiB
	MaxFragments   int           // frames of one message; default 128
	MaxControl     int           // payload of a control frame; default and ceiling 125
	PingsPerSecond int           // pings a client may send a second, as a burst of that size; default 32
	CloseTimeout   time.Duration // how long a close handshake may stay unfinished; default 5 s. Carried for the engine: the codec cannot judge time between calls.
}

// DefaultLimits returns the defaults of ADR 0046.
func DefaultLimits() Limits {
	return Limits{MaxMessage: 64 << 10, MaxFragments: 128, MaxControl: 125, PingsPerSecond: 32, CloseTimeout: 5 * time.Second}
}

func (l *Limits) valid() bool {
	return l.MaxMessage > 0 && l.MaxFragments > 0 && l.MaxControl > 0 && l.MaxControl <= 125 && l.PingsPerSecond > 0 && l.CloseTimeout > 0
}

// Error is a refusal: close the socket with Code. Limit names the limit of ADR 0046 that was
// hit, or LimitNone for a violation of the protocol itself.
type Error struct {
	Code   uint16
	Limit  Limit
	Reason string
}

func (e *Error) Error() string { return e.Reason }

func refuse(code uint16, l Limit, reason string) *Error {
	return &Error{Code: code, Limit: l, Reason: reason}
}

// The refusals. Shared, so a hostile client costs no allocation.
var (
	ErrUnmasked     = refuse(CloseProtocolError, LimitNone, "client frame is not masked")
	ErrRSV          = refuse(CloseProtocolError, LimitNone, "RSV bit set with no extension negotiated")
	ErrOpcode       = refuse(CloseProtocolError, LimitNone, "reserved opcode")
	ErrControlFrag  = refuse(CloseProtocolError, LimitControl, "fragmented control frame")
	ErrControlSize  = refuse(CloseProtocolError, LimitControl, "control frame payload too long")
	ErrNonMinimal   = refuse(CloseProtocolError, LimitNone, "payload length not minimally encoded")
	ErrLengthMSB    = refuse(CloseProtocolError, LimitNone, "payload length has its high bit set")
	ErrContinuation = refuse(CloseProtocolError, LimitNone, "continuation frame with no message to continue")
	ErrInterleaved  = refuse(CloseProtocolError, LimitNone, "new data frame inside a fragmented message")
	ErrMessageSize  = refuse(CloseTooBig, LimitMessage, "message too big")
	ErrFragments    = refuse(CloseTooBig, LimitFragments, "too many fragments")
	ErrPingRate     = refuse(ClosePolicy, LimitPingRate, "too many pings")
	ErrTextUTF8     = refuse(CloseInvalidData, LimitNone, "text message is not valid UTF-8")
	ErrCloseLength  = refuse(CloseProtocolError, LimitNone, "close payload of one byte")
	ErrCloseCode    = refuse(CloseProtocolError, LimitNone, "close status not allowed on the wire")
	ErrCloseReason  = refuse(CloseInvalidData, LimitNone, "close reason is not valid UTF-8")
)
