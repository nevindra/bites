package bites_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// File (ADR 0043): a file sent with its length and Last-Modified, from an os.Root or an fs.FS
// that cannot leave its directory.

type FileIn struct{ Path struct{ Name string } }

func fileRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pub", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SECRET"), 0o644)
	os.WriteFile(filepath.Join(dir, "pub", "hello.txt"), []byte("hello, file"), 0o644)
	os.WriteFile(filepath.Join(dir, "pub", "sub", "data.json"), []byte(`{"a":1}`), 0o644)
	os.WriteFile(filepath.Join(dir, "pub", "noext"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "pub", "empty.txt"), nil, 0o644)
	big := make([]byte, 300_000)
	for i := range big {
		big[i] = byte(i % 251)
	}
	os.WriteFile(filepath.Join(dir, "pub", "big.bin"), big, 0o644)
	os.Chtimes(filepath.Join(dir, "pub", "hello.txt"), time.Time{}, time.Date(2025, 12, 24, 10, 30, 0, 0, time.UTC))
	os.Symlink(filepath.Join(dir, "secret.txt"), filepath.Join(dir, "pub", "link-out"))
	os.Symlink("hello.txt", filepath.Join(dir, "pub", "link-in"))
	root, err := os.OpenRoot(filepath.Join(dir, "pub"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dir
}

func serveFiles(t *testing.T, root *os.Root) *bitetest.Server {
	return start(t, func(a *bites.App) {
		a.Get("/files/{name...}", func(_ context.Context, in FileIn) (bites.File, error) {
			return bites.File{Root: root, Name: in.Path.Name}, nil
		})
	})
}

func TestAFileFromAnOsRootIsSentWithItsLengthLastModifiedAndTypeFromItsExtension(t *testing.T) {
	root, _ := fileRoot(t)
	srv := serveFiles(t, root)
	r := get(t, srv, "/files/hello.txt")
	if r.Status != 200 || r.Text() != "hello, file" || r.Get("content-length") != "11" ||
		r.Get("last-modified") != "Wed, 24 Dec 2025 10:30:00 GMT" || !strings.HasPrefix(r.Get("content-type"), "text/plain") ||
		r.Get("transfer-encoding") != "" {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Text())
	}
	if r := get(t, srv, "/files/sub/data.json"); r.Text() != `{"a":1}` || !strings.HasPrefix(r.Get("content-type"), "application/json") {
		t.Fatalf("%v %q", r.Header, r.Text())
	}
	if r := get(t, srv, "/files/noext"); r.Get("content-type") != "application/octet-stream" || r.Text() != "x" {
		t.Fatalf("%v", r.Header)
	}
	if r := get(t, srv, "/files/empty.txt"); r.Status != 200 || r.Get("content-length") != "0" || r.Text() != "" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if r := get(t, srv, "/files/link-in"); r.Text() != "hello, file" {
		t.Fatalf("a symlink inside the root: %q", r.Text())
	}
}

func TestALargeFileIsSentWholeInChunksOfTheWriterAndKeepsTheConnectionOpen(t *testing.T) {
	root, dir := fileRoot(t)
	srv := serveFiles(t, root)
	want, _ := os.ReadFile(filepath.Join(dir, "pub", "big.bin"))
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	for range 2 {
		fmt.Fprint(c, "GET /files/big.bin HTTP/1.1\r\nHost: x\r\n\r\n")
		r := srv.Read(c, false)
		if r.Status != 200 || r.Get("content-length") != "300000" || string(r.Body) != string(want) {
			t.Fatalf("%d %v len=%d", r.Status, r.Header, len(r.Body))
		}
	}
}

func TestAHeadRequestToAFileRouteGetsTheHeadWithTheSameLengthAndNoBody(t *testing.T) {
	root, _ := fileRoot(t)
	srv := serveFiles(t, root)
	r := srv.Do(bitetest.Request{Method: "HEAD", Target: "/files/hello.txt"})
	if r.Status != 200 || r.Get("content-length") != "11" || r.Get("last-modified") == "" || len(r.Body) != 0 {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Body)
	}
}

