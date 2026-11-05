package zstd_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	kzstd "github.com/klauspost/compress/zstd"
	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/zstd"
)

type item struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age"`
	Page  int    `json:"page"`
}

// list is the 4 KiB typed JSON answer of the core's budget test.
var list = func() []item {
	l := make([]item, 40)
	for i := range l {
		l[i] = item{ID: int64(i), Name: "Ada Lovelace", Email: "ada@example.com", Age: 36, Page: i}
	}
	return l
}()

func serve(t testing.TB, encs ...bites.Encoder) *bitetest.Server {
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		bites.WithCompression(bites.Compression{Encoders: encs}))
	app.Get("/list/{n}", func(_ context.Context, in struct{ Path struct{ N int } }) ([]item, error) {
		return list[:in.Path.N], nil
	})
	return bitetest.Start(t, app)
}

// get sends one request on c and returns the answer's headers, lowercased, and its body.
func get(t testing.TB, br *bufio.Reader, c net.Conn, accept string) (map[string]string, []byte) {
	t.Helper()
	io.WriteString(c, "GET /list/40 HTTP/1.1\r\nHost: x\r\nAccept-Encoding: "+accept+"\r\n\r\n")
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	h := map[string]string{}
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if l == "\r\n" {
			break
		}
		k, v, _ := strings.Cut(strings.TrimRight(l, "\r\n"), ": ")
		h[strings.ToLower(k)] = v
	}
	n, _ := strconv.Atoi(h["content-length"])
	b := make([]byte, n)
	if _, err := io.ReadFull(br, b); err != nil {
		t.Fatal(err)
	}
	return h, b
}

func TestAClientThatAcceptsZstdGetsAZstdAnswerThatDecodesToTheJSON(t *testing.T) {
	srv := serve(t, zstd.Encoder())
	c := srv.Dial("203.0.113.9:4000")
	h, b := get(t, bufio.NewReader(c), c, "gzip, deflate, br, zstd")
	if h["content-encoding"] != "zstd" || h["vary"] != "Accept-Encoding" {
		t.Fatalf("headers %v", h)
	}
	d, _ := kzstd.NewReader(nil)
	plain, err := d.DecodeAll(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []item
	if err := json.Unmarshal(plain, &got); err != nil || len(got) != 40 || got[39] != list[39] {
		t.Fatalf("%v: %d items", err, len(got))
	}
	t.Logf("4 KiB of JSON: %d bytes plain, %d as zstd", len(plain), len(b))
}

func TestZstdAndGzipAreChosenByWeightAndATieGoesToTheOrderTheAppGave(t *testing.T) {
	srv := serve(t, zstd.Encoder(), bites.GzipEncoder())
	c := srv.Dial("203.0.113.9:4000")
	br := bufio.NewReader(c)
	for accept, want := range map[string]string{
		"gzip, zstd":        "zstd",
		"gzip":              "gzip",
		"zstd;q=0.5, gzip":  "gzip",
		"zstd;q=0, *":       "gzip",
		"identity":          "",
		"*;q=0.1, gzip;q=0": "zstd",
	} {
		if h, _ := get(t, br, c, accept); h["content-encoding"] != want {
			t.Errorf("Accept-Encoding %q: %q, want %q", accept, h["content-encoding"], want)
		}
	}
}

func TestOneEncoderCompressesFromManyGoroutinesAtOnce(t *testing.T) {
	e := zstd.Encoder()
	d, _ := kzstd.NewReader(nil)
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src := bytes.Repeat([]byte("goroutine "+strconv.Itoa(g)+" "), 300)
			for range 50 {
				out, err := d.DecodeAll(e.Encode(nil, src), nil)
				if err != nil || !bytes.Equal(out, src) {
					t.Errorf("goroutine %d: %v", g, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if got := e.Encode([]byte("keep"), []byte("x")); !bytes.HasPrefix(got, []byte("keep")) {
		t.Fatal("Encode does not append to dst")
	}
}

func TestATypedJSONAnswerOf4KiBCompressedWithZstdAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	srv := serve(t, zstd.Encoder())
	c := srv.Dial("203.0.113.9:4000")
	req := []byte("GET /list/40 HTTP/1.1\r\nHost: x\r\nAccept-Encoding: zstd\r\n\r\n")
	buf := make([]byte, 16<<10)
	want := 0
	do := func() {
		c.Write(req)
		if want == 0 {
			want, _ = c.Read(buf)
			return
		}
		io.ReadFull(c, buf[:want])
	}
	do()
	if !bytes.Contains(buf[:want], []byte("Content-Encoding: zstd")) {
		t.Fatalf("not compressed:\n%s", buf[:want])
	}
	for range 100 {
		do()
	}
	got := testing.AllocsPerRun(2000, do)
	t.Logf("a 4 KiB typed JSON answer as zstd: %v allocations per request", got)
	if got != 2 {
		t.Errorf("allocs/request = %v, want exactly 2, as with gzip (ADR 0003, ADR 0061)", got)
	}
}
