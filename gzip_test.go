package bites_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// Response compression (ADR 0031): gzip, off by default, decided by the engine from the finished
// response and the request's Accept-Encoding.

type SizeIn struct{ Path struct{ N int } }

type Row struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// rows is a JSON list of about n bytes.
func rows(n int) []Row {
	out := make([]Row, 0, n/60+1)
	for i := 0; len(out)*60 < n; i++ {
		out = append(out, Row{ID: i, Name: "Ada Lovelace " + strconv.Itoa(i), Email: "ada" + strconv.Itoa(i) + "@example.com"})
	}
	return out
}

func startGzip(t *testing.T, g *bites.Gzip, register func(a *bites.App)) *bitetest.Server {
	t.Helper()
	var opts []bites.Option
	if g != nil {
		opts = append(opts, bites.WithGzip(*g))
	}
	app := bites.New(append(opts, bites.WithLogger(quietLog()))...)
	register(app)
	return bitetest.Start(t, app)
}

func sized(a *bites.App) {
	a.Get("/rows/{n}", func(_ context.Context, in SizeIn) ([]Row, error) { return rows(in.Path.N), nil })
}

func ae(v string) bitetest.Request {
	return bitetest.Request{Method: "GET", Target: "/rows/2048", Header: map[string][]string{"Accept-Encoding": {v}}}
}

func ungzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not gzip: %v (%d bytes)", err, len(b))
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func varyHas(r bitetest.Response, want string) bool {
	for _, v := range r.Header["vary"] {
		for _, p := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(p), want) {
				return true
			}
		}
	}
	return false
}

func TestAJSONAnswerOf2KiBToAClientThatAcceptsGzipIsCompressedAndCarriesVary(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{}, sized)
	plain := srv.Do(bitetest.Request{Method: "GET", Target: "/rows/2048"})
	if plain.Get("content-encoding") != "" || len(plain.Body) < 2048 {
		t.Fatalf("without Accept-Encoding: %v, %d bytes", plain.Header, len(plain.Body))
	}
	r := srv.Do(ae("gzip"))
	if r.Status != 200 || r.Get("content-encoding") != "gzip" || !varyHas(r, "Accept-Encoding") {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if r.Get("content-length") != strconv.Itoa(len(r.Body)) || len(r.Body) >= len(plain.Body) {
		t.Fatalf("content-length %s for %d compressed bytes (plain %d)", r.Get("content-length"), len(r.Body), len(plain.Body))
	}
	if !bytes.Equal(ungzip(t, r.Body), plain.Body) {
		t.Fatal("the decompressed body is not the plain body")
	}
	if r.Get("content-type") != plain.Get("content-type") {
		t.Fatalf("content type changed: %q", r.Get("content-type"))
	}
}

func TestAClientThatDoesNotAskForGzipGetsThePlainBodyAndStillVary(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{}, sized)
	for _, v := range []string{"", "deflate, br", "identity", "gzip;q=0", "gzip; q=0.0", "gzip;q=0.000", "*;q=0", "*, gzip;q=0", "gzipx", "gzip;q=abc"} {
		h := map[string][]string{}
		if v != "" {
			h["Accept-Encoding"] = []string{v}
		}
		r := srv.Do(bitetest.Request{Method: "GET", Target: "/rows/2048", Header: h})
		if r.Get("content-encoding") != "" || !varyHas(r, "Accept-Encoding") || len(r.Body) < 2048 {
			t.Errorf("Accept-Encoding %q: %v, %d bytes", v, r.Header, len(r.Body))
		}
	}
}

func TestAcceptEncodingIsReadWithItsWeightsAndWildcard(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{}, sized)
	for _, v := range []string{"gzip", "GZIP", "x-gzip", "gzip, deflate, br", "br;q=1.0, gzip;q=0.5", "deflate, gzip ; q=0.001", "*", "*;q=0.5", "br, *", "identity;q=0, *"} {
		r := srv.Do(ae(v))
		if r.Get("content-encoding") != "gzip" {
			t.Errorf("Accept-Encoding %q: not compressed (%v)", v, r.Header)
		}
	}
}

func TestSeveralAcceptEncodingLinesAreReadAsOneList(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{}, sized)
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	c.Write([]byte("GET /rows/2048 HTTP/1.1\r\nHost: x\r\nAccept-Encoding: br\r\nAccept-Encoding: gzip\r\n\r\n"))
	if r := srv.Read(c, false); r.Get("content-encoding") != "gzip" {
		t.Fatalf("%v", r.Header)
	}
}