func TestAMissingFileADirectoryAndANameThatLeavesTheDirectoryAreTheSame404(t *testing.T) {
	root, _ := fileRoot(t)
	srv := serveFiles(t, root)
	want := `{"title":"Not Found","status":404}`
	for _, name := range []string{"nope.txt", "sub", "sub/", "../secret.txt", "sub/../../secret.txt", "%2e%2e/secret.txt", "/etc/passwd", "link-out", "a//b", "./hello.txt", "hello.txt/"} {
		r := get(t, srv, "/files/"+name)
		if r.Status == 400 && strings.Contains(r.Text(), "the path is not normalised") {
			continue // the router refuses a path with .. or // before the File is built
		}
		if r.Status != 404 || r.Get("content-type") != "application/problem+json" || r.Text() != want {
			t.Errorf("%q: %d %s", name, r.Status, r.Text())
		}
		if strings.Contains(r.Text(), "SECRET") {
			t.Fatalf("%q leaked the file outside the root", name)
		}
	}
}

func TestAFileNameHandedToTheRootDirectlyCannotLeaveItEvenWhenTheRouterWouldPassItOn(t *testing.T) {
	root, _ := fileRoot(t)
	srv := start(t, func(a *bites.App) {
		a.Get("/f", func(_ context.Context, in struct{ Query struct{ Name string } }) (bites.File, error) {
			return bites.File{Root: root, Name: in.Query.Name}, nil
		})
	})
	for _, name := range []string{"../secret.txt", "sub/../../secret.txt", "/etc/passwd", "link-out", "", ".", ".."} {
		r := get(t, srv, "/f?name="+strings.ReplaceAll(name, "/", "%2F"))
		if r.Status != 404 || strings.Contains(r.Text(), "SECRET") {
			t.Errorf("%q: %d %s", name, r.Status, r.Text())
		}
	}
}

func TestAFileFromAnFSIsCheckedWithValidPathAndSentTheSame(t *testing.T) {
	mem := fstest.MapFS{
		"a.css":       {Data: []byte("body{}"), ModTime: time.Date(2024, 2, 29, 23, 59, 58, 0, time.UTC)},
		"dir/b.txt":   {Data: []byte("bee")},
		"outside.txt": {Data: []byte("SECRET")},
	}
	var sub fs.FS = mem
	sub, _ = fs.Sub(mem, "dir")
	srv := start(t, func(a *bites.App) {
		a.Get("/css", func(context.Context, Empty) (bites.File, error) { return bites.File{FS: mem, Name: "a.css"}, nil })
		a.Get("/sub", func(_ context.Context, in struct{ Query struct{ Name string } }) (bites.File, error) {
			return bites.File{FS: sub, Name: in.Query.Name, ContentType: "text/x-custom"}, nil
		})
	})
	r := get(t, srv, "/css")
	if r.Status != 200 || r.Text() != "body{}" || r.Get("last-modified") != "Thu, 29 Feb 2024 23:59:58 GMT" || !strings.HasPrefix(r.Get("content-type"), "text/css") {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Text())
	}
	if r := get(t, srv, "/sub?name=b.txt"); r.Text() != "bee" || r.Get("content-type") != "text/x-custom" {
		t.Fatalf("%v %q", r.Header, r.Text())
	}
	for _, name := range []string{"../outside.txt", "/outside.txt", "outside.txt", "..", "", "a//b"} {
		if r := get(t, srv, "/sub?name="+strings.ReplaceAll(name, "/", "%2F")); r.Status != 404 || strings.Contains(r.Text(), "SECRET") {
			t.Errorf("%q: %d %s", name, r.Status, r.Text())
		}
	}
}

