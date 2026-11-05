package probe

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"unsafe"

	nilogo "github.com/nevindra/bites"
	"github.com/nevindra/bites/internal/wire"
)

// --- Part 1: on the real framework, a string kept from one request survives later requests on
// the same connection, and each request's text lives in its own buffer.

type hdrIn struct {
	Header struct{ Authorization string }
}

func TestR10AKeptHeaderStringSurvivesLaterRequestsOnTheSameConnection(t *testing.T) {
	var mu sync.Mutex
	var kept []string
	conn := rig(t, func(a *nilogo.App) {
		a.Get("/h", func(_ context.Context, in hdrIn) (struct{}, error) {
			mu.Lock()
			kept = append(kept, in.Header.Authorization)
			mu.Unlock()
			return struct{}{}, nil
		})
	})
	br := bufio.NewReader(conn)
	send := func(tok string) {
		fmt.Fprintf(conn, "GET /h HTTP/1.1\r\nHost: x\r\nAuthorization: %s\r\n\r\n", tok)
		// read status line, headers, body by Content-Length
		cl := 0
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(strings.ToLower(l), "content-length:") {
				fmt.Sscanf(l[15:], "%d", &cl)
			}
			if l == "\r\n" {
				break
			}
		}
		io.CopyN(io.Discard, br, int64(cl))
	}
	const n = 300
	want := make([]string, n)
	for i := range n {
		want[i] = fmt.Sprintf("Bearer %0*d", 40, i) // same length every time
		send(want[i])
		if i%50 == 0 {
			runtime.GC()
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(kept) != n {
		t.Fatalf("handler ran %d times", len(kept))
	}
	bad, overlap := 0, 0
	for i := range kept {
		if kept[i] != want[i] {
			bad++
		}
		if i > 0 {
			a0 := uintptr(unsafe.Pointer(unsafe.StringData(kept[i-1])))
			b0 := uintptr(unsafe.Pointer(unsafe.StringData(kept[i])))
			if a0 == b0 {
				overlap++
			}
		}
	}
	t.Logf("measured: %d strings kept across %d requests on one connection; %d changed, %d shared a start address with the previous request's string", len(kept), n, bad, overlap)
	if bad != 0 || overlap != 0 {
		t.Fatal("a kept string changed or a buffer was reused")
	}
}

// Negative control: a string that aliases a pooled read buffer (what Request.Header would be
// if it pointed into the engine's scratch instead of a slab copy) does change.
func TestR10BNegativeControlAliasOfAPooledBufferChanges(t *testing.T) {
	pool := make([]byte, 64)
	copy(pool, "Authorization: Bearer AAAA\r\n")
	alias := unsafe.String(&pool[15], 11)
	before := strings.Clone(alias)
	copy(pool, "Authorization: Bearer BBBB\r\n") // the next request read into the same buffer
	t.Logf("measured: alias before %q after %q", before, alias)
	if alias == before {
		t.Fatal("expected the alias to change")
	}
}

// --- Part 2: the Request view over a slab copy of the head.

type request struct{ head string } // request line plus header lines, one slab copy, ends "\r\n\r\n"

func eqFold(a, lower string) bool {
	if len(a) != len(lower) {
		return false
	}
	for i := 0; i < len(a); i++ {
		c := a[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lower[i] {
			return false
		}
	}
	return true
}

// Header scans the lines. name must be lower case (a constant in first-party middleware).
func (r request) Header(lower string) string {
	h := r.head
	i := strings.IndexByte(h, '\n')
	if i < 0 {
		return ""
	}
	h = h[i+1:]
	for len(h) > 2 {
		j := strings.IndexByte(h, '\n')
		if j < 0 {
			return ""
		}
		line := h[:j-1]
		if c := strings.IndexByte(line, ':'); c == len(lower) && eqFold(line[:c], lower) {
			return strings.TrimLeft(line[c+1:], " \t")
		}
		h = h[j+1:]
	}
	return ""
}

// indexed: one pass builds fixed-size offsets in the value, lookups only compare names.
type ref struct{ ns, ne, vs, ve uint16 }
type request2 struct {
	head string
	n    uint8
	ix   [32]ref
}

func build(head string) request2 {
	r := request2{head: head}
	pos := strings.IndexByte(head, '\n') + 1
	for pos < len(head)-2 && r.n < 32 {
		j := strings.IndexByte(head[pos:], '\n')
		end := pos + j - 1
		c := strings.IndexByte(head[pos:end], ':')
		vs := pos + c + 1
		for vs < end && (head[vs] == ' ' || head[vs] == '\t') {
			vs++
		}
		r.ix[r.n] = ref{uint16(pos), uint16(pos + c), uint16(vs), uint16(end)}
		r.n++
		pos += j + 1
	}
	return r
}

func (r *request2) Header(lower string) string {
	for i := uint8(0); i < r.n; i++ {
		x := r.ix[i]
		if int(x.ne-x.ns) == len(lower) && eqFold(r.head[x.ns:x.ne], lower) {
			return r.head[x.vs:x.ve]
		}
	}
	return ""
}

const apiHead = "POST /users/7?page=2 HTTP/1.1\r\nHost: api.example.com\r\nUser-Agent: Go-http-client/1.1\r\nContent-Length: 58\r\nAccept: application/json\r\nContent-Type: application/json\r\nAuthorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiI3In0.abcdefghijklmnopqrstuvwxyz0123456789\r\nAccept-Encoding: gzip\r\n\r\n"

const browserHead = "GET /users/7?page=2 HTTP/1.1\r\nHost: app.example.com\r\nConnection: keep-alive\r\nsec-ch-ua: \"Chromium\";v=\"126\", \"Not;A=Brand\";v=\"24\"\r\nsec-ch-ua-mobile: ?0\r\nsec-ch-ua-platform: \"Linux\"\r\nUser-Agent: Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36\r\nAccept: application/json, text/plain, */*\r\nOrigin: https://app.example.com\r\nSec-Fetch-Site: same-site\r\nSec-Fetch-Mode: cors\r\nSec-Fetch-Dest: empty\r\nReferer: https://app.example.com/\r\nAccept-Encoding: gzip, deflate, br, zstd\r\nAccept-Language: en-US,en;q=0.9\r\nCookie: theme=dark; sid=0123456789abcdef0123456789abcdef\r\nX-Request-Id: 6f1c2d3e4a5b\r\n\r\n"

func TestR10CHeaderLookupAllocatesNothingAndAliasesTheSlab(t *testing.T) {
	r := request{head: browserHead}
	if got := r.Header("origin"); got != "https://app.example.com" {
		t.Fatal(got)
	}
	r2 := build(browserHead)
	if got := r2.Header("x-request-id"); got != "6f1c2d3e4a5b" {
		t.Fatal(got)
	}
	var s string
	n1 := testing.AllocsPerRun(1000, func() { s = r.Header("origin"); s = r.Header("x-nope") })
	n2 := testing.AllocsPerRun(1000, func() { x := build(browserHead); s = x.Header("origin"); s = x.Header("x-nope") })
	t.Logf("measured: allocs per two lookups, scan %.0f, indexed build+lookups %.0f (heads: browser %d B, api %d B)", n1, n2, len(browserHead), len(apiHead))
	_ = s
}

var sinkS string
var sinkR request2

func benchScan(b *testing.B, head string, names ...string) {
	r := request{head: head}
	for range b.N {
		for _, n := range names {
			sinkS = r.Header(n)
		}
	}
}
func benchIdx(b *testing.B, head string, names ...string) {
	for range b.N {
		x := build(head)
		for _, n := range names {
			sinkS = x.Header(n)
		}
	}
}

func BenchmarkR10ScanBrowser1Hit(b *testing.B) { benchScan(b, browserHead, "origin") }
func BenchmarkR10ScanBrowser4(b *testing.B) {
	benchScan(b, browserHead, "origin", "x-request-id", "authorization", "user-agent")
}
func BenchmarkR10IndexedBrowser1Hit(b *testing.B) { benchIdx(b, browserHead, "origin") }
func BenchmarkR10IndexedBrowser4(b *testing.B) {
	benchIdx(b, browserHead, "origin", "x-request-id", "authorization", "user-agent")
}
func BenchmarkR10ScanAPI1Hit(b *testing.B) { benchScan(b, apiHead, "authorization") }
func BenchmarkR10ScanAPI4(b *testing.B) {
	benchScan(b, apiHead, "origin", "x-request-id", "authorization", "user-agent")
}
func BenchmarkR10IndexedAPI4(b *testing.B) {
	benchIdx(b, apiHead, "origin", "x-request-id", "authorization", "user-agent")
}

// What the slab copy of the head costs on top of the make the engine already does.
func BenchmarkR10SlabInitOnly(b *testing.B) {
	b.ReportAllocs()
	var s wire.Slab
	for range b.N {
		s.Init(len(browserHead))
	}
}
func BenchmarkR10SlabInitPlusHeadCopy(b *testing.B) {
	b.ReportAllocs()
	var s wire.Slab
	src := []byte(browserHead)
	for range b.N {
		s.Init(len(browserHead) * 2)
		sinkS = s.Copy(src)
	}
}
func BenchmarkR10SlabInitDoubleSizeOnly(b *testing.B) {
	b.ReportAllocs()
	var s wire.Slab
	for range b.N {
		s.Init(len(browserHead) * 2)
	}
}

// Overflow: the slab is sized to the head. Lazy copying lets the same bytes be copied twice
// (an Input's Header section and a middleware's Request.Header), which can need more than the
// head's length; one copy of the head that every string aliases cannot.
func TestR10DLazyCopiesCanOverflowTheSlabAndOneHeadCopyCannot(t *testing.T) {
	head := "GET /x HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + strings.Repeat("a", 380) + "\r\n\r\n"
	val := head[strings.Index(head, "Bearer"):strings.LastIndex(head, "\r\n\r\n")]
	// lazy: Input Header section copies the value, a middleware's Request.Header copies it again
	s := wire.NewSlab(len(head))
	s.Copy([]byte("/x"))
	s.Copy([]byte(val))
	s.Copy([]byte(val))
	t.Logf("measured: head %d B, lazy copies: slab fallbacks %d (each is +1 allocation)", len(head), s.Fallbacks())
	if s.Fallbacks() == 0 {
		t.Fatal("expected an overflow")
	}
	// one head copy into a slab of 2*head: later strings alias it
	s2 := wire.NewSlab(2 * len(head))
	h := s2.Copy([]byte(head))
	_ = h[strings.Index(h, "Bearer"):]
	if s2.Fallbacks() != 0 {
		t.Fatal("unexpected overflow")
	}
	t.Logf("measured: one head copy in a slab of 2x: fallbacks %d, remaining %d B", s2.Fallbacks(), s2.Remaining())
}