func TestAnAnswerBelowMinSizeIsNotCompressedButCarriesVary(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{}, sized)
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/rows/300", Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
	if r.Get("content-encoding") != "" || !varyHas(r, "Accept-Encoding") || len(r.Body) >= 1024 {
		t.Fatalf("%v, %d bytes", r.Header, len(r.Body))
	}
}

func TestAMinSizeAboveTheDefaultMovesTheThreshold(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{MinSize: 8192}, sized)
	small := srv.Do(bitetest.Request{Method: "GET", Target: "/rows/4096", Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
	big := srv.Do(bitetest.Request{Method: "GET", Target: "/rows/12000", Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
	if small.Get("content-encoding") != "" || big.Get("content-encoding") != "gzip" {
		t.Fatalf("4 KiB: %v; 12 KB: %v", small.Header, big.Header)
	}
}

func TestAnAppWithoutWithGzipNeverCompressesAndAddsNoVary(t *testing.T) {
	srv := startGzip(t, nil, sized)
	r := srv.Do(ae("gzip"))
	if r.Get("content-encoding") != "" || len(r.Header["vary"]) != 0 || len(r.Body) < 2048 {
		t.Fatalf("%v, %d bytes", r.Header, len(r.Body))
	}
}

func TestAnAnswerOf300KBRoundTripsThroughGzip(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{}, sized)
	plain := srv.Do(bitetest.Request{Method: "GET", Target: "/rows/300000"})
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/rows/300000", Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
	if r.Get("content-encoding") != "gzip" || !bytes.Equal(ungzip(t, r.Body), plain.Body) {
		t.Fatalf("%v", r.Header)
	}
}

func TestTheSameConnectionServesACompressedAndAPlainAnswerInTurn(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{}, sized)
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	for i, ae := range []string{"gzip", "", "gzip", ""} {
		h := ""
		if ae != "" {
			h = "Accept-Encoding: " + ae + "\r\n"
		}
		c.Write([]byte("GET /rows/2048 HTTP/1.1\r\nHost: x\r\n" + h + "\r\n"))
		r := srv.Read(c, false)
		if (r.Get("content-encoding") == "gzip") != (ae != "") {
			t.Fatalf("request %d: %v", i, r.Header)
		}
		if ae != "" {
			ungzip(t, r.Body)
		}
	}
}

func TestAnAnswerThatAlreadyHasContentEncodingIsLeftAlone(t *testing.T) {
	pre := bytes.Repeat([]byte("abc"), 2000)
	srv := startGzip(t, &bites.Gzip{}, func(a *bites.App) {
		a.Get("/pre", func(context.Context, Empty) (bites.Bytes, error) {
			return bites.Bytes{ContentType: "text/plain", Body: pre, Header: bites.Header{"Content-Encoding": {"br"}}}, nil
		})
	})
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/pre", Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
	if r.Get("content-encoding") != "br" || !bytes.Equal(r.Body, pre) || !varyHas(r, "Accept-Encoding") {
		t.Fatalf("%v, %d bytes", r.Header, len(r.Body))
	}
}

func TestAnAnswerWithCacheControlNoTransformIsLeftAlone(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{}, func(a *bites.App) {
		a.Get("/nt", func(context.Context, Empty) (bites.Text, error) {
			return bites.Text{Body: strings.Repeat("abc", 2000), Header: bites.Header{"Cache-Control": {"public, No-Transform"}}}, nil
		})
	})
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/nt", Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
	if r.Get("content-encoding") != "" || len(r.Body) != 6000 {
		t.Fatalf("%v, %d bytes", r.Header, len(r.Body))
	}
}

