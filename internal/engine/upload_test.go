package engine

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/reqctx"
)

// fileBytes is a body larger than the first read, whose every byte differs from its neighbours,
// so a byte lost from the middle cannot go unnoticed.
func fileBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

// chunk encodes b as one chunk and the last chunk.
func chunk(b []byte) []byte {
	out := []byte(strconv.FormatInt(int64(len(b)), 16) + "\r\n")
	out = append(out, b...)
	return append(out, "\r\n0\r\n\r\n"...)
}

// readReply reads one answer of a plain-TCP client: its status and body (Content-Length only).
func readReply(t *testing.T, br *bufio.Reader) (int, string) {
	t.Helper()
	status, n := 0, 0
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading the answer: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if status == 0 {
			status, _ = strconv.Atoi(strings.Fields(line)[1])
		} else if k, v, ok := strings.Cut(line, ": "); ok && strings.EqualFold(k, "Content-Length") {
			n, _ = strconv.Atoi(v)
		}
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(br, b); err != nil {
		t.Fatalf("reading the answer's body: %v", err)
	}
	return status, string(b)
}

// sum answers with the length of what was read and whether it is want.
func sum(ex *Exchange, got, want []byte, err error) {
	r := ex.Respond(200)
	*r.Body() = fmt.Appendf(*r.Body(), "%d %v %v", len(got), bytes.Equal(got, want), err)
}

// readRaw reads a BodyReader's body to its end.
func readRaw(m *Multipart) ([]byte, error) {
	var out []byte
	buf := make([]byte, 1000)
	for {
		n, err := m.ReadRaw(buf)
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// readParts reads every part of an upload and returns the bytes of the first.
func readParts(m *Multipart) ([]byte, error) {
	var first []byte
	buf := make([]byte, 1000)
	for i := 0; ; i++ {
		p, err := m.Next()
		if err == io.EOF {
			return first, nil
		}
		if err != nil {
			return first, err
		}
		for {
			n, err := m.ReadPart(p.Gen, buf)
			if i == 0 {
				first = append(first, buf[:n]...)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return first, err
			}
		}
	}
}

const boundary = "XyZ"

func multipartBody(file []byte) []byte {
	var b bytes.Buffer
	b.WriteString("--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.mp4\"\r\nContent-Type: video/mp4\r\n\r\n")
	b.Write(file)
	b.WriteString("\r\n--" + boundary + "--\r\n")
	return b.Bytes()
}

// The case Athena found (docs/bites-feedback.md there): a Provider looks at the context, which
// arms the watch, and then the body is read. With the reader-goroutine fallback, the goroutine
// used to take the body's next byte, so the file lost a byte and the read waited for it.
func TestWithTheReaderFallbackABodyReadAfterTheContextWasLookedAtLosesNoByte(t *testing.T) {
	file := fileBytes(48393)
	upload := func(read func(*Multipart) ([]byte, error), want []byte, raw bool) handlerFunc {
		return func(ex *Exchange) {
			ex.Context().Done() // what a Provider that queries a database does
			time.Sleep(30 * time.Millisecond)
			o := UploadOptions{Max: 1 << 20, Timeout: 5 * time.Second, MaxParts: 4, Boundary: []byte(boundary)}
			var m *Multipart
			var err error
			if raw {
				m, err = ex.OpenBody(o)
			} else {
				m, err = ex.OpenMultipart(o)
			}
			if err != nil {
				ex.Respond(500)
				return
			}
			got, err := read(m)
			sum(ex, got, want, err)
		}
	}
	buffered := func(want []byte) handlerFunc {
		return func(ex *Exchange) {
			ex.Context().Done()
			time.Sleep(30 * time.Millisecond)
			got, err := ex.ReadBody(1<<20, 5*time.Second)
			sum(ex, got, want, err)
		}
	}
	mp := multipartBody(file)
	for _, tc := range []struct {
		name    string
		h       handlerFunc
		ctype   string
		body    []byte
		wantLen int
	}{
		{"a BodyReader", upload(readRaw, file, true), "application/octet-stream", file, len(file)},
		{"an Upload", upload(readParts, file, false), "multipart/form-data; boundary=" + boundary, mp, len(file)},
		{"a buffered body", buffered(file), "application/octet-stream", file, len(file)},
	} {
		for _, chunked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, chunked %v", tc.name, chunked), func(t *testing.T) {
				_, c := tcpPair(t, true, func(ex *Exchange) {
					if ex.Head().Method[0] == 'G' {
						okHandler("after")(ex)
						return
					}
					tc.h(ex)
				})
				head := "POST /u HTTP/1.1\r\nHost: x\r\nContent-Type: " + tc.ctype + "\r\n"
				body := tc.body
				if chunked {
					head += "Transfer-Encoding: chunked\r\n\r\n"
					body = chunk(body)
				} else {
					head += "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n"
				}
				c.Write([]byte(head))
				time.Sleep(60 * time.Millisecond) // the handler has looked at its context, and its watch is waiting
				c.Write(body)
				c.Write([]byte(get))
				c.SetReadDeadline(time.Now().Add(5 * time.Second))
				br := bufio.NewReader(c)
				status, got := readReply(t, br)
				if want := fmt.Sprintf("%d true <nil>", tc.wantLen); status != 200 || got != want {
					t.Fatalf("got %d %q, want 200 %q", status, got, want)
				}
				if status, _ := readReply(t, br); status != 200 {
					t.Fatalf("the request after the body: status %d", status)
				}
			})
		}
	}
}

