package probe

import (
	"context"
	"strconv"
	"testing"
	"unsafe"
)

// A model of the middleware chain, not the framework: it reproduces the shape that decides
// allocations (where the handler's Output lives, what the Response holds, how Next is passed)
// so each design is measured on the same work. The encode is a small append, the same in all.

type hdr struct{ name, value string }

type pairs interface{ ~[4]hdr | ~[6]hdr | ~[8]hdr }

type Resp[A pairs] struct {
	Status int32
	N      uint8
	Slot   bool // deferred designs: the body is the handler's Output, still in the route's frame
	Size   int32
	H      A
	Boxed  any // boxed design
}

func (r *Resp[A]) Add(n, v string) { r.H[r.N] = hdr{n, v}; r.N++ }

type Req struct{ head string }

//go:noinline
func (Req) Header(string) string { return "x" }

func appendOut(b []byte, o *Out) []byte {
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, o.ID, 10)
	b = append(b, `,"name":"`...)
	b = append(b, o.Name...)
	b = append(b, `","email":"`...)
	b = append(b, o.Email...)
	b = append(b, `","age":`...)
	b = strconv.AppendInt(b, int64(o.Age), 10)
	return append(b, '}')
}

func noescape(p unsafe.Pointer) unsafe.Pointer {
	x := uintptr(p)
	return *(*unsafe.Pointer)(unsafe.Pointer(&x))
}

// ---- design A: deferred encode, Output kept in the route frame, Response holds a token.

type stateA[A pairs] struct {
	out  Out
	have bool
	buf  *[]byte
}
type nextA[A pairs] struct {
	mws []func(context.Context, Req, nextA[A]) (Resp[A], error)
	i   int
	st  unsafe.Pointer
}

//go:noinline
func (n nextA[A]) Call(ctx context.Context, req Req) (Resp[A], error) {
	if n.i < len(n.mws) {
		m := n.mws[n.i]
		n.i++
		return m(ctx, req, n)
	}
	st := (*stateA[A])(n.st)
	st.out = Out{ID: int64(sinkI), Name: names[sinkI&1], Email: "ada@example.com", Age: 36}
	st.have = true
	return Resp[A]{Status: 200, Slot: true}, nil
}

func runA[A pairs](mws []func(context.Context, Req, nextA[A]) (Resp[A], error), buf *[]byte) int {
	var st stateA[A]
	st.buf = buf
	r, _ := nextA[A]{mws: mws, st: noescape(unsafe.Pointer(&st))}.Call(context.Background(), Req{})
	if r.Slot && st.have {
		*buf = appendOut((*buf)[:0], &st.out)
	}
	return len(*buf) + int(r.N)
}

func mwsA[A pairs]() []func(context.Context, Req, nextA[A]) (Resp[A], error) {
	add := func(n, v string) func(context.Context, Req, nextA[A]) (Resp[A], error) {
		return func(ctx context.Context, req Req, next nextA[A]) (Resp[A], error) {
			r, err := next.Call(ctx, req)
			r.Add(n, v)
			return r, err
		}
	}
	look := func(ctx context.Context, req Req, next nextA[A]) (Resp[A], error) {
		r, err := next.Call(ctx, req)
		if r.Status >= 500 {
			return r, err
		}
		return r, err
	}
	return []func(context.Context, Req, nextA[A]) (Resp[A], error){look, add("Access-Control-Allow-Origin", "https://a"), add("X-Request-Id", "abc")}
}

// ---- design B: early encode in the terminal, Response holds the size.

type nextB[A pairs] struct {
	mws []func(context.Context, Req, nextB[A]) (Resp[A], error)
	i   int
	buf *[]byte
}

//go:noinline
func (n nextB[A]) Call(ctx context.Context, req Req) (Resp[A], error) {
	if n.i < len(n.mws) {
		m := n.mws[n.i]
		n.i++
		return m(ctx, req, n)
	}
	o := Out{ID: int64(sinkI), Name: names[sinkI&1], Email: "ada@example.com", Age: 36}
	*n.buf = appendOut((*n.buf)[:0], &o)
	return Resp[A]{Status: 200, Size: int32(len(*n.buf))}, nil
}

func runB[A pairs](mws []func(context.Context, Req, nextB[A]) (Resp[A], error), buf *[]byte) int {
	r, _ := nextB[A]{mws: mws, buf: buf}.Call(context.Background(), Req{})
	return int(r.Size) + int(r.N)
}

func mwsB[A pairs]() []func(context.Context, Req, nextB[A]) (Resp[A], error) {
	add := func(n, v string) func(context.Context, Req, nextB[A]) (Resp[A], error) {
		return func(ctx context.Context, req Req, next nextB[A]) (Resp[A], error) {
			r, err := next.Call(ctx, req)
			r.Add(n, v)
			return r, err
		}
	}
	look := func(ctx context.Context, req Req, next nextB[A]) (Resp[A], error) {
		r, err := next.Call(ctx, req)
		if r.Status >= 500 {
			return r, err
		}
		return r, err
	}
	return []func(context.Context, Req, nextB[A]) (Resp[A], error){look, add("Access-Control-Allow-Origin", "https://a"), add("X-Request-Id", "abc")}
}