func TestAnAlreadyCompressedContentTypeIsNotCompressedAndSVGIs(t *testing.T) {
	body := bytes.Repeat([]byte{0}, 4096)
	srv := startGzip(t, &bites.Gzip{}, func(a *bites.App) {
		for path, ct := range map[string]string{"/png": "image/png", "/mp4": "video/mp4", "/zip": "application/zip", "/gz": "application/gzip", "/pdf": "application/pdf", "/woff": "font/woff2", "/svg": "image/svg+xml", "/txt": "text/plain; charset=utf-8", "/csv": "Text/CSV"} {
			a.Get(path, func(context.Context, Empty) (bites.Bytes, error) {
				return bites.Bytes{ContentType: ct, Body: body}, nil
			})
		}
	})
	for path, want := range map[string]bool{"/png": false, "/mp4": false, "/zip": false, "/gz": false, "/pdf": false, "/woff": false, "/svg": true, "/txt": true, "/csv": true} {
		r := srv.Do(bitetest.Request{Method: "GET", Target: path, Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
		if got := r.Get("content-encoding") == "gzip"; got != want || !varyHas(r, "Accept-Encoding") {
			t.Errorf("%s: compressed %v, want %v", path, got, want)
		}
	}
}

func TestAStrongETagIsWeakenedWhenTheBodyIsCompressedAndAWeakOneIsKept(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{}, func(a *bites.App) {
		for path, tag := range map[string]string{"/strong": `"abc"`, "/weak": `W/"abc"`} {
			a.Get(path, func(context.Context, Empty) (bites.Text, error) {
				return bites.Text{Body: strings.Repeat("abc", 2000), Header: bites.Header{"ETag": {tag}, "X-After": {"1"}}}, nil
			})
		}
	})
	for path, want := range map[string]string{"/strong": `W/"abc"`, "/weak": `W/"abc"`} {
		r := srv.Do(bitetest.Request{Method: "GET", Target: path, Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
		if r.Get("etag") != want || r.Get("content-encoding") != "gzip" || r.Get("x-after") != "1" {
			t.Errorf("%s: %v", path, r.Header)
		}
	}
	// Not compressed, the tag stays strong: the bytes are the ones it names.
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/strong"})
	if r.Get("etag") != `"abc"` {
		t.Errorf("uncompressed: %v", r.Header)
	}
}

func TestA304A204AndAHEADAreNotCompressedButCarryVary(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{}, func(a *bites.App) {
		sized(a)
		a.Get("/nm", func(context.Context, Empty) (bites.NotModified, error) {
			return bites.NotModified{Header: bites.Header{"ETag": {`"x"`}}}, nil
		})
		a.Get("/nc", func(context.Context, Empty) (bites.NoContent, error) { return bites.NoContent{}, nil })
	})
	for _, tc := range []struct{ method, path string }{{"GET", "/nm"}, {"GET", "/nc"}, {"HEAD", "/rows/2048"}} {
		r := srv.Do(bitetest.Request{Method: tc.method, Target: tc.path, Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
		if r.Get("content-encoding") != "" || !varyHas(r, "Accept-Encoding") {
			t.Errorf("%s %s: %v", tc.method, tc.path, r.Header)
		}
		if tc.path == "/nm" && r.Get("etag") != `"x"` {
			t.Errorf("a 304 must keep its strong tag: %v", r.Header)
		}
	}
}

func TestAStreamAnSSEAndAFileAreNeverCompressedAndCarryVary(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{}, func(a *bites.App) {
		a.Get("/stream", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Header: bites.Header{"Content-Type": {"text/plain"}}, Func: func(_ context.Context, w *bites.StreamWriter) error {
				_, err := w.Write(bytes.Repeat([]byte("abc"), 3000))
				return err
			}}, nil
		})
		a.Get("/sse", func(context.Context, Empty) (bites.SSE, error) {
			return bites.SSE{Func: func(_ context.Context, e bites.EventWriter) error {
				return e.Send(bites.Event{Data: strings.Repeat("abc", 1000)})
			}}, nil
		})
		fsys := fstest.MapFS{"f.txt": {Data: []byte(strings.Repeat("abc", 3000))}}
		a.Get("/file", func(context.Context, Empty) (bites.File, error) { return bites.File{FS: fsys, Name: "f.txt"}, nil })
	})
	for _, p := range []string{"/stream", "/sse", "/file"} {
		r := srv.Do(bitetest.Request{Method: "GET", Target: p, Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
		if r.Status != 200 || r.Get("content-encoding") != "" || !varyHas(r, "Accept-Encoding") || len(r.Body) < 3000 {
			t.Errorf("%s: %d %v (%d bytes)", p, r.Status, r.Header, len(r.Body))
		}
	}
}

func TestBodyThatDoesNotShrinkIsSentPlain(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	noise := make([]byte, 4096)
	for i := range noise {
		noise[i] = byte(rnd.Uint32())
	}
	srv := startGzip(t, &bites.Gzip{}, func(a *bites.App) {
		a.Get("/noise", func(context.Context, Empty) (bites.Bytes, error) {
			return bites.Bytes{ContentType: "application/x-noise", Body: noise}, nil
		})
	})
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/noise", Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
	if r.Get("content-encoding") != "" || !bytes.Equal(r.Body, noise) {
		t.Fatalf("%v, %d bytes", r.Header, len(r.Body))
	}
}

func TestAVaryTheHandlerSetIsKeptAndAcceptEncodingIsNotAddedTwice(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{}, func(a *bites.App) {
		a.Get("/v", func(context.Context, Empty) (bites.Text, error) {
			return bites.Text{Body: strings.Repeat("abc", 2000), Header: bites.Header{"Vary": {"Origin"}}}, nil
		})
		a.Get("/v2", func(context.Context, Empty) (bites.Text, error) {
			return bites.Text{Body: strings.Repeat("abc", 2000), Header: bites.Header{"Vary": {"accept-encoding, Origin"}}}, nil
		})
	})
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/v", Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
	if !varyHas(r, "Origin") || !varyHas(r, "Accept-Encoding") || r.Get("content-encoding") != "gzip" {
		t.Errorf("%v", r.Header)
	}
	r = srv.Do(bitetest.Request{Method: "GET", Target: "/v2", Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
	if len(r.Header["vary"]) != 1 {
		t.Errorf("Vary twice: %v", r.Header["vary"])
	}
}

func TestMiddlewareSeesTheUncompressedSizeAndCannotSeeTheCompressedBytes(t *testing.T) {
	var size int
	srv := startGzip(t, &bites.Gzip{}, func(a *bites.App) {
		a.Use(func(ctx context.Context, _ bites.Request, next bites.Next) (bites.Response, error) {
			res := next.Call(ctx)
			size = res.Size()
			return res, nil
		})
		sized(a)
	})
	plain := srv.Do(bitetest.Request{Method: "GET", Target: "/rows/4096"})
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/rows/4096", Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
	if size != len(plain.Body) || size <= len(r.Body) || r.Get("content-encoding") != "gzip" {
		t.Fatalf("Size() %d, plain %d, compressed %d", size, len(plain.Body), len(r.Body))
	}
}

func TestAShort400AnswerIsPlainWithVaryWhateverTheClientAccepts(t *testing.T) {
	srv := startGzip(t, &bites.Gzip{}, func(a *bites.App) {
		a.Post("/p", func(_ context.Context, in struct{ Body struct{ Name string } }) (Row, error) { return Row{}, nil })
	})
	// A 400 for malformed JSON is short: below MinSize, plain, with Vary.
	r := srv.Do(bitetest.Request{Method: "POST", Target: "/p", Body: []byte("{"), Header: map[string][]string{"Accept-Encoding": {"gzip"}, "Content-Type": {"application/json"}}})
	if r.Status != 400 || r.Get("content-encoding") != "" || !varyHas(r, "Accept-Encoding") {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

// The seam of ADR 0061: the app offers several encoders, the client's weights choose, and a tie
// goes to the app's order. reverse is a stand-in for a module: its "compression" is the body
// reversed, which a test can undo, and halved, so that it is smaller.
type reverse struct{}

func (reverse) Coding() string { return "rev" }
func (reverse) Encode(dst, src []byte) []byte {
	for i := len(src) - 1; i >= len(src)/2; i-- {
		dst = append(dst, src[i])
	}
	return dst
}

func startCompression(t *testing.T, c bites.Compression) *bitetest.Server {
	t.Helper()
	app := bites.New(bites.WithLogger(quietLog()), bites.WithCompression(c))
	sized(app)
	return bitetest.Start(t, app)
}

func TestWithCompressionTheClientsWeightsChooseAndATieGoesToTheAppsOrder(t *testing.T) {
	srv := startCompression(t, bites.Compression{Encoders: []bites.Encoder{reverse{}, bites.GzipEncoder()}})
	for accept, want := range map[string]string{
		"gzip, rev":         "rev",
		"rev, gzip":         "rev",
		"gzip, rev;q=0.9":   "gzip",
		"gzip":              "gzip",
		"*":                 "rev",
		"*, rev;q=0":        "gzip",
		"identity, deflate": "",
	} {
		r := srv.Do(ae(accept))
		if r.Get("content-encoding") != want || r.Get("vary") != "Accept-Encoding" {
			t.Errorf("Accept-Encoding %q: Content-Encoding %q, want %q (%v)", accept, r.Get("content-encoding"), want, r.Header)
		}
		if want == "gzip" {
			ungzip(t, r.Body)
		}
	}
}

func TestWithCompressionWithNoEncoderListedIsGzipAlone(t *testing.T) {
	srv := startCompression(t, bites.Compression{})
	if r := srv.Do(ae("br, zstd, gzip")); r.Get("content-encoding") != "gzip" {
		t.Fatalf("%v", r.Header)
	}
}

func TestTheStartUpLogNamesTheCodingsInTheAppsOrder(t *testing.T) {
	srv, lb := startLogged(t, sized, bites.WithCompression(bites.Compression{MinSize: 2048, Encoders: []bites.Encoder{reverse{}, bites.GzipEncoder()}}))
	srv.Do(ae("rev")) // the settings are logged before the first connection is accepted
	for _, want := range []string{`setting=compression value="rev, gzip" source=custom`, `setting="compression min size bytes" value=2048 source=custom`} {
		if !strings.Contains(lb.String(), want) {
			t.Errorf("no %q in\n%s", want, lb.String())
		}
	}
}
