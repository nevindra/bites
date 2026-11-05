package engine

import (
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// The compression seam (encode.go, ADR 0061).

// tagEncoder "compresses" by writing its coding and the body's length: enough to see which one
// the engine picked and that it was handed the whole body.
type tagEncoder string

func (t tagEncoder) Coding() string { return string(t) }
func (t tagEncoder) Encode(dst, src []byte) []byte {
	return append(dst, string(t)+":"+itoa(len(src))...)
}

func itoa(n int) string { return string(appendInt(nil, n)) }

func codersOf(names ...string) []coder {
	var cs []coder
	for _, n := range names {
		cs = append(cs, coder{enc: tagEncoder(n), coding: n})
	}
	return cs
}

func TestTheHeaviestAcceptableCodingWinsAndATieGoesToTheServersOrder(t *testing.T) {
	cs := codersOf("br", "zstd", "gzip")
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"gzip, deflate, br, zstd"}, "br"},          // a browser: a tie, the server's first
		{[]string{"zstd, gzip"}, "zstd"},                     // br not listed, no *: refused
		{[]string{"gzip;q=1, br;q=0.5, zstd;q=0.9"}, "gzip"}, // the weights decide
		{[]string{"br;q=0.5", "zstd;q=0.501"}, "zstd"},       // one list across lines, thousandths count
		{[]string{"*;q=0.5, gzip;q=0.4"}, "br"},              // * weighs the unlisted ones
		{[]string{"*, br;q=0"}, "zstd"},                      // br refused beside *
		{[]string{"x-gzip"}, "gzip"},                         // the old name
		{[]string{"br;q=0.7, br;q=0.9, zstd;q=0.8"}, "zstd"}, // the first weight of a coding listed twice
		{[]string{"BR;Q=1."}, "br"},                          // case, and a weight of "1."
		{[]string{"identity"}, ""},                           // nothing to pick
		{[]string{"*;q=0"}, ""},                              // everything refused
		{[]string{"br;q=2, zstd;q=0.0001, gzip;q=x"}, ""},    // weights that do not parse are refusals
		{nil, ""},
	} {
		got := ""
		if i := pick(lines(tc.in...), cs); i >= 0 {
			got = cs[i].coding
		}
		if got != tc.want {
			t.Errorf("Accept-Encoding %q: picked %q, want %q", strings.Join(tc.in, " | "), got, tc.want)
		}
	}
}

func TestAWeightIsReadInThousandths(t *testing.T) {
	for in, want := range map[string]int{
		"q=1": 1000, "q=1.": 1000, "q=1.000": 1000, "q=0.5": 500, "q=0.05": 50, "q=0.005": 5, "Q = 0.25 ": 250,
		"level=3": 1000, "level=3;q=0.2": 200, "q=0": 0, "q=1.001": 0, "q=0.0005": 0, "q=.5": 0, "q=": 0, "q=01": 0,
	} {
		if got := qValue([]byte(in)); got != want {
			t.Errorf("qValue(%q) = %d, want %d", in, got, want)
		}
	}
}

func compressRig(t *testing.T, encs []Encoder, logs io.Writer) *rig {
	t.Helper()
	body := strings.Repeat("compressible text, ", 200)
	return newRig(t, func(ex *Exchange) {
		r := ex.Respond(200)
		r.Header("Content-Type", "text/plain")
		r.Header("ETag", `"v1"`)
		*r.Body() = append(*r.Body(), body...)
	}, func(o *Options) {
		o.Compress = CompressConfig{Encoders: encs}
		if logs != nil {
			o.Logger = slog.New(slog.NewTextHandler(logs, nil))
		}
	})
}

func TestTheEncoderPickedNamesTheContentEncodingAndIsHandedTheWholeBody(t *testing.T) {
	r := compressRig(t, []Encoder{tagEncoder("br"), tagEncoder("zstd"), Gzip()}, nil)
	c := r.dial()
	c.send("GET / HTTP/1.1\r\nHost: x\r\nAccept-Encoding: gzip, zstd;q=0.9, br;q=0.5\r\n\r\n")
	rp := c.read()
	if rp.headers["content-encoding"] != "gzip" || rp.headers["vary"] != "Accept-Encoding" || rp.headers["etag"] != `W/"v1"` {
		t.Fatalf("headers %v", rp.headers)
	}
	zr, err := gzip.NewReader(strings.NewReader(rp.body))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); !bytes.Equal(b, []byte(strings.Repeat("compressible text, ", 200))) {
		t.Fatal("the gzip body does not round-trip")
	}
	c.send("GET / HTTP/1.1\r\nHost: x\r\nAccept-Encoding: zstd, br\r\n\r\n")
	if rp := c.read(); rp.headers["content-encoding"] != "br" || rp.body != "br:3800" {
		t.Fatalf("a tie: %v %q, want br, the server's first", rp.headers, rp.body)
	}
}

type panicEncoder struct{}

func (panicEncoder) Coding() string              { return "zstd" }
func (panicEncoder) Encode(dst, _ []byte) []byte { panic("a broken module") }

func TestAnEncoderThatPanicsSendsTheAnswerUncompressedAndIsLogged(t *testing.T) {
	var logs lockedBuf
	r := compressRig(t, []Encoder{panicEncoder{}}, &logs)
	c := r.dial()
	for range 2 {
		c.send("GET / HTTP/1.1\r\nHost: x\r\nAccept-Encoding: zstd\r\n\r\n")
		rp := c.read()
		if rp.status != 200 || rp.headers["content-encoding"] != "" || len(rp.body) != 3800 || rp.headers["etag"] != `"v1"` {
			t.Fatalf("%d %v, %d bytes", rp.status, rp.headers, len(rp.body))
		}
	}
	if !strings.Contains(logs.String(), "panic in an Encoder") {
		t.Fatalf("not logged:\n%s", logs.String())
	}
}

// growEncoder writes more than it was given, as an encoder does with an incompressible body.
type growEncoder struct{}

func (growEncoder) Coding() string                { return "br" }
func (growEncoder) Encode(dst, src []byte) []byte { return append(append(dst, src...), 0) }

func TestAnAnswerAnEncoderWouldNotShrinkIsSentAsItIs(t *testing.T) {
	r := compressRig(t, []Encoder{growEncoder{}}, nil)
	c := r.dial()
	c.send("GET / HTTP/1.1\r\nHost: x\r\nAccept-Encoding: br\r\n\r\n")
	if rp := c.read(); rp.headers["content-encoding"] != "" || len(rp.body) != 3800 || rp.headers["vary"] != "Accept-Encoding" {
		t.Fatalf("%v, %d bytes", rp.headers, len(rp.body))
	}
}
