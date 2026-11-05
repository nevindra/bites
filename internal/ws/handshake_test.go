package ws

import (
	"strings"
	"testing"

	"github.com/nevindra/bites/internal/h1"
)

const goodKey = "dGhlIHNhbXBsZSBub25jZQ=="

func head(t *testing.T, method, proto string, headers ...string) *h1.Request {
	t.Helper()
	s := method + " /chat " + proto + "\r\nHost: example.com\r\n" + strings.Join(headers, "\r\n")
	if len(headers) > 0 {
		s += "\r\n"
	}
	s += "\r\n"
	var req h1.Request
	if _, err := h1.ParseHead([]byte(s), &req, h1.DefaultLimits()); err != nil {
		t.Fatalf("test head did not parse: %v", err)
	}
	return &req
}

func good() []string {
	return []string{"Upgrade: websocket", "Connection: Upgrade", "Sec-WebSocket-Version: 13", "Sec-WebSocket-Key: " + goodKey}
}

func without(h []string, prefix string) []string {
	var out []string
	for _, l := range h {
		if !strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func TestTheAcceptKeyOfRFC6455IsComputed(t *testing.T) {
	if got := string(AppendAccept(nil, []byte(goodKey))); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("got %s", got)
	}
}

func TestTheUpgradeResponseNamesTheAcceptKey(t *testing.T) {
	got := string(AppendUpgradeResponse(nil, []byte(goodKey)))
	want := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestAWellFormedHandshakeIsAccepted(t *testing.T) {
	key, err := CheckHandshake(head(t, "GET", "HTTP/1.1", good()...))
	if err != nil || string(key) != goodKey {
		t.Fatalf("key %q err %v", key, err)
	}
}

func TestHeaderNamesAndTokensAreComparedWithoutCaseAndAcrossLinesAndLists(t *testing.T) {
	h := []string{"upgrade: h2c, WebSocket", "CONNECTION: keep-alive", "connection: keep-alive,  UPGRADE", "sec-websocket-version:  13 ", "SEC-WEBSOCKET-KEY: " + goodKey}
	if _, err := CheckHandshake(head(t, "GET", "HTTP/1.1", h...)); err != nil {
		t.Fatalf("refused: %v", err)
	}
}

func TestAHandshakeWithVersion8IsA426NamingVersion13(t *testing.T) {
	h := append(without(good(), "Sec-WebSocket-Version"), "Sec-WebSocket-Version: 8")
	_, err := CheckHandshake(head(t, "GET", "HTTP/1.1", h...))
	if err == nil || err.Status != 426 || !err.WantVersion || !strings.Contains(err.Reason, "13") {
		t.Fatalf("got %+v", err)
	}
}

func TestAHandshakeWithNoVersionOrAListOfVersionsIsA426(t *testing.T) {
	for _, h := range [][]string{without(good(), "Sec-WebSocket-Version"), append(without(good(), "Sec-WebSocket-Version"), "Sec-WebSocket-Version: 13, 8"), append(without(good(), "Sec-WebSocket-Version"), "Sec-WebSocket-Version: 013")} {
		if _, err := CheckHandshake(head(t, "GET", "HTTP/1.1", h...)); err == nil || err.Status != 426 || !err.WantVersion {
			t.Errorf("got %+v for %v", err, h)
		}
	}
}

func TestAHandshakeThatIsNotAGETIs405(t *testing.T) {
	if _, err := CheckHandshake(head(t, "POST", "HTTP/1.1", good()...)); err == nil || err.Status != 405 {
		t.Fatalf("got %+v", err)
	}
}

func TestAHandshakeOverHTTP10Is400(t *testing.T) {
	if _, err := CheckHandshake(head(t, "GET", "HTTP/1.0", good()...)); err == nil || err.Status != 400 {
		t.Fatalf("got %+v", err)
	}
}

func TestAHandshakeWithoutUpgradeWebsocketOrConnectionUpgradeIs400(t *testing.T) {
	for _, h := range [][]string{
		without(good(), "Upgrade"),
		without(good(), "Connection"),
		append(without(good(), "Upgrade"), "Upgrade: h2c"),
		append(without(good(), "Connection"), "Connection: keep-alive"),
		append(without(good(), "Upgrade"), "Upgrade: websockets"),
		append(without(good(), "Connection"), "Connection: upgraded"),
	} {
		if _, err := CheckHandshake(head(t, "GET", "HTTP/1.1", h...)); err == nil || err.Status != 400 || err.WantVersion {
			t.Errorf("got %+v for %v", err, h)
		}
	}
}

func TestAHandshakeKeyThatIsNotBase64Of16BytesIs400(t *testing.T) {
	for _, k := range []string{"", "short", "dGhlIHNhbXBsZSBub25jZQ", "dGhlIHNhbXBsZSBub25jZQ=", "dGhlIHNhbXBsZSBub25jZQ===", "dGhlIHNhbXBsZSBub25j!Q==", "dGhlIHNhbXBsZSBub25jZXJ0YQ==", "AAAAAAAAAAAAAAAAAAAAAAAA"} {
		h := append(without(good(), "Sec-WebSocket-Key"), "Sec-WebSocket-Key: "+k)
		if _, err := CheckHandshake(head(t, "GET", "HTTP/1.1", h...)); err == nil || err.Status != 400 {
			t.Errorf("key %q: got %+v", k, err)
		}
	}
	if _, err := CheckHandshake(head(t, "GET", "HTTP/1.1", without(good(), "Sec-WebSocket-Key")...)); err == nil || err.Status != 400 {
		t.Errorf("no key: got %+v", err)
	}
}

func TestAHandshakeWithTwoKeysOrTwoVersionsIs400(t *testing.T) {
	for _, extra := range []string{"Sec-WebSocket-Key: " + goodKey, "Sec-WebSocket-Version: 13"} {
		if _, err := CheckHandshake(head(t, "GET", "HTTP/1.1", append(good(), extra)...)); err == nil || err.Status != 400 {
			t.Errorf("got %+v for %s", err, extra)
		}
	}
}

func TestCheckingAHandshakeAllocatesNothing(t *testing.T) {
	req := head(t, "GET", "HTTP/1.1", good()...)
	if a := testing.AllocsPerRun(200, func() { _, _ = CheckHandshake(req) }); a != 0 {
		t.Fatalf("%v allocations", a)
	}
}