func TestWithTheReaderFallbackAContextLookedAtFromAnotherGoroutineDuringAnUploadLosesNoByte(t *testing.T) {
	file := fileBytes(200000)
	_, c := tcpPair(t, true, func(ex *Exchange) {
		m, err := ex.OpenBody(UploadOptions{Max: 1 << 20, Timeout: 5 * time.Second})
		if err != nil {
			ex.Respond(500)
			return
		}
		buf := make([]byte, 1000)
		n, err := m.ReadRaw(buf)
		got := append([]byte(nil), buf[:n]...)
		looked := make(chan struct{})
		go func() { ex.Context().Done(); close(looked) }()
		<-looked
		rest, err2 := readRaw(m)
		if err == nil {
			err = err2
		}
		sum(ex, append(got, rest...), file, err)
	})
	c.Write([]byte("POST /u HTTP/1.1\r\nHost: x\r\nContent-Length: " + strconv.Itoa(len(file)) + "\r\n\r\n"))
	c.Write(file[:5000])
	time.Sleep(60 * time.Millisecond)
	c.Write(file[5000:])
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if status, got := readReply(t, bufio.NewReader(c)); status != 200 || got != fmt.Sprintf("%d true <nil>", len(file)) {
		t.Fatalf("got %d %q", status, got)
	}
}

func TestWithTheReaderFallbackAClientThatLeavesMidUploadCancelsTheContextItLookedAt(t *testing.T) {
	cause := make(chan error, 1)
	_, c := tcpPair(t, true, func(ex *Exchange) {
		ctx := ex.Context()
		ctx.Done()
		m, err := ex.OpenBody(UploadOptions{Max: 1 << 20, Timeout: 5 * time.Second})
		if err != nil {
			ex.Respond(500)
			return
		}
		_, err = readRaw(m)
		var he *h1.Error
		if !errors.As(err, &he) {
			cause <- fmt.Errorf("the read ended with %v, want a refusal", err)
			return
		}
		select {
		case <-ctx.Done():
			cause <- ctx.Cause()
		case <-time.After(5 * time.Second):
			cause <- errors.New("not cancelled")
		}
		ex.Respond(204)
	})
	c.Write([]byte("POST /u HTTP/1.1\r\nHost: x\r\nContent-Length: 100000\r\n\r\n"))
	c.Write(fileBytes(10000))
	time.Sleep(30 * time.Millisecond)
	c.Close()
	if err := <-cause; err != reqctx.ErrClientGone {
		t.Fatalf("cause = %v", err)
	}
}

func TestWithTheReaderFallbackAContextLookedAtDuringAnUploadIsWatchedOnceTheBodyIsOver(t *testing.T) {
	cause := make(chan error, 1)
	read := make(chan struct{})
	_, c := tcpPair(t, true, func(ex *Exchange) {
		ctx := ex.Context()
		ctx.Done()
		m, err := ex.OpenBody(UploadOptions{Max: 1 << 20, Timeout: 5 * time.Second})
		if err == nil {
			_, err = readRaw(m)
		}
		if err != nil {
			cause <- err
			return
		}
		close(read)
		select {
		case <-ctx.Done():
			cause <- ctx.Cause()
		case <-time.After(5 * time.Second):
			cause <- errors.New("not cancelled")
		}
		ex.Respond(204)
	})
	file := fileBytes(30000)
	c.Write([]byte("POST /u HTTP/1.1\r\nHost: x\r\nContent-Length: " + strconv.Itoa(len(file)) + "\r\n\r\n"))
	time.Sleep(30 * time.Millisecond)
	c.Write(file)
	select {
	case <-read:
	case err := <-cause:
		t.Fatal(err)
	}
	c.Close()
	if err := <-cause; err != reqctx.ErrClientGone {
		t.Fatalf("cause = %v", err)
	}
}

func TestWithTheReaderFallbackAStreamAfterAnUploadOutlivesTheBodysDeadlineWithItsContextLive(t *testing.T) {
	file := fileBytes(20000)
	_, c := tcpPair(t, true, func(ex *Exchange) {
		ctx := ex.Context()
		m, err := ex.OpenMultipart(UploadOptions{Max: 1 << 20, Timeout: 100 * time.Millisecond, MaxParts: 4, Boundary: []byte(boundary)})
		if err == nil {
			_, err = readParts(m)
		}
		if err != nil {
			ex.Respond(500)
			return
		}
		ctx.Done() // the watch starts now, the body being over
		r := ex.Respond(200)
		r.Header("Content-Type", "text/plain")
		ex.Stream(false, fnRunner{run: func(w *StreamWriter) error {
			time.Sleep(300 * time.Millisecond) // past the body's deadline
			_, err := fmt.Fprintf(w, "cause %v", ctx.Cause())
			return err
		}})
	})
	body := multipartBody(file)
	c.Write([]byte("POST /u HTTP/1.1\r\nHost: x\r\nContent-Type: multipart/form-data; boundary=" + boundary + "\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n"))
	c.Write(body)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var all strings.Builder
	buf := make([]byte, 4096)
	for !strings.HasSuffix(all.String(), "0\r\n\r\n") {
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("%v; got %q", err, all.String())
		}
		all.Write(buf[:n])
	}
	if !strings.Contains(all.String(), "cause <nil>") {
		t.Fatalf("got %q", all.String())
	}
}
