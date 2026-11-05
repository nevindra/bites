package bites

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"text/tabwriter"

	"github.com/nevindra/bites/internal/jsoncodec"
)

// wireRoute is what a typed route keeps for WriteWireTable: the types and the plans Registration
// compiled, so the table is read from the same plans the decoder and the encoder run (ADR 0011).
type wireRoute struct {
	method, pattern, where string
	inT, outT              reflect.Type
	in                     *inputPlan
	out                    *outPlan
	unknown                bool // the route accepts body members the type does not have
}

// WriteWireTable writes, route by route in registration order, every name a client sees: the
// section, the Go field path, the wire name and the type of each Path, Query, Header and Cookie
// field, of the Body (nested objects and lists included, as address.city and items[].sku), and of
// the success responses. The names are the ones the app's naming policy, WireNames methods and
// json tags produced, read from the plans Registration built, so the table cannot disagree with
// the decoder, the encoder or the API document, and it works for any NamingPolicy. It is for a
// test, a `go run` main or a review aid, never a request, and it allocates freely (ADR 0011).
func (a *App) WriteWireTable(w io.Writer) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	opts := jsoncodec.Options{Names: a.naming, Formats: a.formats, MaxDepth: a.limits.MaxDepth}
	for i, r := range a.wires {
		if i > 0 {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
		fmt.Fprintf(w, "%s %s  (registered at %s)\n", r.method, r.pattern, r.where)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  SECTION\tGO FIELD\tWIRE NAME\tTYPE")
		if err := r.rows(tw, opts); err != nil {
			return err
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func (r *wireRoute) rows(tw *tabwriter.Writer, opts jsoncodec.Options) error {
	label := typeLabel(r.inT)
	for _, s := range []*textSection{r.in.path, r.in.query, r.in.header, r.in.cookie} {
		wireSection(tw, s)
	}
	if b := r.in.body; b != nil {
		switch b.kind {
		case bodyBytes:
			fmt.Fprintf(tw, "  body\t%s.Body\t(raw bytes)\t[]byte\n", label)
		case bodyUpload:
			fmt.Fprintf(tw, "  body\t%s.Body\t(multipart parts)\tUpload\n", label)
		case bodyForm, bodyMultipart:
			wireSection(tw, b.form)
		default:
			bt, _ := r.inT.FieldByName("Body")
			if b.opt {
				fmt.Fprintf(tw, "  body\t%s.Body\t(may be absent)\t%s\n", label, typeText(bt.Type))
			}
			t := bodyValueType(bt.Type)
			if isOwn(t, "Raw[") {
				t = t.Field(0).Type
			}
			o := opts
			o.AllowUnknown = r.unknown
			if err := walkWire(tw, "body", label+".Body", "", t, o, jsoncodec.Decoding, nil); err != nil {
				return err
			}
		}
	}
	return r.responses(tw, opts)
}

// wireSection lists the fields of a text section; an Opt is marked optional and a list is its
// slice type.
func wireSection(tw *tabwriter.Writer, s *textSection) {
	if s == nil {
		return
	}
	for _, f := range s.fields {
		typ := typeText(f.typ)
		if !f.required && s.kind != secPath {
			typ += " (optional)"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", secNames[s.kind], f.goPath, f.wire, typ)
	}
}

// responses lists the body of each success answer, with the same choice of type answersOf makes
// for the API document.
func (r *wireRoute) responses(tw *tabwriter.Writer, opts jsoncodec.Options) error {
	one := func(t reflect.Type, p *outPlan) error {
		var bt reflect.Type
		status := 200
		switch p.wk {
		case wrapNone:
			bt = t
		case wrapOK, wrapCreated, wrapAccepted:
			f, _ := t.FieldByName("Body")
			bt, status = f.Type, wrapStatus[p.wk]
		case wrapEvents:
			bt = p.events.eventType()
		default:
			return nil
		}
		label, _, _ := strings.Cut(typeLabel(t), "[")
		if p.wk != wrapNone && p.wk != wrapEvents {
			label += ".Body"
		}
		return walkWire(tw, fmt.Sprintf("response %d", status), label, "", bt, opts, jsoncodec.Encoding, nil)
	}
	switch {
	case r.out.dynamic:
		for _, v := range r.out.variants {
			if err := one(v.t, v.plan); err != nil {
				return err
			}
		}
	case r.out.alts != nil:
		for _, al := range r.out.alts {
			if err := one(al.t, al.plan); err != nil {
				return err
			}
		}
	default:
		return one(r.outT, r.out)
	}
	return nil
}

// walkWire prints the fields of t, the body or response type, through the codec's own Describe, and
// descends into nested structs, lists and maps. wirePath is the dotted wire path so far, and stack
// the struct types being walked, so a recursive type is printed once and then named.
func walkWire(tw *tabwriter.Writer, section, goPath, wirePath string, t reflect.Type, o jsoncodec.Options, dir jsoncodec.Direction, stack []reflect.Type) error {
	sh, err := jsoncodec.Describe(t, o, dir)
	if err != nil {
		return err
	}
	switch sh.Kind {
	case jsoncodec.KindOpt, jsoncodec.KindNull, jsoncodec.KindPtr:
		return walkWire(tw, section, goPath, wirePath, sh.Elem, o, dir, stack)
	case jsoncodec.KindSlice, jsoncodec.KindArray:
		if wirePath == "" {
			fmt.Fprintf(tw, "  %s\t%s\t(a list)\t%s\n", section, goPath, typeText(t))
		}
		return walkWire(tw, section, goPath, wirePath+"[]", sh.Elem, o, dir, stack)
	case jsoncodec.KindMap:
		if wirePath == "" {
			fmt.Fprintf(tw, "  %s\t%s\t(a map)\t%s\n", section, goPath, typeText(t))
		}
		return walkWire(tw, section, goPath, wirePath+"{}", sh.Elem, o, dir, stack)
	case jsoncodec.KindStruct:
	default:
		if wirePath == "" {
			fmt.Fprintf(tw, "  %s\t%s\t(the value)\t%s\n", section, goPath, typeText(t))
		}
		return nil
	}
	for _, s := range stack {
		if s == t {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t(recursive: %s)\n", section, goPath, wirePath, typeLabel(t))
			return nil
		}
	}
	stack = append(stack, t)
	for _, f := range sh.Fields {
		// GoPath of a nested field starts with the struct's own type, which for an anonymous
		// struct is its whole source text, so only the field's name is taken from it.
		gp := goPath + "." + f.GoPath[strings.LastIndexByte(f.GoPath, '.')+1:]
		wp := f.Name
		if wirePath != "" {
			wp = wirePath + "." + f.Name
		}
		inner, err := jsoncodec.Describe(f.Type, o, dir)
		if err != nil {
			return err
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", section, gp, wp, typeText(f.Type))
		if inner.Kind == jsoncodec.KindStruct || inner.Elem != nil {
			if err := descend(tw, section, gp, wp, f.Type, o, dir, stack); err != nil {
				return err
			}
		}
	}
	return nil
}

// descend lists the fields below a field that holds a struct, directly or inside Opt, Null, a
// list or a map. A list or map of plain values has nothing below it.
func descend(tw *tabwriter.Writer, section, goPath, wirePath string, t reflect.Type, o jsoncodec.Options, dir jsoncodec.Direction, stack []reflect.Type) error {
	sh, err := jsoncodec.Describe(t, o, dir)
	if err != nil {
		return err
	}
	switch sh.Kind {
	case jsoncodec.KindOpt, jsoncodec.KindNull, jsoncodec.KindPtr:
		return descend(tw, section, goPath, wirePath, sh.Elem, o, dir, stack)
	case jsoncodec.KindSlice, jsoncodec.KindArray:
		return descend(tw, section, goPath+"[]", wirePath+"[]", sh.Elem, o, dir, stack)
	case jsoncodec.KindMap:
		return descend(tw, section, goPath+"{}", wirePath+"{}", sh.Elem, o, dir, stack)
	case jsoncodec.KindStruct:
		return walkWire(tw, section, goPath, wirePath, t, o, dir, stack)
	}
	return nil
}

// typeText is a type as a reader of the table knows it: the public names of Opt and Null.
func typeText(t reflect.Type) string {
	return strings.NewReplacer("wire.Opt[", "bites.Opt[", "wire.Null[", "bites.Null[").Replace(t.String())
}