// ---- design C: deferred encode by boxing the Output into the Response (no unsafe).

type nextC[A pairs] struct {
	mws []func(context.Context, Req, nextC[A]) (Resp[A], error)
	i   int
}

//go:noinline
func (n nextC[A]) Call(ctx context.Context, req Req) (Resp[A], error) {
	if n.i < len(n.mws) {
		m := n.mws[n.i]
		n.i++
		return m(ctx, req, n)
	}
	o := Out{ID: int64(sinkI), Name: names[sinkI&1], Email: "ada@example.com", Age: 36}
	return Resp[A]{Status: 200, Boxed: o}, nil
}

func runC[A pairs](mws []func(context.Context, Req, nextC[A]) (Resp[A], error), buf *[]byte) int {
	r, _ := nextC[A]{mws: mws}.Call(context.Background(), Req{})
	o := r.Boxed.(Out)
	*buf = appendOut((*buf)[:0], &o)
	return len(*buf) + int(r.N)
}

func mwsC[A pairs]() []func(context.Context, Req, nextC[A]) (Resp[A], error) {
	add := func(n, v string) func(context.Context, Req, nextC[A]) (Resp[A], error) {
		return func(ctx context.Context, req Req, next nextC[A]) (Resp[A], error) {
			r, err := next.Call(ctx, req)
			r.Add(n, v)
			return r, err
		}
	}
	return []func(context.Context, Req, nextC[A]) (Resp[A], error){add("a", "b"), add("Access-Control-Allow-Origin", "https://a"), add("X-Request-Id", "abc")}
}

var sinkI int

func BenchmarkR1DeferredFrameSlot4(b *testing.B) {
	m, buf := mwsA[[4]hdr](), make([]byte, 0, 256)
	b.ReportAllocs()
	for range b.N {
		sinkI += runA(m, &buf)
	}
}
func BenchmarkR1DeferredFrameSlot8(b *testing.B) {
	m, buf := mwsA[[8]hdr](), make([]byte, 0, 256)
	b.ReportAllocs()
	for range b.N {
		sinkI += runA(m, &buf)
	}
}
func BenchmarkR1EarlyEncode4(b *testing.B) {
	m, buf := mwsB[[4]hdr](), make([]byte, 0, 256)
	b.ReportAllocs()
	for range b.N {
		sinkI += runB(m, &buf)
	}
}
func BenchmarkR1EarlyEncode6(b *testing.B) {
	m, buf := mwsB[[6]hdr](), make([]byte, 0, 256)
	b.ReportAllocs()
	for range b.N {
		sinkI += runB(m, &buf)
	}
}
func BenchmarkR1EarlyEncode8(b *testing.B) {
	m, buf := mwsB[[8]hdr](), make([]byte, 0, 256)
	b.ReportAllocs()
	for range b.N {
		sinkI += runB(m, &buf)
	}
}
func BenchmarkR1DeferredBoxedAny8(b *testing.B) {
	m, buf := mwsC[[8]hdr](), make([]byte, 0, 256)
	b.ReportAllocs()
	for range b.N {
		sinkI += runC(m, &buf)
	}
}

// Header overflow: more headers than the inline pairs. A slice fallback is an allocation.
func TestR1MoreHeadersThanInlinePairsNeedsAFallback(t *testing.T) {
	// the first-party set on one JSON route: Access-Control-Allow-Origin, Vary, Access-Control-
	// Allow-Credentials, Access-Control-Expose-Headers, X-Request-Id, plus Cache-Control and
	// Location from a Created wrapper = 7, with Content-Type kept out of the pairs.
	n := 7
	t.Logf("claim (counted from ADR 0032 and ADR 0013, not measured on a built middleware): a CORS+request-id route that returns Created[T] with Cache-Control sets %d headers; inline 4 overflows, 8 holds", n)
}

var names = [2]string{"Ada Lovelace", "Grace Hopper"}

func TestR1SizesOfTheValuesThatCrossTheChain(t *testing.T) {
	t.Logf("measured: sizeof Resp with 4 inline pairs %d B, 6 pairs %d B, 8 pairs %d B", unsafe.Sizeof(Resp[[4]hdr]{}), unsafe.Sizeof(Resp[[6]hdr]{}), unsafe.Sizeof(Resp[[8]hdr]{}))
	type either struct {
		a, b  nilogoCreated
		which uint8
	}
	t.Logf("measured: sizeof an Either of two Created[Out]-shaped wrappers %d B (Created[Out] alone %d B)", unsafe.Sizeof(either{}), unsafe.Sizeof(nilogoCreated{}))
}

type nilogoCreated struct {
	Body     Out
	Location string
	Header   map[string][]string
	Cookies  []struct{ a, b, c, d, e, f, g string }
}
