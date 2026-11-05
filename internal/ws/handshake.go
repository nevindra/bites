package ws

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"

	"github.com/nevindra/bites/internal/h1"
)

// guid is the constant RFC 6455 section 1.3 appends to the client's key.
const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// HandshakeError is a refused upgrade request: answer Status. When WantVersion is set the answer
// carries Sec-WebSocket-Version: 13, as RFC 6455 section 4.4 asks of a 426. The answer's body
// and media type (problem+json, ADR 0012) are the engine's.
type HandshakeError struct {
	Status      int
	Reason      string
	WantVersion bool
}

func (e *HandshakeError) Error() string { return e.Reason }

var (
	errHSMethod     = &HandshakeError{Status: 405, Reason: "a WebSocket handshake is a GET"}
	errHSProto      = &HandshakeError{Status: 400, Reason: "a WebSocket handshake needs HTTP/1.1"}
	errHSUpgrade    = &HandshakeError{Status: 400, Reason: "missing Upgrade: websocket"}
	errHSConnection = &HandshakeError{Status: 400, Reason: "missing Connection: Upgrade"}
	errHSVersion    = &HandshakeError{Status: 426, Reason: "unsupported Sec-WebSocket-Version: only version 13 is served", WantVersion: true}
	errHSKey        = &HandshakeError{Status: 400, Reason: "missing or malformed Sec-WebSocket-Key"}
	errHSDupVersion = &HandshakeError{Status: 400, Reason: "more than one Sec-WebSocket-Version"}
	errHSDupKey     = &HandshakeError{Status: 400, Reason: "more than one Sec-WebSocket-Key"}
)

// CheckHandshake judges a parsed request as a WebSocket opening handshake: method GET, HTTP/1.1,
// an Upgrade header listing websocket, a Connection header listing upgrade (tokens compared
// without case, over every header line of that name), Sec-WebSocket-Version exactly 13 and a
// Sec-WebSocket-Key that is the base64 of 16 bytes. It looks at nothing else: Origin, the route
// and subprotocols are the engine's. The key it returns aliases req's buffer.
func CheckHandshake(req *h1.Request) (key []byte, err *HandshakeError) {
	if string(req.Method) != "GET" {
		return nil, errHSMethod
	}
	if req.Minor < 1 {
		return nil, errHSProto
	}
	var upgrade, connection, haveVersion, haveKey, versionOK bool
	for _, h := range req.Headers {
		switch {
		case equalFold(h.Name, "Upgrade"):
			upgrade = upgrade || hasToken(h.Value, "websocket")
		case equalFold(h.Name, "Connection"):
			connection = connection || hasToken(h.Value, "upgrade")
		case equalFold(h.Name, "Sec-WebSocket-Version"):
			if haveVersion {
				return nil, errHSDupVersion
			}
			haveVersion = true
			versionOK = string(bytes.Trim(h.Value, " \t")) == "13"
		case equalFold(h.Name, "Sec-WebSocket-Key"):
			if haveKey {
				return nil, errHSDupKey
			}
			haveKey = true
			key = bytes.Trim(h.Value, " \t")
		}
	}
	switch {
	case !upgrade:
		return nil, errHSUpgrade
	case !connection:
		return nil, errHSConnection
	case !versionOK:
		return nil, errHSVersion
	case !validKey(key):
		return nil, errHSKey
	}
	return key, nil
}

// validKey reports whether k is the base64 of exactly 16 bytes (24 characters, ending "==").
func validKey(k []byte) bool {
	if len(k) != 24 {
		return false
	}
	var out [18]byte
	n, err := strictB64.Decode(out[:], k)
	return err == nil && n == 16
}

// AppendAccept appends the Sec-WebSocket-Accept value for key: base64 of the SHA-1 of the key
// followed by the RFC's GUID. It allocates only if dst must grow.
func AppendAccept(dst, key []byte) []byte {
	var in [24 + len(guid)]byte
	n := copy(in[:], key)
	if n > 24 {
		n = 24
	}
	copy(in[n:], guid)
	sum := sha1.Sum(in[:n+len(guid)])
	return base64.StdEncoding.AppendEncode(dst, sum[:])
}

// AppendUpgradeResponse appends the 101 answer to a handshake with key. Choosing a subprotocol
// or anything else to add to it is the engine's.
func AppendUpgradeResponse(dst, key []byte) []byte {
	dst = append(dst, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "...)
	dst = AppendAccept(dst, key)
	return append(dst, "\r\n\r\n"...)
}

func equalFold(name []byte, s string) bool {
	if len(name) != len(s) {
		return false
	}
	for i := range name {
		c := name[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		d := s[i]
		if 'A' <= d && d <= 'Z' {
			d += 'a' - 'A'
		}
		if c != d {
			return false
		}
	}
	return true
}

// hasToken reports whether the comma-separated list v holds tok, compared without case.
func hasToken(v []byte, tok string) bool {
	for len(v) > 0 {
		var item []byte
		if i := bytes.IndexByte(v, ','); i >= 0 {
			item, v = v[:i], v[i+1:]
		} else {
			item, v = v, nil
		}
		if equalFold(bytes.Trim(item, " \t"), tok) {
			return true
		}
	}
	return false
}

var strictB64 = base64.StdEncoding.Strict()
