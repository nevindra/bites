package mpart

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/nevindra/bites/internal/h1"
)

// trickle gives the parser the body n bytes at a time, the way a slow client does.
type trickle struct {
	b []byte
	n int
}

func (t *trickle) Fill(dst []byte) (int, error) {
	if len(t.b) == 0 {
		return 0, io.EOF
	}
	k := min(len(dst), t.n, len(t.b))
	copy(dst, t.b[:k])
	t.b = t.b[k:]
	return k, nil
}

type got struct{ name, file, ctype, body string }

func parseAll(t *testing.T, body string, step, maxParts int) ([]got, error) {
	t.Helper()
	var p Parser
	if err := p.Reset([]byte("XyZ"), make([]byte, MinWindow), &trickle{[]byte(body), step}, maxParts); err != nil {
		t.Fatal(err)
	}
	var out []got
	for {
		h, err := p.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		g := got{name: string(h.Name), file: string(h.FileName), ctype: string(h.ContentType)}
		b, err := io.ReadAll(&p)
		g.body = string(b)
		out = append(out, g)
		if err != nil {
			return out, err
		}
	}
}

func part(disp, ctype, body string) string {
	s := "--XyZ\r\nContent-Disposition: " + disp + "\r\n"
	if ctype != "" {
		s += "Content-Type: " + ctype + "\r\n"
	}
	return s + "\r\n" + body + "\r\n"
}

func TestPartsAreReadWhateverSizeTheBytesArriveIn(t *testing.T) {
	body := "preamble that is ignored\r\n" +
		part(`form-data; name="a"`, "", "one") +
		part(`form-data; name="file"; filename="x.txt"`, "text/plain", "line\r\n--XyZ-not-a-delimiter\r\n--Xy\r\nend") +
		part(`form-data; name="empty"`, "", "") +
		"--XyZ--\r\nepilogue"
	for _, step := range []int{1, 2, 3, 7, 100, 1 << 20} {
		parts, err := parseAll(t, body, step, 10)
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		want := []got{
			{"a", "", "", "one"},
			{"file", "x.txt", "text/plain", "line\r\n--XyZ-not-a-delimiter\r\n--Xy\r\nend"},
			{"empty", "", "", ""},
		}
		if len(parts) != len(want) {
			t.Fatalf("step %d: %d parts, want %d: %+v", step, len(parts), len(want), parts)
		}
		for i := range want {
			if parts[i] != want[i] {
				t.Errorf("step %d part %d = %+v, want %+v", step, i, parts[i], want[i])
			}
		}
	}
}

func TestABodyThatStartsWithTheDelimiterNeedsNoPreamble(t *testing.T) {
	parts, err := parseAll(t, part(`form-data; name="a"`, "", "x")+"--XyZ--", 1, 10)
	if err != nil || len(parts) != 1 || parts[0].body != "x" {
		t.Fatalf("%+v %v", parts, err)
	}
}

func TestAQuotedNameIsUnescapedAndAFileNameIsKeptAsSent(t *testing.T) {
	body := part(`form-data; name="a\"b"; filename="../../etc/passwd"`, "", "x") + "--XyZ--"
	parts, err := parseAll(t, body, 5, 10)
	if err != nil || parts[0].name != `a"b` || parts[0].file != "../../etc/passwd" {
		t.Fatalf("%+v %v", parts, err)
	}
	body = part(`FORM-DATA;filename=plain.txt;name=tok`, "", "x") + "--XyZ--"
	parts, err = parseAll(t, body, 5, 10)
	if err != nil || parts[0].name != "tok" || parts[0].file != "plain.txt" {
		t.Fatalf("%+v %v", parts, err)
	}
}

func isErr(err error, want *h1.Error) bool { return errors.Is(err, want) }

