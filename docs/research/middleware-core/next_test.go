package middlewarecore

import (
	"context"
	"sync"
	"testing"
)

// Why Next is a small struct with a Call method and not a func (ADR 0022): a func value made for
// each layer of each request is an allocation, because the middleware is an unknown function and
// whatever it is given escapes. This file models the two shapes with the same three layers and
// the same pooled state, and counts allocations.

type resp struct{ status int }

type run struct{ ms []mwStruct }

type nextStruct struct {
	r *run
	i int
}

func (n nextStruct) Call(ctx context.Context) resp {
	if n.i == len(n.r.ms) {
		return resp{200}
	}
	return n.r.ms[n.i](ctx, nextStruct{n.r, n.i + 1})
}

type mwStruct func(ctx context.Context, next nextStruct) resp

type nextFunc func(ctx context.Context) resp
type mwFunc func(ctx context.Context, next nextFunc) resp

func chainFunc(ms []mwFunc, i int) nextFunc {
	if i == len(ms) {
		return func(context.Context) resp { return resp{200} }
	}
	return func(ctx context.Context) resp { return ms[i](ctx, chainFunc(ms, i+1)) }
}

var pool = sync.Pool{New: func() any { return new(run) }}

var sink resp

func passS(ctx context.Context, next nextStruct) resp { return next.Call(ctx) }
func passF(ctx context.Context, next nextFunc) resp   { return next(ctx) }

func bench(b *testing.B, n int, f func(ctx context.Context, ms []mwStruct, mf []mwFunc)) {
	ms := make([]mwStruct, n)
	mf := make([]mwFunc, n)
	for i := range ms {
		ms[i], mf[i] = passS, passF
	}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		f(ctx, ms, mf)
	}
}

func BenchmarkNext(b *testing.B) {
	for _, n := range []int{1, 3} {
		name := map[int]string{1: "1_layer", 3: "3_layers"}[n]
		b.Run("struct_"+name, func(b *testing.B) {
			bench(b, n, func(ctx context.Context, ms []mwStruct, _ []mwFunc) {
				r := pool.Get().(*run)
				r.ms = ms
				sink = nextStruct{r, 0}.Call(ctx)
				r.ms = nil
				pool.Put(r)
			})
		})
		b.Run("func_"+name, func(b *testing.B) {
			bench(b, n, func(ctx context.Context, _ []mwStruct, mf []mwFunc) {
				sink = chainFunc(mf, 0)(ctx)
			})
		})
	}
}
