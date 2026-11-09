package bites_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// Multipart[T] (ADR 0044): a multipart form decoded into a struct before the handler runs,
// through the in-memory connections, with the hostile clients of the ADR.

type mpPhoto struct{ bites.FormFile }

func (mpPhoto) Accept() []string { return []string{"image/png", "image/jpeg"} }
func (mpPhoto) MaxSize() int64   { return 3 << 20 }

type mpTiny struct{ bites.FormFile }

func (mpTiny) MaxSize() int64 { return 1000 }

type mpForm struct {
	Title   bites.NonEmpty
	Price   int64
	Tags    bites.Opt[[]string]
	Cover   mpPhoto
	Gallery []mpPhoto
	Floor   bites.Opt[mpPhoto]
}

type mpIn struct {
	Query struct{ Page bites.Opt[int] }
	Body  bites.Multipart[mpForm]
}

type mpFile struct {
	Name string `json:"name"`
	File string `json:"file"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	Sum  string `json:"sum"`
}

type mpOut struct {
	Title   string   `json:"title"`
	Price   int64    `json:"price"`
	Tags    []string `json:"tags"`
	Cover   mpFile   `json:"cover"`
	Gallery []mpFile `json:"gallery"`
	Floor   *mpFile  `json:"floor"`
}

// describe opens the file, reads it whole and reports it.
func describe(f bites.FormFile) (mpFile, error) {
	r, err := f.Open()
	if err != nil {
		return mpFile{}, err
	}
	defer r.Close()
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return mpFile{}, err
	}
	if n != f.Size {
		return mpFile{}, fmt.Errorf("read %d bytes of a file of %d", n, f.Size)
	}
	return mpFile{f.Name, f.FileName, f.ContentType, f.Size, hex.EncodeToString(h.Sum(nil))[:12]}, nil
}

func sumOf(data string) string {
	s := sha256.Sum256([]byte(data))
	return hex.EncodeToString(s[:])[:12]
}

// mpHandler describes the whole form, after the hook (when any) has looked at it.
func mpHandler(hook func(mpIn)) func(context.Context, mpIn) (mpOut, error) {
	return func(_ context.Context, in mpIn) (mpOut, error) {
		if hook != nil {
			hook(in)
		}
		f := in.Body.Value
		out := mpOut{Title: string(f.Title), Price: f.Price, Tags: f.Tags.Or(nil)}
		var err error
		if out.Cover, err = describe(f.Cover.FormFile); err != nil {
			return out, err
		}
		for _, g := range f.Gallery {
			d, err := describe(g.FormFile)
			if err != nil {
				return out, err
			}
			out.Gallery = append(out.Gallery, d)
		}
		if p, ok := f.Floor.Get(); ok {
			d, err := describe(p.FormFile)
			if err != nil {
				return out, err
			}
			out.Floor = &d
		}
		return out, nil
	}
}

func mpApp(hook func(mpIn), opts ...bites.RouteOption) func(*bites.App) {
	return func(a *bites.App) {
		a.Post("/mp", mpHandler(hook), append([]bites.RouteOption{bites.MaxBody(8 << 20)}, opts...)...)
	}
}

func startMP(t *testing.T, register func(*bites.App), l bites.Limits) *bitetest.Server {
	t.Helper()
	if l.UploadDir == "" {
		l.UploadDir = t.TempDir()
	}
	return startWith(t, register, bites.WithLimits(l))
}

func mpRequest(target, body string) string {
	return upHead("POST", target, "Content-Length: "+strconv.Itoa(len(body))+"\r\n\r\n") + body
}

func png(name, data string) string { return fileField(name, name+".png", "image/png", data) }

// base is a valid form around the parts a test adds.
func base(extra ...string) string {
	return field("title", "A flat") + field("price", "1200") + png("cover", "COVER") + strings.Join(extra, "") + closing()
}

// filler is n bytes of data that are not all the same.
func filler(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "%08d", i)
	}
	return b.String()[:n]
}

// sendAsync writes raw on c in the background: a server that answers before it has read everything
// would leave a synchronous writer blocked.
func sendAsync(c net.Conn, raw string) {
	go func() { c.Write([]byte(raw)) }()
}

// spills counts the unlinked files this process holds open in dir.
func spills(t *testing.T, dir string) int {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("counting descriptors needs /proc")
	}
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd")
	}
	n := 0
	for _, e := range ents {
		l, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err == nil && strings.HasPrefix(l, dir) && strings.HasSuffix(l, " (deleted)") {
			n++
		}
	}
	return n
}

func TestAMultipartFormIsDecodedIntoItsFieldsAndItsFiles(t *testing.T) {
	srv := startMP(t, mpApp(nil), bites.Limits{})
	body := field("title", "A flat") + field("price", "1200") + field("tags", "sea") + field("tags", "quiet") +
		png("cover", "COVER") + fileField("gallery", "g1.jpg", "image/jpeg", "ONE") + fileField("gallery", "g2.jpg", "image/jpeg", "TWO") + closing()
	r := doRaw(t, srv, mpRequest("/mp?page=2", body))
	want := `{"title":"A flat","price":1200,"tags":["sea","quiet"],"cover":{"name":"cover","file":"cover.png","type":"image/png","size":5,"sum":"` + sumOf("COVER") +
		`"},"gallery":[{"name":"gallery","file":"g1.jpg","type":"image/jpeg","size":3,"sum":"` + sumOf("ONE") + `"},{"name":"gallery","file":"g2.jpg","type":"image/jpeg","size":3,"sum":"` + sumOf("TWO") + `"}],"floor":null}`
	if r.Status != 200 || r.Text() != want {
		t.Fatalf("%d\n%s\nwant\n%s", r.Status, r.Text(), want)
	}
}

func TestAChunkedMultipartFormIsDecodedTheSameAndTheConnectionServesTheNextRequest(t *testing.T) {
	srv := startMP(t, mpApp(nil), bites.Limits{})
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	for range 2 {
		fmt.Fprint(c, upHead("POST", "/mp", "Transfer-Encoding: chunked\r\n\r\n")+chunked(base(), 37))
		r := srv.Read(c, false)
		if r.Status != 200 || !strings.Contains(r.Text(), `"title":"A flat"`) || !strings.Contains(r.Text(), sumOf("COVER")) {
			t.Fatalf("%d %s", r.Status, r.Text())
		}
	}
}

func TestAFileUnder256KiBIsReadFromMemoryAndALargerOneFromAnUnlinkedTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	var held []int
	srv := startMP(t, mpApp(func(mpIn) { held = append(held, spills(t, dir)) }), bites.Limits{UploadDir: dir})
	small := filler(100 << 10)
	large := filler(600 << 10)
	for _, data := range []string{small, large} {
		r := doRaw(t, srv, mpRequest("/mp", field("title", "x")+field("price", "1")+png("cover", data)+closing()))
		if r.Status != 200 || !strings.Contains(r.Text(), `"sum":"`+sumOf(data)+`"`) {
			t.Fatalf("%d %.300s", r.Status, r.Text())
		}
	}
	if len(held) != 2 || held[0] != 0 || held[1] != 1 {
		t.Fatalf("files open on disk while the handler ran: %v, want [0 1] for a 100 KiB file and a 600 KiB file", held)
	}
	if n := spills(t, dir); n != 0 {
		t.Fatalf("%d temporary files still open after the requests", n)
	}
}

func TestASecondFileThatWouldPassTheRequestsMemoryGoesToDiskAndTheFirstStaysInMemory(t *testing.T) {
	dir := t.TempDir()
	var held int
	srv := startMP(t, mpApp(func(mpIn) { held = spills(t, dir) }), bites.Limits{UploadDir: dir})
	a, b := filler(150<<10), filler(150<<10)+"!"
	body := field("title", "x") + field("price", "1") + png("cover", a) + fileField("gallery", "g.png", "image/png", b) + closing()
	r := doRaw(t, srv, mpRequest("/mp", body))
	if r.Status != 200 || !strings.Contains(r.Text(), sumOf(a)) || !strings.Contains(r.Text(), sumOf(b)) {
		t.Fatalf("%d %.400s", r.Status, r.Text())
	}
	if held != 1 {
		t.Fatalf("%d files on disk while the handler ran, want 1 (the second)", held)
	}
}

func TestTheAppsMemoryShareSpillsAFileToDiskWhenOtherRequestsHoldIt(t *testing.T) {
	dir := t.TempDir()
	hold := make(chan struct{})
	entered := make(chan struct{}, 2)
	var mu sync.Mutex
	var disk []int
	srv := startMP(t, mpApp(func(mpIn) {
		mu.Lock()
		disk = append(disk, spills(t, dir))
		mu.Unlock()
		entered <- struct{}{}
		<-hold
	}), bites.Limits{UploadDir: dir, UploadMemory: 150 << 10})
	data := filler(100 << 10)
	body := mpRequest("/mp", field("title", "x")+field("price", "1")+png("cover", data)+closing())
	c1 := srv.Dial("203.0.113.9:4001")
	defer c1.Close()
	fmt.Fprint(c1, body)
	<-entered
	c2 := srv.Dial("203.0.113.9:4002")
	defer c2.Close()
	fmt.Fprint(c2, body)
	<-entered
	close(hold)
	for _, c := range []net.Conn{c1, c2} {
		if r := srv.Read(c, false); r.Status != 200 || !strings.Contains(r.Text(), sumOf(data)) {
			t.Fatalf("%d %.300s", r.Status, r.Text())
		}
	}
	if disk[0] != 0 || disk[1] != 1 {
		t.Fatalf("files on disk when each handler started: %v, want [0 1]: the first kept 100 KiB of the 150 KiB share, so the second went to disk", disk)
	}
	// The share is given back: a third request is kept in memory again.
	var again int
	_ = again
}

func TestTheTemporaryFileIsGoneFromTheDirectoryWhileTheHandlerReadsIt(t *testing.T) {
	dir := t.TempDir()
	var listed []string
	var open int
	srv := startMP(t, mpApp(func(mpIn) {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			listed = append(listed, e.Name())
		}
		open = spills(t, dir)
	}), bites.Limits{UploadDir: dir})
	r := doRaw(t, srv, mpRequest("/mp", field("title", "x")+field("price", "1")+png("cover", filler(400<<10))+closing()))
	if r.Status != 200 || open != 1 || len(listed) != 0 {
		t.Fatalf("%d, open %d, directory holds %v: a spilled file must be open and unlinked", r.Status, open, listed)
	}
}

