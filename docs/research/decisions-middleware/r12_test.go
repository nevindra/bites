package probe

import (
	"context"
	"reflect"
	"testing"
	"unsafe"

	nilogo "github.com/nevindra/bites"
)

// R12 baseline on the real framework: a concrete Output, an Output of any holding a wrapper,
// and an Output of any holding a plain struct, same Input, same response bytes.
func TestR12InterfaceReturnAllocationsOnTheRealFramework(t *testing.T) {
	concrete := func(ctx context.Context, in In) (nilogo.OK[Out], error) {
		o, _ := handler(ctx, in)
		return nilogo.OK[Out]{Body: o}, nil
	}
	dynWrapper := func(ctx context.Context, in In) (any, error) {
		o, _ := handler(ctx, in)
		return nilogo.OK[Out]{Body: o}, nil
	}
	dynPlain := func(ctx context.Context, in In) (any, error) {
		o, _ := handler(ctx, in)
		return o, nil
	}
	plain := func(ctx context.Context, in In) (Out, error) { return handler(ctx, in) }
	for _, c := range []struct {
		name string
		reg  func(*nilogo.App)
	}{
		{"plain Out", func(a *nilogo.App) { a.Post("/u/{id}", plain) }},
		{"concrete nilogo.OK[Out]", func(a *nilogo.App) { a.Post("/u/{id}", concrete) }},
		{"any holding nilogo.OK[Out]", func(a *nilogo.App) { a.Post("/u/{id}", dynWrapper) }},
		{"any holding Out", func(a *nilogo.App) { a.Post("/u/{id}", dynPlain) }},
	} {
		conn := rig(t, c.reg)
		n := keepAlive(t, conn, post("/u/7?page=2", body), "HTTP/1.1 200")
		t.Logf("allocs per request, %-28s %.2f", c.name, n)
	}
}

// The techniques, in isolation: how a plan could take the value out of an interface.
type shape interface{ isShape() }
type created struct {
	Body   Out
	Loc    string
	Header map[string][]string
}

func (created) isShape() {}

type okk struct {
	Body   Out
	Header map[string][]string
}

func (okk) isShape() {}

type eface struct{ typ, data unsafe.Pointer }

var sink int

func encodeOut(p unsafe.Pointer) int { o := (*Out)(p); return len(o.Name) + len(o.Email) + int(o.ID) }

//go:noinline
func mk(i int) shape {
	if i&1 == 0 {
		return created{Body: Out{ID: int64(i), Name: "Ada"}, Loc: "/u/1"}
	}
	return okk{Body: Out{ID: int64(i), Name: "Ada"}}
}

// Today: reflect.ValueOf, reflect.New, Set (decode.go writeDynamic).
func BenchmarkR12ReflectNew(b *testing.B) {
	b.ReportAllocs()
	for i := range b.N {
		v := any(mk(i))
		rv := reflect.ValueOf(v)
		c := reflect.New(rv.Type())
		c.Elem().Set(rv)
		sink += encodeOut(c.UnsafePointer())
	}
}

// Proposed: the data word of the interface already points at the boxed copy the handler's
// return made (non-pointer-shaped types), so the value is read in place. The plan is found by a
// scan of the declared Returns types, compared as reflect.Type.
var declared = []reflect.Type{reflect.TypeFor[created](), reflect.TypeFor[okk]()}

func BenchmarkR12DataWordAndScan(b *testing.B) {
	b.ReportAllocs()
	for i := range b.N {
		v := mk(i)
		t := reflect.TypeOf(v)
		idx := -1
		for j, d := range declared {
			if d == t {
				idx = j
				break
			}
		}
		if idx < 0 {
			b.Fatal("undeclared")
		}
		p := (*eface)(unsafe.Pointer(&v)).data
		sink += encodeOut(p)
	}
}

// The sync.Map lookup of today's planFor, for the time of finding a plan.
func BenchmarkR12SyncMapLookup(b *testing.B) {
	var m syncMap
	m.Store(declared[0], 1)
	m.Store(declared[1], 2)
	b.ReportAllocs()
	for i := range b.N {
		v := mk(i)
		x, _ := m.Load(reflect.TypeOf(v))
		sink += x.(int)
	}
}