func TestMoreThanTheAllowedPartsIsRefused(t *testing.T) {
	var b strings.Builder
	for range 5 {
		b.WriteString(part(`form-data; name="a"`, "", "x"))
	}
	b.WriteString("--XyZ--")
	if _, err := parseAll(t, b.String(), 100, 4); !isErr(err, ErrTooManyParts) {
		t.Fatalf("5 parts with a limit of 4: %v", err)
	}
	if parts, err := parseAll(t, b.String(), 100, 5); err != nil || len(parts) != 5 {
		t.Fatalf("5 parts with a limit of 5: %d %v", len(parts), err)
	}
}

func TestAPartHeaderOverTheLimitOrWithTooManyFieldsIsRefused(t *testing.T) {
	big := "--XyZ\r\nContent-Disposition: form-data; name=\"a\"\r\nX-Pad: " + strings.Repeat("a", MaxHeaderBytes) + "\r\n\r\nx\r\n--XyZ--"
	if _, err := parseAll(t, big, 100, 10); !isErr(err, ErrHeaderTooLarge) {
		t.Fatalf("a header of %d bytes: %v", MaxHeaderBytes, err)
	}
	endless := "--XyZ\r\nContent-Disposition: form-data; name=\"a\"\r\nX-Pad: " + strings.Repeat("a", 3*MaxHeaderBytes)
	if _, err := parseAll(t, endless, 100, 10); !isErr(err, ErrHeaderTooLarge) {
		t.Fatalf("a header that never ends: %v", err)
	}
	many := "--XyZ\r\nContent-Disposition: form-data; name=\"a\"\r\n" + strings.Repeat("X-F: 1\r\n", MaxHeaderFields) + "\r\nx\r\n--XyZ--"
	if _, err := parseAll(t, many, 100, 10); !isErr(err, ErrTooManyFields) {
		t.Fatalf("17 header fields: %v", err)
	}
}