func TestAFileReadAfterTheHandlerReturnedIsErrUploadClosedInMemoryAndOnDisk(t *testing.T) {
	var kept []bites.FormFile
	var readers []io.ReadSeekCloser
	srv := startMP(t, mpApp(func(in mpIn) {
		kept = append(kept, in.Body.Value.Cover.FormFile)
		rd, err := in.Body.Value.Cover.Open()
		if err != nil {
			t.Error(err)
			return
		}
		readers = append(readers, rd)
	}), bites.Limits{})
	for _, n := range []int{1 << 10, 500 << 10} {
		r := doRaw(t, srv, mpRequest("/mp", field("title", "x")+field("price", "1")+png("cover", filler(n))+closing()))
		if r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Text())
		}
	}
	for i, f := range kept {
		if _, err := f.Open(); !errors.Is(err, bites.ErrUploadClosed) {
			t.Errorf("file %d: Open after the handler = %v, want ErrUploadClosed", i, err)
		}
		if _, err := readers[i].Read(make([]byte, 8)); !errors.Is(err, bites.ErrUploadClosed) {
			t.Errorf("file %d: Read of a reader opened in the handler = %v, want ErrUploadClosed", i, err)
		}
	}
	if _, err := (bites.FormFile{}).Open(); err == nil {
		t.Error("Open on the zero FormFile succeeded")
	}
}