func TestAFileWithBothOrNeitherOfRootAndFSIsA500AndLogged(t *testing.T) {
	root, _ := fileRoot(t)
	srv, lb := startLogged(t, func(a *bites.App) {
		a.Get("/none", func(context.Context, Empty) (bites.File, error) { return bites.File{Name: "x"}, nil })
		a.Get("/both", func(context.Context, Empty) (bites.File, error) {
			return bites.File{Root: root, FS: fstest.MapFS{}, Name: "x"}, nil
		})
		a.Get("/badtype", func(context.Context, Empty) (bites.File, error) {
			return bites.File{Root: root, Name: "hello.txt", ContentType: "nonsense"}, nil
		})
	})
	for _, p := range []string{"/none", "/both", "/badtype"} {
		if r := get(t, srv, p); r.Status != 500 {
			t.Errorf("%s: %d", p, r.Status)
		}
	}
	if !strings.Contains(lb.String(), "a File needs exactly one of Root and FS") || !strings.Contains(lb.String(), `a File has the content type \"nonsense\"`) {
		t.Fatalf("log: %s", lb.String())
	}
}

func TestAFilesHeaderContentTypeAndCookiesAreUsedAndTheHeaderWins(t *testing.T) {
	root, _ := fileRoot(t)
	srv := start(t, func(a *bites.App) {
		a.Get("/f", func(context.Context, Empty) (bites.File, error) {
			return bites.File{Root: root, Name: "hello.txt", Header: bites.Header{
				"Content-Type": {"application/x-wins"}, "Content-Disposition": {`attachment; filename="h.txt"`}, "Content-Length": {"1"},
			}, Cookies: []bites.Cookie{{Name: "seen", Value: "1"}}}, nil
		})
	})
	r := get(t, srv, "/f")
	if r.Get("content-type") != "application/x-wins" || r.Get("content-disposition") != `attachment; filename="h.txt"` ||
		r.Get("content-length") != "11" || !strings.HasPrefix(r.Get("set-cookie"), "seen=1;") || r.Text() != "hello, file" {
		t.Fatalf("%v %q", r.Header, r.Text())
	}
}

func TestAFileThatChangesSizeAfterTheStatIsA500AndOneThatShrinksWhileSentClosesTheConnection(t *testing.T) {
	root, dir := fileRoot(t)
	path := filepath.Join(dir, "pub", "grow.txt")
	os.WriteFile(path, []byte("12345"), 0o644)
	// The size is read at the stat; the handler changes the file after it by being the middleware.
	srv, _ := startLogged(t, func(a *bites.App) {
		a.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			resp := next.Call(ctx)
			os.WriteFile(path, []byte("1234567890"), 0o644) // grows after the stat sized the answer
			return resp, nil
		})
		a.Get("/g", func(context.Context, Empty) (bites.File, error) {
			return bites.File{Root: root, Name: "grow.txt"}, nil
		})
	})
	if r := get(t, srv, "/g"); r.Status != 500 {
		t.Fatalf("a file that grew between the stat and the open: %d %s", r.Status, r.Text())
	}
}

func TestMiddlewareSeesAFileAsAStreamingResponse(t *testing.T) {
	root, _ := fileRoot(t)
	var streaming bool
	var ctype string
	srv := start(t, func(a *bites.App) {
		a.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			resp := next.Call(ctx)
			streaming, ctype = resp.Streaming(), resp.ContentType()
			return resp, nil
		})
		a.Get("/f", func(context.Context, Empty) (bites.File, error) {
			return bites.File{Root: root, Name: "hello.txt"}, nil
		})
	})
	if r := get(t, srv, "/f"); r.Status != 200 || !streaming || !strings.HasPrefix(ctype, "text/plain") {
		t.Fatalf("%d streaming=%v %q", r.Status, streaming, ctype)
	}
}

// ---- Validators, conditional and range requests (ADR 0064) ----

func doFile(t *testing.T, srv *bitetest.Server, method, target string, h map[string]string) bitetest.Response {
	t.Helper()
	hdr := map[string][]string{}
	for k, v := range h {
		hdr[k] = []string{v}
	}
	return srv.Do(bitetest.Request{Method: method, Target: target, Header: hdr})
}

