// Package defaultsproto prototypes "defaults are a function on the route":
// Registration runs the function on a zero Input and on a poisoned one, and
// a field is defaulted when both runs leave the same value in it.
package defaultsproto

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Opt is a stand-in for nilogo.Opt.
type Opt[T any] struct {
	v  T
	ok bool
}

func (o Opt[T]) Or(d T) T {
	if o.ok {
		return o.v
	}
	return d
}
func (o Opt[T]) Get() (T, bool) { return o.v, o.ok }
func (Opt[T]) isOpt()           {}

type optish interface{ isOpt() }

type fieldDefault struct {
	path  []int
	name  string
	value reflect.Value
}

// Route holds what Registration learned.
type Route[In any] struct {
	defaults []fieldDefault
	shallow  *In // the "start from a copy" variant, kept to show its bug
}

var timeType = reflect.TypeFor[time.Time]()

func poison(v reflect.Value) {
	switch {
	case v.Type() == timeType:
		v.Set(reflect.ValueOf(time.Unix(1, 0).UTC()))
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		if _, ok := v.Addr().Interface().(optish); ok {
			return // Opt left alone; assigning one is refused separately
		}
		for i := 0; i < v.NumField(); i++ {
			poison(v.Field(i))
		}
	case reflect.Int, reflect.Int64:
		v.SetInt(-7_777_777)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.String:
		v.SetString("\x00poison")
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
	}
}

// Register runs fn and classifies every leaf field, or refuses.
func Register[In any](fn func(*In)) (*Route[In], error) {
	var z1, z2, p In
	fn(&z1)
	fn(&z2)
	poison(reflect.ValueOf(&p).Elem())
	pBefore := p
	fn(&p)
	r := &Route[In]{}
	var errs []string
	var walk func(a, b, pv, pb reflect.Value, path []int, name string)
	walk = func(a, b, pv, pb reflect.Value, path []int, name string) {
		if a.Kind() == reflect.Struct && a.Type() != timeType {
			if _, ok := a.Addr().Interface().(optish); ok {
				if !a.IsZero() {
					errs = append(errs, name+": an Opt field cannot have a default; make it a plain field")
				}
				return
			}
			for i := 0; i < a.NumField(); i++ {
				f := a.Type().Field(i)
				walk(a.Field(i), b.Field(i), pv.Field(i), pb.Field(i), append(append([]int{}, path...), i), strings.TrimPrefix(name+"."+f.Name, "."))
			}
			return
		}
		eq := func(x, y reflect.Value) bool { return reflect.DeepEqual(x.Interface(), y.Interface()) }
		switch {
		case !eq(a, b):
			errs = append(errs, name+": the default changes between calls; compute it in the handler with Opt.Or")
		case eq(a, pv):
			r.defaults = append(r.defaults, fieldDefault{path, name, a})
		case !a.IsZero() && eq(pv, pb):
			errs = append(errs, name+": the function sets this field only when it is empty, so it reads the Input; assign defaults unconditionally")
		case !eq(pv, pb):
			errs = append(errs, name+": the function assigns this field from another one; assign constants only")
		}
	}
	walk(reflect.ValueOf(&z1).Elem(), reflect.ValueOf(&z2).Elem(), reflect.ValueOf(&p).Elem(), reflect.ValueOf(&pBefore).Elem(), nil, "")
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	r.shallow = &z1
	return r, nil
}

func (r *Route[In]) Defaulted() map[string]any {
	m := map[string]any{}
	for _, d := range r.defaults {
		m[d.name] = d.value.Interface()
	}
	return m
}

func (r *Route[In]) isDefaulted(path []int) bool {
	for _, d := range r.defaults {
		if reflect.DeepEqual(d.path, path) {
			return true
		}
	}
	return false
}

// Decode fills In from a flat key -> value map (keys are lower-cased leaf field names).
// shallowStart reproduces the naive "decode into a copy of the defaulted struct".
func (r *Route[In]) Decode(q map[string]string, shallowStart bool) (In, error) {
	var in In
	if shallowStart {
		in = *r.shallow
	}
	var missing []string
	var fill func(v reflect.Value, path []int, name string) error
	fill = func(v reflect.Value, path []int, name string) error {
		if v.Kind() == reflect.Struct && v.Type() != timeType {
			if o, ok := v.Addr().Interface().(optish); ok {
				_ = o
				raw, sent := q[strings.ToLower(name)]
				if !sent {
					return nil
				}
				inner := reflect.New(v.Field(0).Type()).Elem()
				if err := set(inner, raw); err != nil {
					return err
				}
				v.Field(0).Set(inner)
				v.Field(1).SetBool(true)
				return nil
			}
			for i := 0; i < v.NumField(); i++ {
				if err := fill(v.Field(i), append(append([]int{}, path...), i), v.Type().Field(i).Name); err != nil {
					return err
				}
			}
			return nil
		}
		raw, sent := q[strings.ToLower(name)]
		if !sent {
			if !r.isDefaulted(path) {
				missing = append(missing, name)
			} else if !shallowStart {
				for _, d := range r.defaults {
					if reflect.DeepEqual(d.path, path) {
						v.Set(clone(d.value))
					}
				}
			}
			return nil
		}
		return set(v, raw)
	}
	if err := fill(reflect.ValueOf(&in).Elem(), nil, ""); err != nil {
		return in, err
	}
	if len(missing) > 0 {
		return in, fmt.Errorf("required: %s", strings.Join(missing, ", "))
	}
	return in, nil
}

func clone(v reflect.Value) reflect.Value {
	if v.Kind() == reflect.Slice && !v.IsNil() {
		c := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		reflect.Copy(c, v)
		return c
	}
	return v
}

func set(v reflect.Value, raw string) error {
	if v.Type() == timeType {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return err
		}
		v.Set(reflect.ValueOf(t))
		return nil
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.String:
		v.SetString(raw)
	case reflect.Slice:
		parts := strings.Split(raw, ",")
		s := reflect.MakeSlice(v.Type(), len(parts), len(parts))
		for i, p := range parts {
			s.Index(i).SetString(p)
		}
		v.Set(s)
	}
	return nil
}
