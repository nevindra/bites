package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"testing"
)

type In struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}
type Out struct{ Msg string }

type Reg struct{ call func(ctx context.Context, body []byte) error }

// E1: typed var, json.Unmarshal(body, &in) (any-typed parameter).
func E1[I, O any](fn func(context.Context, I) (O, error)) Reg {
	return Reg{func(ctx context.Context, body []byte) error {
		var in I
		if err := json.Unmarshal(body, &in); err != nil {
			return err
		}
		_, err := fn(ctx, in)
		return err
	}}
}

// E2: typed var, filled by reflect.ValueOf(&in).Elem() (no json call).
func E2[I, O any](fn func(context.Context, I) (O, error)) Reg {
	return Reg{func(ctx context.Context, body []byte) error {
		var in I
		v := reflect.ValueOf(&in).Elem()
		v.Field(1).SetInt(int64(len(body)))
		_, err := fn(ctx, in)
		return err
	}}
}

// E3: reflect.New(T), decoded via json, passed by value.
func E3[I, O any](fn func(context.Context, I) (O, error)) Reg {
	t := reflect.TypeFor[I]()
	return Reg{func(ctx context.Context, body []byte) error {
		p := reflect.New(t)
		if err := json.Unmarshal(body, p.Interface()); err != nil {
			return err
		}
		_, err := fn(ctx, *p.Interface().(*I))
		return err
	}}
}

// E4: reflect.New(T) filled by reflect only (no json).
func E4[I, O any](fn func(context.Context, I) (O, error)) Reg {
	t := reflect.TypeFor[I]()
	return Reg{func(ctx context.Context, body []byte) error {
		p := reflect.New(t)
		p.Elem().Field(1).SetInt(int64(len(body)))
		_, err := fn(ctx, *p.Interface().(*I))
		return err
	}}
}

func h(ctx context.Context, in In) (Out, error) { return Out{Msg: in.Name}, nil }

func main() {
	ctx := context.Background()
	bodies := make([][]byte, 1000)
	for i := range bodies {
		bodies[i] = []byte(fmt.Sprintf(`{"name":"bob%06d","n":3}`, i))
	}
	i := 0
	run := func(name string, r Reg) {
		fmt.Printf("%-60s %v allocs\n", name, testing.AllocsPerRun(1000, func() { r.call(ctx, bodies[i%1000]); i++ }))
	}
	run("E1 var in I; json.Unmarshal(body,&in)  [1 is the Name string]", E1(h))
	run("E2 var in I; reflect.ValueOf(&in).Elem().Field(1).SetInt", E2(h))
	run("E3 reflect.New(T); json.Unmarshal(body,p.Interface()) [1 is the string]", E3(h))
	run("E4 reflect.New(T); p.Elem().Field(1).SetInt", E4(h))
}