func TestMalformedPartsAreRefusedAndNeverReadAsData(t *testing.T) {
	for name, body := range map[string]string{
		"no disposition":      "--XyZ\r\nContent-Type: text/plain\r\n\r\nx\r\n--XyZ--",
		"no name":             "--XyZ\r\nContent-Disposition: form-data\r\n\r\nx\r\n--XyZ--",
		"not form-data":       "--XyZ\r\nContent-Disposition: attachment; name=\"a\"\r\n\r\nx\r\n--XyZ--",
		"a folded header":     "--XyZ\r\nContent-Disposition: form-data;\r\n name=\"a\"\r\n\r\nx\r\n--XyZ--",
		"a field with no ':'": "--XyZ\r\nContent-Disposition form-data\r\n\r\nx\r\n--XyZ--",
		"two dispositions":    "--XyZ\r\nContent-Disposition: form-data; name=\"a\"\r\nContent-Disposition: form-data; name=\"b\"\r\n\r\nx\r\n--XyZ--",
		"an empty header":     "--XyZ\r\n\r\nx\r\n--XyZ--",
		"junk after boundary": "--XyZ  junk\r\n\r\n",
	} {
		if _, err := parseAll(t, body, 100, 10); err == nil || err == io.EOF {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
}

func TestABodyThatStopsInsideAPartOrBeforeTheFirstDelimiterIsTruncated(t *testing.T) {
	for name, body := range map[string]string{
		"in the body":    "--XyZ\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\nhalf a val",
		"in the header":  "--XyZ\r\nContent-Disposition: form-data; na",
		"after the part": part(`form-data; name="a"`, "", "x"),
		"no delimiter":   "just some text",
		"empty":          "",
	} {
		for _, step := range []int{1, 1 << 20} {
			if _, err := parseAll(t, body, step, 10); !isErr(err, ErrTruncated) {
				t.Errorf("%s (step %d): err = %v, want ErrTruncated", name, step, err)
			}
		}
	}
}

func TestASourceErrorIsReturnedAsItIs(t *testing.T) {
	boom := errors.New("boom")
	var p Parser
	p.Reset([]byte("XyZ"), make([]byte, MinWindow), &failing{[]byte("--XyZ\r\nContent-Dis"), boom}, 5)
	if _, err := p.Next(); err != boom {
		t.Fatalf("err = %v, want boom", err)
	}
}

type failing struct {
	b   []byte
	err error
}

func (f *failing) Fill(dst []byte) (int, error) {
	if len(f.b) == 0 {
		return 0, f.err
	}
	n := copy(dst, f.b)
	f.b = f.b[n:]
	return n, nil
}

func TestTheBoundaryOfAContentTypeIsReadAndOverTheLimitRefused(t *testing.T) {
	ok := map[string]string{
		`multipart/form-data; boundary=abc`:                        "abc",
		`Multipart/Form-Data;boundary="a b'c"`:                     "a b'c",
		`multipart/form-data; charset=x; BOUNDARY=zz`:              "zz",
		`multipart/form-data; boundary=` + strings.Repeat("a", 70): strings.Repeat("a", 70),
	}
	for in, want := range ok {
		b, err := ParseContentType([]byte(in))
		if err != nil || string(b) != want {
			t.Errorf("%q: %q %v", in, b, err)
		}
	}
	for in, want := range map[string]*h1.Error{
		`application/json`:               ErrNotMultipart,
		`multipart/mixed; boundary=a`:    ErrNotMultipart,
		`multipart/form-data`:            ErrBadBoundary,
		`multipart/form-data; boundary=`: ErrBadBoundary,
		`multipart/form-data; boundary="` + strings.Repeat("a", 71) + `"`: ErrBadBoundary,
		`multipart/form-data; boundary=a; boundary=b`:                     ErrBadBoundary,
		"multipart/form-data; boundary=\"a\r\nb\"":                        ErrBadBoundary,
		`multipart/form-data; boundary="ends with space "`:                ErrBadBoundary,
	} {
		if _, err := ParseContentType([]byte(in)); !isErr(err, want) {
			t.Errorf("%q: err = %v, want %v", in, err, want)
		}
	}
}

func TestDrainReadsTheEpilogueToTheEnd(t *testing.T) {
	var p Parser
	src := &trickle{[]byte(part(`form-data; name="a"`, "", "x") + "--XyZ--\r\n" + strings.Repeat("e", 100000)), 4096}
	p.Reset([]byte("XyZ"), make([]byte, MinWindow), src, 5)
	for {
		if _, err := p.Next(); err != nil {
			if err != io.EOF {
				t.Fatal(err)
			}
			break
		}
	}
	if !p.Ended() {
		t.Fatal("not ended")
	}
	if err := p.Drain(); err != nil || len(src.b) != 0 {
		t.Fatalf("drain: %v, %d bytes left", err, len(src.b))
	}
}

func FuzzParserNeverPanicsAndKeepsItsLimits(f *testing.F) {
	f.Add("--XyZ\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\nx\r\n--XyZ--", 3)
	f.Add("--XyZ\r\nContent-Disposition: form-data; name=\"a\"; filename=\"\\\"\r\n\r\n\r\n--XyZ", 1)
	f.Add("junk\r\n--XyZ\r\n\r\n", 2)
	f.Fuzz(func(t *testing.T, body string, step int) {
		if step < 1 {
			step = 1
		}
		var p Parser
		if err := p.Reset([]byte("XyZ"), make([]byte, MinWindow), &trickle{[]byte(body), step}, 4); err != nil {
			t.Fatal(err)
		}
		parts := 0
		for {
			h, err := p.Next()
			if err != nil {
				return
			}
			parts++
			if parts > 4 || len(h.Name) > MaxHeaderBytes {
				t.Fatalf("limits broken: %d parts, name of %d bytes", parts, len(h.Name))
			}
			if _, err := io.Copy(io.Discard, &p); err != nil {
				return
			}
		}
	})
}