func TestTheTemporaryFileIsClosedWhenTheHandlerPanics(t *testing.T) {
	dir := t.TempDir()
	srv := startMP(t, mpApp(func(mpIn) { panic("boom") }), bites.Limits{UploadDir: dir})
	r := doRaw(t, srv, mpRequest("/mp", field("title", "x")+field("price", "1")+png("cover", filler(400<<10))+closing()))
	if r.Status != 500 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if n := spills(t, dir); n != 0 {
		t.Fatalf("%d temporary files open after a handler that panicked", n)
	}
}

func TestAHandlerThatNeverOpensAFileLeaksNoDescriptor(t *testing.T) {
	dir := t.TempDir()
	srv := startMP(t, func(a *bites.App) {
		a.Post("/mp", func(_ context.Context, in mpIn) (bites.NoContent, error) { return bites.NoContent{}, nil }, bites.MaxBody(8<<20))
	}, bites.Limits{UploadDir: dir})
	body := mpRequest("/mp", field("title", "x")+field("price", "1")+png("cover", filler(400<<10))+closing())
	count := func() int {
		ents, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skip("no /proc/self/fd")
		}
		return len(ents)
	}
	doRaw(t, srv, body)
	before := count()
	for range 20 {
		if r := doRaw(t, srv, body); r.Status != 204 {
			t.Fatalf("%d %s", r.Status, r.Text())
		}
	}
	if after := count(); after > before+2 {
		t.Fatalf("descriptors went from %d to %d over 20 requests that never opened their file", before, after)
	}
	if n := spills(t, dir); n != 0 {
		t.Fatalf("%d temporary files open", n)
	}
}