func TestAFileWithAModificationTimeCarriesAStrongETagOfItsTimeAndSizeAndAcceptRanges(t *testing.T) {
	root, _ := fileRoot(t)
	srv := serveFiles(t, root)
	r := get(t, srv, "/files/hello.txt")
	mod := time.Date(2025, 12, 24, 10, 30, 0, 0, time.UTC)
	want := fmt.Sprintf(`"%x-%x"`, mod.UnixNano(), 11)
	if r.Status != 200 || r.Get("etag") != want || r.Get("accept-ranges") != "bytes" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

func TestAFileFromAnFSWithNoModificationTimeHasNoETagAndNoLastModified(t *testing.T) {
	mem := fstest.MapFS{"a.txt": {Data: []byte("abc")}}
	srv := start(t, func(a *bites.App) {
		a.Get("/a", func(context.Context, Empty) (bites.File, error) { return bites.File{FS: mem, Name: "a.txt"}, nil })
	})
	r := get(t, srv, "/a")
	if r.Status != 200 || r.Get("etag") != "" || r.Get("last-modified") != "" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if r := doFile(t, srv, "GET", "/a", map[string]string{"If-None-Match": `"x"`, "If-Modified-Since": "Wed, 24 Dec 2099 10:30:00 GMT"}); r.Status != 200 || r.Text() != "abc" {
		t.Fatalf("with no validators, a conditional request gets the file: %d", r.Status)
	}
}

func TestIfNoneMatchWithTheCurrentTagIsA304WithTheValidatorsAndNoBody(t *testing.T) {
	root, _ := fileRoot(t)
	srv := serveFiles(t, root)
	tag := get(t, srv, "/files/hello.txt").Get("etag")
	for _, inm := range []string{tag, `"other", ` + tag, "W/" + tag, "*"} {
		r := doFile(t, srv, "GET", "/files/hello.txt", map[string]string{"If-None-Match": inm})
		if r.Status != 304 || len(r.Body) != 0 || r.Get("etag") != tag || r.Get("last-modified") == "" ||
			r.Get("content-length") != "" || r.Get("content-type") != "" {
			t.Fatalf("%q: %d %v %q", inm, r.Status, r.Header, r.Body)
		}
	}
	if r := doFile(t, srv, "HEAD", "/files/hello.txt", map[string]string{"If-None-Match": tag}); r.Status != 304 {
		t.Fatalf("HEAD: %d", r.Status)
	}
	if r := doFile(t, srv, "GET", "/files/hello.txt", map[string]string{"If-None-Match": `"stale"`}); r.Status != 200 || r.Text() != "hello, file" {
		t.Fatalf("a stale tag gets the file: %d", r.Status)
	}
}

func TestIfNoneMatchWinsOverIfModifiedSinceAndIfModifiedSinceAloneIsA304WhenNotModified(t *testing.T) {
	root, _ := fileRoot(t)
	srv := serveFiles(t, root)
	lm := get(t, srv, "/files/hello.txt").Get("last-modified")
	if r := doFile(t, srv, "GET", "/files/hello.txt", map[string]string{"If-Modified-Since": lm}); r.Status != 304 {
		t.Fatalf("the same date: %d", r.Status)
	}
	if r := doFile(t, srv, "GET", "/files/hello.txt", map[string]string{"If-Modified-Since": "Tue, 23 Dec 2025 10:30:00 GMT"}); r.Status != 200 {
		t.Fatalf("an older date: %d", r.Status)
	}
	if r := doFile(t, srv, "GET", "/files/hello.txt", map[string]string{"If-Modified-Since": "Wednesday, 24-Dec-25 10:30:00 GMT"}); r.Status != 304 {
		t.Fatalf("the RFC 850 date: %d", r.Status)
	}
	if r := doFile(t, srv, "GET", "/files/hello.txt", map[string]string{"If-Modified-Since": "Wed Dec 24 10:30:00 2025"}); r.Status != 304 {
		t.Fatalf("the asctime date: %d", r.Status)
	}
	if r := doFile(t, srv, "GET", "/files/hello.txt", map[string]string{"If-Modified-Since": "not a date"}); r.Status != 200 {
		t.Fatalf("a date that does not parse is ignored: %d", r.Status)
	}
	// RFC 9110 section 13.1.3: If-Modified-Since is ignored when If-None-Match is present.
	if r := doFile(t, srv, "GET", "/files/hello.txt", map[string]string{"If-Modified-Since": lm, "If-None-Match": `"stale"`}); r.Status != 200 {
		t.Fatalf("If-None-Match decides: %d", r.Status)
	}
}

func TestIfMatchAndIfUnmodifiedSinceThatFailAreA412ProblemAndOpenNothing(t *testing.T) {
	root, _ := fileRoot(t)
	srv := serveFiles(t, root)
	tag := get(t, srv, "/files/hello.txt").Get("etag")
	want := `{"title":"Precondition Failed","status":412}`
	for _, h := range []map[string]string{
		{"If-Match": `"stale"`},
		{"If-Match": "W/" + tag}, // If-Match compares strongly
		{"If-Unmodified-Since": "Tue, 23 Dec 2025 10:30:00 GMT"},
	} {
		r := doFile(t, srv, "GET", "/files/hello.txt", h)
		if r.Status != 412 || r.Get("content-type") != "application/problem+json" || r.Text() != want {
			t.Errorf("%v: %d %s", h, r.Status, r.Text())
		}
	}
	for _, h := range []map[string]string{{"If-Match": tag}, {"If-Match": "*"}, {"If-Unmodified-Since": "Wed, 24 Dec 2025 10:30:00 GMT"}} {
		if r := doFile(t, srv, "GET", "/files/hello.txt", h); r.Status != 200 || r.Text() != "hello, file" {
			t.Errorf("%v: %d", h, r.Status)
		}
	}
}

func TestIfNoneMatchThatMatchesOnAMethodOtherThanGetIsA412(t *testing.T) {
	root, _ := fileRoot(t)
	srv := start(t, func(a *bites.App) {
		a.Post("/f", func(context.Context, Empty) (bites.File, error) {
			return bites.File{Root: root, Name: "hello.txt"}, nil
		})
	})
	if r := doFile(t, srv, "POST", "/f", map[string]string{"If-None-Match": "*"}); r.Status != 412 {
		t.Fatalf("%d", r.Status)
	}
}

func TestARangeOfOnePartIsA206WithContentRangeAndOnlyThoseBytes(t *testing.T) {
	root, dir := fileRoot(t)
	srv := serveFiles(t, root)
	big, _ := os.ReadFile(filepath.Join(dir, "pub", "big.bin"))
	for _, tc := range []struct {
		rng      string
		from, to int
	}{
		{"bytes=0-0", 0, 0},
		{"bytes=10-19", 10, 19},
		{"bytes=299990-", 299990, 299999},
		{"bytes=-5", 299995, 299999},
		{"bytes=100000-999999999", 100000, 299999},
		{"bytes=-999999", 0, 299999},
		{"Bytes=1-2", 1, 2},
	} {
		r := doFile(t, srv, "GET", "/files/big.bin", map[string]string{"Range": tc.rng})
		wantCR := fmt.Sprintf("bytes %d-%d/300000", tc.from, tc.to)
		if r.Status != 206 || r.Get("content-range") != wantCR || r.Get("content-length") != fmt.Sprint(tc.to-tc.from+1) ||
			string(r.Body) != string(big[tc.from:tc.to+1]) {
			t.Errorf("%q: %d %v len=%d", tc.rng, r.Status, r.Header, len(r.Body))
		}
	}
}

func TestARangeThatStartsPastTheEndIsA416NamingTheSize(t *testing.T) {
	root, _ := fileRoot(t)
	srv := serveFiles(t, root)
	for _, rng := range []string{"bytes=11-", "bytes=500-600", "bytes=-0"} {
		r := doFile(t, srv, "GET", "/files/hello.txt", map[string]string{"Range": rng})
		if r.Status != 416 || r.Get("content-range") != "bytes */11" || r.Text() != `{"title":"Range Not Satisfiable","status":416}` {
			t.Errorf("%q: %d %v %s", rng, r.Status, r.Header, r.Text())
		}
	}
}

func TestSeveralRangesAnotherUnitAndARangeThatDoesNotParseGetTheWholeFile(t *testing.T) {
	root, _ := fileRoot(t)
	srv := serveFiles(t, root)
	many := "bytes=" + strings.Repeat("0-1,", 500) + "0-1"
	for _, rng := range []string{"bytes=0-1,3-4", many, "items=0-1", "bytes=5-2", "bytes=a-b", "bytes=1", "bytes=--1"} {
		r := doFile(t, srv, "GET", "/files/hello.txt", map[string]string{"Range": rng})
		if r.Status != 200 || r.Text() != "hello, file" || r.Get("content-range") != "" {
			t.Errorf("%.40q: %d %v", rng, r.Status, r.Header)
		}
	}
	if r := doFile(t, srv, "GET", "/files/empty.txt", map[string]string{"Range": "bytes=0-"}); r.Status != 200 || r.Get("content-length") != "0" {
		t.Errorf("an empty file ignores Range: %d %v", r.Status, r.Header)
	}
	if r := doFile(t, srv, "HEAD", "/files/hello.txt", map[string]string{"Range": "bytes=0-1"}); r.Status != 200 || r.Get("content-length") != "11" {
		t.Errorf("HEAD ignores Range: %d %v", r.Status, r.Header)
	}
}

func TestIfRangeThatNoLongerNamesTheFileGetsTheWholeFile(t *testing.T) {
	root, _ := fileRoot(t)
	srv := serveFiles(t, root)
	first := get(t, srv, "/files/hello.txt")
	tag, lm := first.Get("etag"), first.Get("last-modified")
	for _, tc := range []struct {
		ifRange string
		want    int
	}{{tag, 206}, {lm, 206}, {`"stale"`, 200}, {"W/" + tag, 200}, {"Tue, 23 Dec 2025 10:30:00 GMT", 200}, {"junk", 200}} {
		r := doFile(t, srv, "GET", "/files/hello.txt", map[string]string{"Range": "bytes=0-4", "If-Range": tc.ifRange})
		if r.Status != tc.want {
			t.Errorf("If-Range %q: %d, want %d", tc.ifRange, r.Status, tc.want)
		}
	}
}

func TestAnETagAndLastModifiedInHeaderReplaceTheMadeOnesInTheAnswerAndTheComparisons(t *testing.T) {
	root, _ := fileRoot(t)
	srv, lb := startLogged(t, func(a *bites.App) {
		a.Get("/f", func(context.Context, Empty) (bites.File, error) {
			return bites.File{Root: root, Name: "hello.txt", Header: bites.Header{"ETag": {`"v42"`}, "Cache-Control": {"max-age=60"}, "X-Extra": {"1"}}}, nil
		})
		a.Get("/bad", func(context.Context, Empty) (bites.File, error) {
			return bites.File{Root: root, Name: "hello.txt", Header: bites.Header{"ETag": {"unquoted"}}}, nil
		})
		a.Get("/norange", func(context.Context, Empty) (bites.File, error) {
			return bites.File{Root: root, Name: "hello.txt", Header: bites.Header{"Accept-Ranges": {"none"}}}, nil
		})
	})
	if r := get(t, srv, "/f"); r.Get("etag") != `"v42"` || len(r.Header["etag"]) != 1 {
		t.Fatalf("%v", r.Header)
	}
	r := doFile(t, srv, "GET", "/f", map[string]string{"If-None-Match": `"v42"`})
	if r.Status != 304 || r.Get("cache-control") != "max-age=60" || r.Get("x-extra") != "" {
		t.Fatalf("a 304 carries the cache headers and not the others: %d %v", r.Status, r.Header)
	}
	if r := get(t, srv, "/bad"); r.Status != 500 || !strings.Contains(lb.String(), "which is not an entity-tag") {
		t.Fatalf("%d %s", r.Status, lb.String())
	}
	if r := doFile(t, srv, "GET", "/norange", map[string]string{"Range": "bytes=0-1"}); r.Status != 200 || r.Get("accept-ranges") != "none" {
		t.Fatalf("Accept-Ranges: none turns ranges off: %d %v", r.Status, r.Header)
	}
}

func TestARangeOfAFileFromAnFSWithoutSeekIsReadUpToItsStart(t *testing.T) {
	data := []byte("0123456789abcdefghij")
	srv := start(t, func(a *bites.App) {
		a.Get("/n", func(context.Context, Empty) (bites.File, error) {
			return bites.File{FS: noSeekFS{fstest.MapFS{"n.txt": {Data: data, ModTime: time.Unix(1700000000, 0)}}}, Name: "n.txt"}, nil
		})
	})
	if r := doFile(t, srv, "GET", "/n", map[string]string{"Range": "bytes=10-14"}); r.Status != 206 || r.Text() != "abcde" {
		t.Fatalf("%d %q", r.Status, r.Text())
	}
}

// noSeekFS hides every method of its files but Read, Stat and Close.
type noSeekFS struct{ fs.FS }

type noSeekFile struct{ f fs.File }

func (n noSeekFS) Open(name string) (fs.File, error) {
	f, err := n.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return noSeekFile{f}, nil
}
func (f noSeekFile) Read(b []byte) (int, error) { return f.f.Read(b) }
func (f noSeekFile) Stat() (fs.FileInfo, error) { return f.f.Stat() }
func (f noSeekFile) Close() error               { return f.f.Close() }

func TestA206IsNeverCompressedAndA416KeepsItsContentRangeWithGzipOn(t *testing.T) {
	root, _ := fileRoot(t)
	srv := startWith(t, func(a *bites.App) {
		a.Get("/files/{name...}", func(_ context.Context, in FileIn) (bites.File, error) {
			return bites.File{Root: root, Name: in.Path.Name}, nil
		})
	}, bites.WithGzip(bites.Gzip{}))
	r := doFile(t, srv, "GET", "/files/big.bin", map[string]string{"Range": "bytes=0-4095", "Accept-Encoding": "gzip"})
	if r.Status != 206 || r.Get("content-encoding") != "" || len(r.Body) != 4096 {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	r = doFile(t, srv, "GET", "/files/big.bin", map[string]string{"Range": "bytes=999999-", "Accept-Encoding": "gzip"})
	if r.Status != 416 || r.Get("content-encoding") != "" || r.Get("content-range") != "bytes */300000" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

func TestAFileWithAnAttachmentNameIsADownloadOnGetHeadRangeAndGzipButNotOnA304(t *testing.T) {
	root, _ := fileRoot(t)
	srv := startWith(t, func(a *bites.App) {
		for i, c := range downloadCases {
			a.Get("/dl/"+strconv.Itoa(i), func(context.Context, Empty) (bites.File, error) {
				return bites.File{Root: root, Name: "hello.txt", Attachment: c.name}, nil
			})
		}
	}, bites.WithGzip(bites.Gzip{}))
	if r := doFile(t, srv, "GET", "/dl/0", nil); r.Get("etag") != "" {
		if nm := doFile(t, srv, "GET", "/dl/0", map[string]string{"If-None-Match": r.Get("etag")}); nm.Status != 304 || len(headerValues(nm, "Content-Disposition")) != 0 {
			t.Errorf("304: %d %v", nm.Status, nm.Header)
		}
	} else {
		t.Error("no ETag, so the 304 was not tested")
	}
	for i, c := range downloadCases {
		target := "/dl/" + strconv.Itoa(i)
		r := doFile(t, srv, "GET", target, nil)
		oneDisposition(t, r, c.want)
		if r.Status != 200 || r.Text() != "hello, file" {
			t.Errorf("%s: %d %q", target, r.Status, r.Text())
		}
		h := doFile(t, srv, "HEAD", target, nil)
		oneDisposition(t, h, c.want)
		if h.Get("content-length") != "11" || len(h.Body) != 0 {
			t.Errorf("%s HEAD: %v", target, h.Header)
		}
		oneDisposition(t, doFile(t, srv, "GET", target, map[string]string{"Accept-Encoding": "gzip"}), c.want)
		oneDisposition(t, doFile(t, srv, "GET", target, map[string]string{"Range": "bytes=0-4"}), c.want)
	}
}