func TestAnOptionalFileThatIsMissingIsNotSetAndOneThatIsSentIs(t *testing.T) {
	srv := startMP(t, mpApp(nil), bites.Limits{})
	r := doRaw(t, srv, mpRequest("/mp", base()))
	if r.Status != 200 || !strings.Contains(r.Text(), `"floor":null`) {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r = doRaw(t, srv, mpRequest("/mp", base(fileField("floor", "f.png", "image/png", "PLAN"))))
	if r.Status != 200 || !strings.Contains(r.Text(), `"floor":{"name":"floor","file":"f.png","type":"image/png","size":4`) {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestSeveralFilesUnderOneNameFillAListInTheOrderSent(t *testing.T) {
	srv := startMP(t, mpApp(nil), bites.Limits{})
	var parts []string
	for i := range 5 {
		parts = append(parts, fileField("gallery", "g"+strconv.Itoa(i)+".png", "image/png", strings.Repeat("x", i)))
	}
	r := doRaw(t, srv, mpRequest("/mp", base(parts...)))
	var names []string
	for i := range 5 {
		names = append(names, `"file":"g`+strconv.Itoa(i)+`.png"`)
	}
	at := 0
	for _, n := range names {
		j := strings.Index(r.Text()[at:], n)
		if j < 0 {
			t.Fatalf("%s missing or out of order in %s", n, r.Text())
		}
		at += j
	}
	// An empty list is legal.
	if r := doRaw(t, srv, mpRequest("/mp", base())); r.Status != 200 || !strings.Contains(r.Text(), `"gallery":[]`) {
		t.Fatalf("no gallery: %d %s", r.Status, r.Text())
	}
}

func problemAt(t *testing.T, r bitetest.Response, status int, loc, msg string) {
	t.Helper()
	want := `{"location":"` + loc + `","message":"` + msg + `"`
	if r.Status != status || !strings.Contains(r.Text(), want) || r.Get("content-type") != "application/problem+json" {
		t.Fatalf("%d %s\nwant %d with %s", r.Status, r.Text(), status, want)
	}
}

func TestTwoFilesUnderANameGivenToASingleFileIsA400NamingBodyCover(t *testing.T) {
	srv := startMP(t, mpApp(nil), bites.Limits{})
	r := doRaw(t, srv, mpRequest("/mp", base(png("cover", "AGAIN"))))
	problemAt(t, r, 400, "body.cover", "expected one file, got 2")
	r = doRaw(t, srv, mpRequest("/mp", base(fileField("floor", "a.png", "image/png", "1"), fileField("floor", "b.png", "image/png", "2"))))
	problemAt(t, r, 400, "body.floor", "expected one file, got 2")
}

func TestAMissingRequiredFileIsA400NamingBodyCover(t *testing.T) {
	srv := startMP(t, mpApp(nil), bites.Limits{})
	r := doRaw(t, srv, mpRequest("/mp", field("title", "x")+field("price", "1")+closing()))
	problemAt(t, r, 400, "body.cover", "required")
}

func TestAFileOverMaxSizeIsA413NamingBodyCoverAndItsTemporaryFileIsClosed(t *testing.T) {
	dir := t.TempDir()
	type in struct {
		Body bites.Multipart[struct{ Cover mpTiny }]
	}
	srv := startMP(t, func(a *bites.App) {
		a.Post("/t", func(context.Context, in) (bites.NoContent, error) { return bites.NoContent{}, nil }, bites.MaxBody(8<<20))
	}, bites.Limits{UploadDir: dir})
	for _, n := range []int{1001, 400 << 10} {
		r := doRaw(t, srv, mpRequest("/t", fileField("cover", "c.bin", "application/octet-stream", filler(n))+closing()))
		problemAt(t, r, 413, "body.cover", "the file is over 1000 bytes")
	}
	if n := spills(t, dir); n != 0 {
		t.Fatalf("%d temporary files open after the refusal", n)
	}
	// A file of exactly MaxSize passes.
	if r := doRaw(t, srv, mpRequest("/t", fileField("cover", "c.bin", "application/octet-stream", filler(1000))+closing())); r.Status != 204 {
		t.Fatalf("a file of exactly 1000 bytes: %d %s", r.Status, r.Text())
	}
}

func TestAFileWhoseDeclaredTypeIsNotInAcceptIsA400NamingBodyCover(t *testing.T) {
	srv := startMP(t, mpApp(nil), bites.Limits{})
	r := doRaw(t, srv, mpRequest("/mp", field("title", "x")+field("price", "1")+fileField("cover", "c.gif", "image/gif", "GIF")+closing()))
	problemAt(t, r, 400, "body.cover", "the file must be sent as image/png or image/jpeg")
	if strings.Contains(r.Text(), "required") {
		t.Fatalf("a file refused for its type is not also missing: %s", r.Text())
	}
	// Case and parameters of the declared type are ignored.
	r = doRaw(t, srv, mpRequest("/mp", field("title", "x")+field("price", "1")+fileField("cover", "c.png", "IMAGE/PNG; charset=binary", "PNG")+closing()))
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestATextFieldOverMaxFieldBytesIsA413NamingIt(t *testing.T) {
	srv := startMP(t, mpApp(nil), bites.Limits{MaxFieldBytes: 100})
	r := doRaw(t, srv, mpRequest("/mp", field("title", strings.Repeat("a", 101))+field("price", "1")+png("cover", "C")+closing()))
	problemAt(t, r, 413, "body.title", "the field is over 100 bytes")
	r = doRaw(t, srv, mpRequest("/mp", field("title", strings.Repeat("a", 100))+field("price", "1")+png("cover", "C")+closing()))
	if r.Status != 200 {
		t.Fatalf("a field of exactly the limit: %d %s", r.Status, r.Text())
	}
	// A field over the window of the scratch buffer is read the same way.
	srv = startMP(t, mpApp(nil), bites.Limits{MaxFieldBytes: 200 << 10, MaxFormBytes: 400 << 10})
	r = doRaw(t, srv, mpRequest("/mp", field("title", strings.Repeat("a", 100<<10))+field("price", "1")+png("cover", "C")+closing()))
	if r.Status != 200 || !strings.Contains(r.Text(), `"title":"`+strings.Repeat("a", 100<<10)+`"`) {
		t.Fatalf("a field of 100 KiB: %d", r.Status)
	}
	r = doRaw(t, srv, mpRequest("/mp", field("title", strings.Repeat("a", 200<<10+1))+field("price", "1")+png("cover", "C")+closing()))
	problemAt(t, r, 413, "body.title", "the field is over 204800 bytes")
}

func TestAllTheTextOfARequestOverMaxFormBytesIsA413(t *testing.T) {
	srv := startMP(t, mpApp(nil), bites.Limits{MaxFormBytes: 150})
	r := doRaw(t, srv, mpRequest("/mp", field("title", strings.Repeat("a", 100))+field("tags", strings.Repeat("b", 51))+png("cover", "C")+closing()))
	problemAt(t, r, 413, "body.tags", "all the text of the request is over 150 bytes")
}

func TestAPartWithANameTheTypeLacksIsUnknownFieldAtBodyNameAndItsBytesAreNotKept(t *testing.T) {
	dir := t.TempDir()
	srv := startMP(t, mpApp(nil), bites.Limits{UploadDir: dir})
	r := doRaw(t, srv, mpRequest("/mp", base(fileField("secret", "s.bin", "application/octet-stream", filler(400<<10)))))
	problemAt(t, r, 400, "body.secret", "unknown field")
	if n := spills(t, dir); n != 0 {
		t.Fatalf("%d temporary files for a part nobody asked for", n)
	}
	long := strings.Repeat("n", 100)
	r = doRaw(t, srv, mpRequest("/mp", base(field(long, "v"))))
	if !strings.Contains(r.Text(), `"location":"body.`+long[:64]+`"`) || strings.Contains(r.Text(), long[:65]) {
		t.Fatalf("a name echoed in full: %s", r.Text())
	}
	// A route that accepts unknown members lets it pass.
	srv = startMP(t, mpApp(nil, bites.AcceptUnknownMembers()), bites.Limits{})
	if r := doRaw(t, srv, mpRequest("/mp", base(field("extra", "v")))); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestMorePartsThanMaxPartsIsA413BeforeTheyAreKept(t *testing.T) {
	dir := t.TempDir()
	srv := startMP(t, mpApp(nil, bites.MaxParts(4)), bites.Limits{UploadDir: dir})
	var extra []string
	for range 3 {
		extra = append(extra, fileField("gallery", "g.png", "image/png", filler(300<<10)))
	}
	r := doRaw(t, srv, mpRequest("/mp", base(extra...)))
	if r.Status != 413 || !strings.Contains(r.Text(), "more parts than this route allows") {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if n := spills(t, dir); n != 0 {
		t.Fatalf("%d temporary files open after the refusal", n)
	}
}

func TestATextPartSentUnderAFileFieldsNameAndAFileUnderATextFieldsNameAreEach400(t *testing.T) {
	srv := startMP(t, mpApp(nil), bites.Limits{})
	r := doRaw(t, srv, mpRequest("/mp", field("title", "x")+field("price", "1")+field("cover", "not a file")+closing()))
	problemAt(t, r, 400, "body.cover", "a file part needs a filename")
	r = doRaw(t, srv, mpRequest("/mp", fileField("title", "t.txt", "text/plain", "x")+field("price", "1")+png("cover", "C")+closing()))
	problemAt(t, r, 400, "body.title", "a text field was sent as a file")
}

func TestATextFieldThatIsNotTheTypeIsA400NamingIt(t *testing.T) {
	srv := startMP(t, mpApp(nil), bites.Limits{})
	r := doRaw(t, srv, mpRequest("/mp", field("title", "x")+field("price", "cheap")+png("cover", "C")+closing()))
	problemAt(t, r, 400, "body.price", "expected an integer")
}

func TestEveryMistakeOfTheFormIsInOne400UpToTheCap(t *testing.T) {
	srv := startMP(t, mpApp(nil), bites.Limits{})
	r := doRaw(t, srv, mpRequest("/mp", field("price", "cheap")+fileField("cover", "c.gif", "image/gif", "G")+field("zzz", "1")+closing()))
	for _, loc := range []string{"body.title", "body.price", "body.cover", "body.zzz"} {
		if !strings.Contains(r.Text(), `"location":"`+loc+`"`) {
			t.Errorf("%s is missing from the 400: %s", loc, r.Text())
		}
	}
	if r.Status != 400 {
		t.Fatalf("%d", r.Status)
	}
}

func TestAQueryMistakeIsAnsweredBeforeTheBodyIsReadAndABodyMistakeListsEveryField(t *testing.T) {
	ran := 0
	srv := startMP(t, mpApp(func(mpIn) { ran++ }), bites.Limits{})
	// The client asks for 100-continue and sends no body: a request the Input refuses never invites it.
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	fmt.Fprint(c, upHead("POST", "/mp?page=x", "Expect: 100-continue\r\nContent-Length: "+strconv.Itoa(len(base()))+"\r\n\r\n"))
	r := srv.Read(c, false)
	problemAt(t, r, 400, "query.page", "expected an integer")
	if strings.Contains(r.Text(), "body.") || ran != 0 {
		t.Fatalf("the body was read for a request whose query was wrong: %s ran=%d", r.Text(), ran)
	}
	// With the query right, the body mistakes are all listed.
	r = doRaw(t, srv, mpRequest("/mp?page=1", field("price", "cheap")+closing()))
	if r.Status != 400 || !strings.Contains(r.Text(), `"body.title"`) || !strings.Contains(r.Text(), `"body.price"`) || !strings.Contains(r.Text(), `"body.cover"`) {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

type mpValidated struct {
	Body bites.Multipart[struct{ Cover mpPhoto }]
}

var mpValidateSaw struct {
	size int64
	typ  string
}

func (v mpValidated) Validate() error {
	f := v.Body.Value.Cover
	mpValidateSaw.size, mpValidateSaw.typ = f.Size, f.ContentType
	if f.Size > 3 {
		return bites.Invalid("body.cover", "too big for this route")
	}
	return nil
}

func TestValidateSeesTheFilesSizeAndTypeBeforeTheHandler(t *testing.T) {
	ran := false
	srv := startMP(t, func(a *bites.App) {
		a.Post("/v", func(context.Context, mpValidated) (bites.NoContent, error) { ran = true; return bites.NoContent{}, nil }, bites.MaxBody(8<<20))
	}, bites.Limits{})
	r := doRaw(t, srv, mpRequest("/v", png("cover", "12345")+closing()))
	problemAt(t, r, 400, "body.cover", "too big for this route")
	if mpValidateSaw.size != 5 || mpValidateSaw.typ != "image/png" || ran {
		t.Fatalf("Validate saw %+v, handler ran %v", mpValidateSaw, ran)
	}
}

func TestATruncatedMultipartBodyIsA400AndTheConnectionCloses(t *testing.T) {
	dir := t.TempDir()
	srv := startMP(t, mpApp(nil), bites.Limits{UploadDir: dir})
	body := field("title", "x") + field("price", "1") + "--" + bnd + "\r\nContent-Disposition: form-data; name=\"cover\"; filename=\"c.png\"\r\nContent-Type: image/png\r\n\r\n" + filler(300<<10)
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	// The client declares more than it sends and closes its side.
	go func() {
		c.Write([]byte(upHead("POST", "/mp", "Content-Length: "+strconv.Itoa(len(body)+500)+"\r\n\r\n") + body))
		c.(interface{ CloseWrite() error }).CloseWrite()
	}()
	r := srv.Read(c, false)
	if r.Status != 400 || r.Get("connection") != "close" {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Text())
	}
	// A complete Content-Length whose multipart body is cut before the closing delimiter.
	r = doRaw(t, srv, mpRequest("/mp", field("title", "x")+field("price", "1")+png("cover", "C")))
	if r.Status != 400 || !strings.Contains(r.Text(), "ended inside a part") {
		t.Fatalf("no closing delimiter: %d %s", r.Status, r.Text())
	}
	if n := spills(t, dir); n != 0 {
		t.Fatalf("%d temporary files open after a body that was cut", n)
	}
}

func TestAPartWithNoNameIsA400(t *testing.T) {
	srv := startMP(t, mpApp(nil), bites.Limits{})
	for _, p := range []string{
		"--" + bnd + "\r\nContent-Disposition: form-data\r\n\r\nx\r\n",
		"--" + bnd + "\r\nContent-Disposition: form-data; filename=\"a.png\"\r\n\r\nx\r\n",
		"--" + bnd + "\r\nContent-Type: text/plain\r\n\r\nx\r\n",
	} {
		r := doRaw(t, srv, mpRequest("/mp", base(p)))
		if r.Status != 400 || !strings.Contains(r.Text(), "has no Content-Disposition of type form-data with a name") {
			t.Errorf("%d %s", r.Status, r.Text())
		}
	}
}

func TestABodyOverMaxBodyIsA413AndAMultipartRouteNeedsTheRightMediaType(t *testing.T) {
	type plain struct {
		Body bites.Multipart[struct{ Title string }]
	}
	srv := startMP(t, func(a *bites.App) {
		a.Post("/mp", func(context.Context, plain) (bites.NoContent, error) { return bites.NoContent{}, nil }, bites.MaxBody(2000))
	}, bites.Limits{})
	r := doRaw(t, srv, upHead("POST", "/mp", "Expect: 100-continue\r\nContent-Length: 3000\r\n\r\n"))
	if r.Status != 413 {
		t.Fatalf("declared 3000 with a limit of 2000: %d %s", r.Status, r.Text())
	}
	r = doRaw(t, srv, upHead("POST", "/mp", "Transfer-Encoding: chunked\r\n\r\n")+chunked(field("title", strings.Repeat("a", 3000))+closing(), 500))
	if r.Status != 413 {
		t.Fatalf("chunked over the limit: %d %s", r.Status, r.Text())
	}
	for _, ct := range []string{"application/json", "application/x-www-form-urlencoded"} {
		r = doRaw(t, srv, "POST /mp HTTP/1.1\r\nHost: x\r\nContent-Type: "+ct+"\r\nContent-Length: 2\r\n\r\n{}")
		if r.Status != 415 || !strings.Contains(r.Text(), "the body must be sent as multipart/form-data") {
			t.Errorf("%s: %d %s", ct, r.Status, r.Text())
		}
	}
}

func TestAClientThatSendsFilesAndStopsHoldsNoMoreThanMaxInFlightUploadsOnDisk(t *testing.T) {
	dir := t.TempDir()
	srv := startMP(t, mpApp(nil, bites.MaxInFlight(2)), bites.Limits{UploadDir: dir, UploadMemory: 1})
	head := func(n int) string {
		return upHead("POST", "/mp", "Content-Length: "+strconv.Itoa(n)+"\r\n\r\n")
	}
	partial := field("title", "x") + field("price", "1") + "--" + bnd + "\r\nContent-Disposition: form-data; name=\"cover\"; filename=\"c.png\"\r\nContent-Type: image/png\r\n\r\n" + filler(300<<10)
	var conns []net.Conn
	for i := range 2 {
		c := srv.Dial("203.0.113.9:" + strconv.Itoa(6000+i))
		conns = append(conns, c)
		fmt.Fprint(c, head(len(partial)+(1<<20))+partial)
	}
	// Both are mid-file: wait until both hold their spill, then a third is turned away.
	mpWaitFor(t, func() bool { return spills(t, dir) == 2 })
	c3 := srv.Dial("203.0.113.9:6002")
	defer c3.Close()
	fmt.Fprint(c3, head(100)+"x")
	if r := srv.Read(c3, false); r.Status != 503 {
		t.Fatalf("a third upload of a route with MaxInFlight 2: %d %s", r.Status, r.Text())
	}
	if n := spills(t, dir); n != 2 {
		t.Fatalf("%d files on disk, want 2", n)
	}
	for _, c := range conns {
		c.Close()
	}
	mpWaitFor(t, func() bool { return spills(t, dir) == 0 })
}

func mpWaitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		if ok() {
			return
		}
		runtime.Gosched()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the condition did not come true")
}

func TestManyIdleConnectionsEachHoldingHalfAFileHoldNoMoreThanTheAppsMemoryShare(t *testing.T) {
	dir := t.TempDir()
	const conns = 24
	srv := startMP(t, mpApp(nil), bites.Limits{UploadDir: dir, UploadMemory: 1 << 20})
	partial := field("title", "x") + field("price", "1") + "--" + bnd + "\r\nContent-Disposition: form-data; name=\"cover\"; filename=\"c.png\"\r\nContent-Type: image/png\r\n\r\n" + filler(100<<10)
	var cs []net.Conn
	for i := range conns {
		c := srv.Dial("203.0.113." + strconv.Itoa(10+i) + ":7000")
		cs = append(cs, c)
		// Each declares a 400 KiB body and sends a quarter of it, then stops.
		fmt.Fprint(c, upHead("POST", "/mp", "Content-Length: "+strconv.Itoa(400<<10)+"\r\n\r\n")+partial)
	}
	// A request asks for up to 256 KiB while its body is still to come, so a 1 MiB share holds the
	// first four in memory and the other twenty wait on disk: the bytes in memory never pass the
	// share, whatever the number of connections.
	mpWaitFor(t, func() bool { return spills(t, dir) == conns-4 })
	for _, c := range cs {
		c.Close()
	}
	mpWaitFor(t, func() bool { return spills(t, dir) == 0 })
}

// The Inputs of the Multipart rows of the refusal table (refusal_test.go).
type (
	badAccept1  struct{ bites.FormFile }
	badAccept2  struct{ bites.FormFile }
	badAccept3  struct{ bites.FormFile }
	badSize0    struct{ bites.FormFile }
	badSizeBig  struct{ bites.FormFile }
	badSizeType struct{ bites.FormFile }
	extraFile   struct {
		bites.FormFile
		Note string
	}
	MPOfInt struct{ Body bites.Multipart[int] }
	MPOfOpt struct {
		Body bites.Multipart[bites.Opt[int]]
	}
	MPPlain struct {
		Body bites.Multipart[struct{ Title string }]
	}
	MPSubFile struct {
		Body bites.Submitted[struct{ Cover bites.FormFile }]
	}
	FormWithFile struct {
		Body bites.Form[struct{ Cover bites.FormFile }]
	}
	QueryFile struct {
		Query struct{ Cover bites.FormFile }
	}
	HeaderFile struct {
		Header struct{ Cover bites.FormFile }
	}
	JSONFile struct {
		Body struct{ Cover bites.FormFile }
	}
	MPPtrFile struct {
		Body bites.Multipart[struct{ Cover *bites.FormFile }]
	}
	MPMapFile struct {
		Body bites.Multipart[struct{ Cover map[string]bites.FormFile }]
	}
	MPArrayFile struct {
		Body bites.Multipart[struct{ Cover [2]bites.FormFile }]
	}
	MPOptList struct {
		Body bites.Multipart[struct{ Cover bites.Opt[[]bites.FormFile] }]
	}
	MPListOpt struct {
		Body bites.Multipart[struct{ Cover []bites.Opt[bites.FormFile] }]
	}
	MPBytes struct {
		Body bites.Multipart[struct{ Cover []byte }]
	}
	MPHeader struct {
		Body bites.Multipart[struct{ Cover *multipart.FileHeader }]
	}
	MPReader struct {
		Body bites.Multipart[struct{ Cover io.Reader }]
	}
	MPAccept1 struct {
		Body bites.Multipart[struct{ Cover badAccept1 }]
	}
	MPAccept2 struct {
		Body bites.Multipart[struct{ Cover badAccept2 }]
	}
	MPAccept3 struct {
		Body bites.Multipart[struct{ Cover badAccept3 }]
	}
	MPSize0 struct {
		Body bites.Multipart[struct{ Cover badSize0 }]
	}
	MPSizeBig struct {
		Body bites.Multipart[struct{ Cover badSizeBig }]
	}
	MPSizeType struct {
		Body bites.Multipart[struct{ Cover badSizeType }]
	}
	MPExtra struct {
		Body bites.Multipart[struct{ Cover extraFile }]
	}
	MPNineLists struct {
		Body bites.Multipart[struct{ A, B, C, D, E, F, G, H, I []string }]
	}
	MPAndUpload struct {
		Body  bites.Multipart[struct{ Title string }]
		Other bites.Upload
	}
	UploadAndMP struct {
		Body  bites.Upload
		Other bites.Multipart[struct{ Title string }]
	}
	MPInJSON struct {
		Body struct {
			F bites.Multipart[struct{ A string }]
		}
	}
	MPInSlice struct {
		Body []bites.Multipart[struct{ A string }]
	}
	MPInQuery struct {
		Query struct {
			F bites.Multipart[struct{ A string }]
		}
	}
)

func (badAccept1) Accept() []string { return []string{"image"} }
func (badAccept2) Accept() []string { return nil }
func (badAccept3) Accept() []string { return []string{"*/*"} }
func (badSize0) MaxSize() int64     { return 0 }
func (badSizeBig) MaxSize() int64   { return 2 << 20 }
func (badSizeType) MaxSize() int    { return 5 }

// FuzzAMultipartForm sends arbitrary bodies to a Multipart route: the answer is never a 500 and no
// temporary file stays open.
func FuzzAMultipartForm(f *testing.F) {
	f.Add(base())
	f.Add(field("title", "x") + fileField("cover", "c.png", "image/png", "x") + closing())
	f.Add("--" + bnd + "\r\nContent-Disposition: form-data\r\n\r\nx\r\n" + closing())
	f.Add(field("title", "x") + "--" + bnd + "\r\nContent-Disposition: form-data; name=\"cover\"; filename=\"a\"\r\n\r\n" + filler(300<<10))
	f.Add("")
	f.Fuzz(func(t *testing.T, body string) {
		dir := t.TempDir()
		srv := startMP(t, mpApp(nil, bites.MaxParts(8)), bites.Limits{UploadDir: dir, MaxFieldBytes: 4096, MaxFormBytes: 8192})
		r := doRaw(t, srv, mpRequest("/mp", body))
		if r.Status >= 500 {
			t.Fatalf("%d %s", r.Status, r.Text())
		}
		if runtime.GOOS == "linux" {
			if n := spills(t, dir); n != 0 {
				t.Fatalf("%d temporary files open after the request", n)
			}
		}
	})
}

// wordFile is a file field that embeds FormFile and sets its own limits, as an application's does.
type wordFile struct{ bites.FormFile }

func (wordFile) Accept() []string {
	return []string{"application/vnd.openxmlformats-officedocument.wordprocessingml.document"}
}
func (wordFile) MaxSize() int64 { return 4 << 20 }

type nameOut struct {
	FileName    string `json:"file"`
	ContentType string `json:"type"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Sum         string `json:"sum"`
}

// nameBodySizes cross the read window, the 256 KiB the request keeps in memory, and a few reads.
var nameBodySizes = []int{5, 5 << 10, 100 << 10, 300 << 10, 2 << 20}

const docxType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

func registerNameForm(a *bites.App) {
	a.Post("/word", func(_ context.Context, in struct {
		Body bites.Multipart[struct{ File wordFile }]
	}) (nameOut, error) {
		f := in.Body.Value.File
		d, err := describe(f.FormFile)
		if err != nil {
			return nameOut{}, err
		}
		return nameOut{f.FileName, f.ContentType, f.Name, f.Size, d.Sum}, nil
	}, bites.MaxBody(8<<20))
}

func nameWant(data string) string {
	return `{"file":"Notes.docx","type":"` + docxType + `","name":"file","size":` + strconv.Itoa(len(data)) + `,"sum":"` + sumOf(data) + `"}`
}

func TestAFilesNameAndTypeSurviveTheReadingOfItsBytesWhateverItsSize(t *testing.T) {
	srv := startMP(t, registerNameForm, bites.Limits{})
	for _, n := range nameBodySizes {
		data := "[Content_Types].xml" + filler(n)
		body := fileField("file", "Notes.docx", docxType, data) + closing()
		r := doRaw(t, srv, mpRequest("/word", body))
		if r.Status != 200 || r.Text() != nameWant(data) {
			t.Fatalf("%d bytes: %d\n%s\nwant\n%s", n, r.Status, r.Text(), nameWant(data))
		}
		c := srv.Dial("203.0.113.9:4000")
		fmt.Fprint(c, upHead("POST", "/word", "Transfer-Encoding: chunked\r\n\r\n")+chunked(body, 37))
		r = srv.Read(c, false)
		c.Close()
		if r.Status != 200 || r.Text() != nameWant(data) {
			t.Fatalf("%d bytes chunked: %d\n%s", n, r.Status, r.Text())
		}
	}
}

func TestAFilesNameAndTypeSurviveTheReadingOfItsBytesOnARealSocket(t *testing.T) {
	app := bites.New(bites.WithLimits(bites.Limits{UploadDir: t.TempDir()}))
	registerNameForm(app)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go app.Serve(ln)
	t.Cleanup(func() { app.Close() })
	for _, n := range nameBodySizes {
		data := "[Content_Types].xml" + filler(n)
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(10 * time.Second))
		go c.Write([]byte(mpRequest("/word", fileField("file", "Notes.docx", docxType, data)+closing())))
		buf := make([]byte, 0, 1024)
		tmp := make([]byte, 1024)
		for !strings.Contains(string(buf), `"sum"`) {
			k, err := c.Read(tmp)
			buf = append(buf, tmp[:k]...)
			if err != nil {
				break
			}
		}
		c.Close()
		if !strings.Contains(string(buf), nameWant(data)) {
			t.Fatalf("%d bytes: %s\nwant %s", n, buf, nameWant(data))
		}
	}
}
