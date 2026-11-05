package h1

import (
	"errors"
	"strings"
	"testing"
)

func parse(t *testing.T, head string) (*Request, int, error) {
	t.Helper()
	var req Request
	n, err := ParseHead([]byte(head), &req, DefaultLimits())
	return &req, n, err
}

func wantRefusal(t *testing.T, head string, status int, closeConn bool) {
	t.Helper()
	_, _, err := parse(t, head)
	var he *Error
	if !errors.As(err, &he) {
		t.Fatalf("want *Error for %q, got %v", head, err)
	}
	if he.Status != status || he.Close != closeConn {
		t.Fatalf("%q: got status %d close %v (%s), want %d close %v", head, he.Status, he.Close, he.Reason, status, closeConn)
	}
}

const okHost = "Host: example.com\r\n"

func TestParseHead(t *testing.T) {
	t.Run("a plain GET is read into its parts", func(t *testing.T) {
		head := "GET /a/b?x=1&y=2 HTTP/1.1\r\nHost: example.com\r\nUser-Agent:  curl \t\r\n\r\n"
		req, n, err := parse(t, head)
		if err != nil || n != len(head) {
			t.Fatalf("n=%d err=%v", n, err)
		}
		if string(req.Method) != "GET" || string(req.Path) != "/a/b" || string(req.RawQuery) != "x=1&y=2" ||
			string(req.Host) != "example.com" || req.Minor != 1 || req.ContentLength != -1 || req.Chunked || !req.KeepAlive {
			t.Fatalf("%+v", req)
		}
		if len(req.Headers) != 2 || string(req.Headers[1].Name) != "User-Agent" || string(req.Headers[1].Value) != "curl" {
			t.Fatalf("headers %q", req.Headers)
		}
	})

	t.Run("a query is nil when there is no question mark and empty when there is one", func(t *testing.T) {
		req, _, _ := parse(t, "GET /a HTTP/1.1\r\n"+okHost+"\r\n")
		if req.RawQuery != nil {
			t.Fatal("want nil")
		}
		req, _, _ = parse(t, "GET /a? HTTP/1.1\r\n"+okHost+"\r\n")
		if req.RawQuery == nil || len(req.RawQuery) != 0 || string(req.Path) != "/a" {
			t.Fatalf("%q %q", req.Path, req.RawQuery)
		}
	})

	t.Run("the path is kept raw, not percent-decoded", func(t *testing.T) {
		req, _, _ := parse(t, "GET /a%2Fb%20c HTTP/1.1\r\n"+okHost+"\r\n")
		if string(req.Path) != "/a%2Fb%20c" {
			t.Fatalf("%q", req.Path)
		}
	})

	t.Run("both Transfer-Encoding and Content-Length are refused and the connection closed", func(t *testing.T) {
		wantRefusal(t, "POST / HTTP/1.1\r\n"+okHost+"Transfer-Encoding: chunked\r\nContent-Length: 4\r\n\r\n", 400, true)
		wantRefusal(t, "POST / HTTP/1.1\r\n"+okHost+"Content-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n", 400, true)
		wantRefusal(t, "POST / HTTP/1.1\r\n"+okHost+"Content-Length: 4\r\nTransfer-Encoding: gzip\r\n\r\n", 400, true)
	})

	t.Run("duplicate Content-Length is refused even when the values are equal", func(t *testing.T) {
		wantRefusal(t, "POST / HTTP/1.1\r\n"+okHost+"Content-Length: 4\r\nContent-Length: 4\r\n\r\n", 400, true)
		wantRefusal(t, "POST / HTTP/1.1\r\n"+okHost+"Content-Length: 4\r\ncontent-length: 5\r\n\r\n", 400, true)
	})

	t.Run("a Transfer-Encoding other than a lone chunked is a 501 and the connection closed", func(t *testing.T) {
		for _, v := range []string{"gzip", "gzip, chunked", "chunked, identity", "chunked, chunked", "identity", "", ",chunked", "chunked,", "xchunked", "chunked;x=1"} {
			wantRefusal(t, "POST / HTTP/1.1\r\n"+okHost+"Transfer-Encoding: "+v+"\r\n\r\n", 501, true)
		}
		wantRefusal(t, "POST / HTTP/1.1\r\n"+okHost+"Transfer-Encoding: chunked\r\nTransfer-Encoding: chunked\r\n\r\n", 501, true)
	})

	t.Run("Transfer-Encoding: chunked is recognised whatever its case", func(t *testing.T) {
		req, _, err := parse(t, "POST / HTTP/1.1\r\n"+okHost+"transfer-ENCODING:  Chunked \r\n\r\n")
		if err != nil || !req.Chunked || req.ContentLength != -1 {
			t.Fatalf("%v %+v", err, req)
		}
	})

	t.Run("Transfer-Encoding in an HTTP/1.0 request is refused", func(t *testing.T) {
		wantRefusal(t, "POST / HTTP/1.0\r\nTransfer-Encoding: chunked\r\n\r\n", 400, true)
	})

	t.Run("obs-fold is refused and the connection closed", func(t *testing.T) {
		wantRefusal(t, "GET / HTTP/1.1\r\n"+okHost+"X-A: one\r\n two\r\n\r\n", 400, true)
		wantRefusal(t, "GET / HTTP/1.1\r\n"+okHost+"X-A: one\r\n\ttwo\r\n\r\n", 400, true)
		wantRefusal(t, "GET / HTTP/1.1\r\n Host: a\r\n\r\n", 400, true)
	})

	t.Run("a bare CR is refused and the connection closed", func(t *testing.T) {
		wantRefusal(t, "GET / HTTP/1.1\r\n"+okHost+"X-A: one\rtwo\r\n\r\n", 400, true)
		wantRefusal(t, "GET / HTTP/1.1\r\r\n"+okHost+"\r\n", 400, true)
		wantRefusal(t, "GET /\r HTTP/1.1\r\n"+okHost+"\r\n", 400, true)
	})

	t.Run("a bare LF as a line end is refused and the connection closed", func(t *testing.T) {
		wantRefusal(t, "GET / HTTP/1.1\n"+okHost+"\r\n", 400, true)
		wantRefusal(t, "GET / HTTP/1.1\r\n"+"Host: a\nX: b\r\n\r\n", 400, true)
		wantRefusal(t, "GET / HTTP/1.1\r\n"+okHost+"\n", 400, true)
	})

	t.Run("a NUL is refused and the connection closed", func(t *testing.T) {
		wantRefusal(t, "GET / HTTP/1.1\r\nHost: a\x00b\r\n\r\n", 400, true)
		wantRefusal(t, "GET /\x00 HTTP/1.1\r\n"+okHost+"\r\n", 400, true)
		wantRefusal(t, "GET / HTTP/1.1\r\n"+okHost+"X\x00: b\r\n\r\n", 400, true)
	})

	t.Run("a space before the colon is refused and the connection closed", func(t *testing.T) {
		wantRefusal(t, "GET / HTTP/1.1\r\n"+okHost+"X-A : b\r\n\r\n", 400, true)
		wantRefusal(t, "POST / HTTP/1.1\r\n"+okHost+"Transfer-Encoding : chunked\r\n\r\n", 400, true)
		wantRefusal(t, "POST / HTTP/1.1\r\n"+okHost+"Content-Length\t: 3\r\n\r\n", 400, true)
	})

	t.Run("a field name that is not a token is refused and the connection closed", func(t *testing.T) {
		for _, name := range []string{"X(A)", "X A", "X/A", "X\"", "é", "X<>", "X@"} {
			wantRefusal(t, "GET / HTTP/1.1\r\n"+okHost+name+": v\r\n\r\n", 400, true)
		}
		wantRefusal(t, "GET / HTTP/1.1\r\n"+okHost+": v\r\n\r\n", 400, true)
		wantRefusal(t, "GET / HTTP/1.1\r\n"+okHost+"NoColon\r\n\r\n", 400, true)
	})

	t.Run("a control character in a value is refused", func(t *testing.T) {
		wantRefusal(t, "GET / HTTP/1.1\r\n"+okHost+"X: a\x01b\r\n\r\n", 400, true)
		wantRefusal(t, "GET / HTTP/1.1\r\n"+okHost+"X: a\x7fb\r\n\r\n", 400, true)
	})

	t.Run("a tab inside a value is kept", func(t *testing.T) {
		req, _, err := parse(t, "GET / HTTP/1.1\r\n"+okHost+"X: a\tb\r\n\r\n")
		if err != nil || string(req.Headers[1].Value) != "a\tb" {
			t.Fatalf("%v %q", err, req.Headers)
		}
	})

	t.Run("a missing Host in HTTP/1.1 is a 400 that closes the connection and still returns the head length", func(t *testing.T) {
		head := "POST / HTTP/1.1\r\nContent-Length: 3\r\n\r\n"
		req, n, err := parse(t, head)
		var he *Error
		if !errors.As(err, &he) || he.Status != 400 || !he.Close {
			t.Fatalf("%v", err)
		}
		if n != len(head) || req.ContentLength != 3 {
			t.Fatalf("n=%d cl=%d", n, req.ContentLength)
		}
	})

	t.Run("more than one Host is a 400 that closes the connection", func(t *testing.T) {
		wantRefusal(t, "GET / HTTP/1.1\r\nHost: a\r\nHost: a\r\n\r\n", 400, true)
		wantRefusal(t, "GET / HTTP/1.0\r\nHost: a\r\nhost: b\r\n\r\n", 400, true)
	})

	t.Run("a Host with a path or userinfo in it is refused", func(t *testing.T) {
		wantRefusal(t, "GET / HTTP/1.1\r\nHost: a/b\r\n\r\n", 400, true)
		wantRefusal(t, "GET / HTTP/1.1\r\nHost: u@a\r\n\r\n", 400, true)
		wantRefusal(t, "GET / HTTP/1.1\r\nHost: a b\r\n\r\n", 400, true)
	})

	t.Run("an empty Host is accepted in HTTP/1.1", func(t *testing.T) {
		req, _, err := parse(t, "GET / HTTP/1.1\r\nHost:\r\n\r\n")
		if err != nil || len(req.Host) != 0 {
			t.Fatal(err)
		}
	})

	t.Run("HTTP/1.0 needs no Host", func(t *testing.T) {
		req, _, err := parse(t, "GET / HTTP/1.0\r\n\r\n")
		if err != nil || req.Minor != 0 || req.Host != nil {
			t.Fatalf("%v %+v", err, req)
		}
	})

	t.Run("an absolute-form target is accepted and its host wins over the Host header", func(t *testing.T) {
		req, _, err := parse(t, "GET http://Real.example:8080/p/q?z=1 HTTP/1.1\r\nHost: other\r\n\r\n")
		if err != nil || string(req.Host) != "Real.example:8080" || string(req.Path) != "/p/q" || string(req.RawQuery) != "z=1" ||
			string(req.Target) != "http://Real.example:8080/p/q?z=1" {
			t.Fatalf("%v %q %q %q %q", err, req.Host, req.Path, req.RawQuery, req.Target)
		}
	})

	t.Run("an absolute-form target without a path has the path slash", func(t *testing.T) {
		for _, tg := range []string{"http://h", "HTTP://h?a=1"} {
			req, _, err := parse(t, "GET "+tg+" HTTP/1.1\r\nHost: h\r\n\r\n")
			if err != nil || string(req.Path) != "/" || string(req.Host) != "h" {
				t.Fatalf("%s: %v %q", tg, err, req.Path)
			}
		}
	})

	t.Run("an absolute-form target with userinfo, no host or another scheme is refused", func(t *testing.T) {
		for _, tg := range []string{"http://u@h/", "http:///x", "ftp://h/", "h/x", "http://h b/"} {
			wantRefusal(t, "GET "+tg+" HTTP/1.1\r\nHost: h\r\n\r\n", 400, true)
		}
	})

	t.Run("a percent sign in the Host header is a 400", func(t *testing.T) {
		for _, h := range []string{"a%41", "a%zz", "%C3%BCber.example", "[fe80::1%25lo]", "a%"} {
			wantRefusal(t, "GET / HTTP/1.1\r\nHost: "+h+"\r\n\r\n", 400, true)
		}
	})

	t.Run("a percent sign in an absolute-form host is a 400", func(t *testing.T) {
		for _, h := range []string{"a%41.example", "a%zz", "%C3%BCber.example", "[fe80::1%25lo]"} {
			wantRefusal(t, "GET http://"+h+"/p HTTP/1.1\r\nHost: ok\r\n\r\n", 400, true)
		}
		wantRefusal(t, "CONNECT a%41.example:443 HTTP/1.1\r\n\r\n", 400, true)
	})

	t.Run("an IPv6 literal host is still accepted", func(t *testing.T) {
		req, _, err := parse(t, "GET / HTTP/1.1\r\nHost: [::1]:8080\r\n\r\n")
		if err != nil || string(req.Host) != "[::1]:8080" {
			t.Fatalf("header: %v %q", err, req.Host)
		}
		req, _, err = parse(t, "GET http://[2001:db8::1]:80/p HTTP/1.1\r\nHost: x\r\n\r\n")
		if err != nil || string(req.Host) != "[2001:db8::1]:80" {
			t.Fatalf("target: %v %q", err, req.Host)
		}
	})

	t.Run("OPTIONS * is flagged and * with another method is refused", func(t *testing.T) {
		req, _, err := parse(t, "OPTIONS * HTTP/1.1\r\n"+okHost+"\r\n")
		if err != nil || !req.Asterisk || len(req.Path) != 0 {
			t.Fatalf("%v %+v", err, req)
		}
		wantRefusal(t, "GET * HTTP/1.1\r\n"+okHost+"\r\n", 400, true)
	})

	t.Run("CONNECT takes an authority-form target and needs no Host", func(t *testing.T) {
		req, _, err := parse(t, "CONNECT example.com:443 HTTP/1.1\r\n\r\n")
		if err != nil || string(req.Host) != "example.com:443" {
			t.Fatalf("%v", err)
		}
		wantRefusal(t, "CONNECT /x HTTP/1.1\r\n\r\n", 400, true)
	})

	t.Run("Expect: 100-continue sets the flag in HTTP/1.1 and is ignored in HTTP/1.0", func(t *testing.T) {
		req, _, err := parse(t, "POST / HTTP/1.1\r\n"+okHost+"Expect: 100-Continue\r\nContent-Length: 3\r\n\r\n")
		if err != nil || !req.Expect100 || req.ContentLength != 3 {
			t.Fatalf("%v %+v", err, req)
		}
		req, _, err = parse(t, "POST / HTTP/1.0\r\nExpect: 100-continue\r\n\r\n")
		if err != nil || req.Expect100 {
			t.Fatalf("%v %+v", err, req)
		}
	})

	t.Run("an unknown Expect is a 417", func(t *testing.T) {
		wantRefusal(t, "POST / HTTP/1.1\r\n"+okHost+"Expect: 200-ok\r\n\r\n", 417, true)
	})

	t.Run("keep-alive follows the version and the Connection header", func(t *testing.T) {
		cases := []struct {
			version, conn string
			want          bool
		}{
			{"1.1", "", true},
			{"1.1", "close", false},
			{"1.1", "Close", false},
			{"1.1", "keep-alive, close", false},
			{"1.1", "upgrade, keep-alive", true},
			{"1.0", "", false},
			{"1.0", "keep-alive", true},
			{"1.0", "Keep-Alive", true},
			{"1.0", "keep-alive, close", false},
			{"1.0", "close", false},
		}
		for _, c := range cases {
			h := "GET / HTTP/" + c.version + "\r\n" + okHost
			if c.conn != "" {
				h += "Connection: " + c.conn + "\r\n"
			}
			req, _, err := parse(t, h+"\r\n")
			if err != nil || req.KeepAlive != c.want {
				t.Fatalf("%q: keepalive %v err %v", h, req.KeepAlive, err)
			}
		}
		req, _, _ := parse(t, "GET / HTTP/1.1\r\n"+okHost+"Connection: a\r\nConnection: close\r\n\r\n")
		if req.KeepAlive {
			t.Fatal("a second Connection line must count")
		}
	})

	t.Run("the method must be a token", func(t *testing.T) {
		wantRefusal(t, "G(T / HTTP/1.1\r\n"+okHost+"\r\n", 400, true)
		wantRefusal(t, " GET / HTTP/1.1\r\n"+okHost+"\r\n", 400, true)
		wantRefusal(t, "\r\nGET / HTTP/1.1\r\n"+okHost+"\r\n", 400, true)
	})

	t.Run("a request line that is not three parts is refused", func(t *testing.T) {
		for _, l := range []string{"GET /", "GET", "GET  / HTTP/1.1", "GET / x HTTP/1.1", "GET /  HTTP/1.1"} {
			wantRefusal(t, l+"\r\n"+okHost+"\r\n", 400, true)
		}
	})

	t.Run("a target with a control character, a space or a fragment is refused", func(t *testing.T) {
		wantRefusal(t, "GET /a\tb HTTP/1.1\r\n"+okHost+"\r\n", 400, true)
		wantRefusal(t, "GET /a\x7f HTTP/1.1\r\n"+okHost+"\r\n", 400, true)
		wantRefusal(t, "GET /a#f HTTP/1.1\r\n"+okHost+"\r\n", 400, true)
	})

	t.Run("a version other than 1.0 or 1.1 is a 505 and a malformed one is a 400", func(t *testing.T) {
		for _, v := range []string{"HTTP/2.0", "HTTP/0.9", "HTTP/1.2", "HTTP/9.9"} {
			wantRefusal(t, "GET / "+v+"\r\n"+okHost+"\r\n", 505, true)
		}
		for _, v := range []string{"http/1.1", "HTTP/1", "HTTP/1.11", "HTTPS/1.1", "HTTP/a.1", "HTTP/1.1 "} {
			wantRefusal(t, "GET / "+v+"\r\n"+okHost+"\r\n", 400, true)
		}
	})

	t.Run("a head over the limit is a 431 whether or not it is complete", func(t *testing.T) {
		lim := Limits{MaxHead: 64, MaxFields: 100}
		var req Request
		long := "GET / HTTP/1.1\r\nHost: a\r\nX: " + strings.Repeat("v", 80) + "\r\n\r\n"
		for _, b := range []string{long, long[:64], long[:70]} {
			_, err := ParseHead([]byte(b), &req, lim)
			var he *Error
			if !errors.As(err, &he) || he.Status != 431 || !he.Close {
				t.Fatalf("%d bytes: %v", len(b), err)
			}
		}
		_, err := ParseHead([]byte(long[:63]), &req, lim)
		if err != ErrNeedMore {
			t.Fatalf("under the limit and incomplete must wait: %v", err)
		}
	})

	t.Run("a head exactly at the limit is accepted", func(t *testing.T) {
		head := "GET / HTTP/1.1\r\nHost: a\r\n\r\n"
		var req Request
		n, err := ParseHead([]byte(head), &req, Limits{MaxHead: len(head), MaxFields: 100})
		if err != nil || n != len(head) {
			t.Fatal(n, err)
		}
		_, err = ParseHead([]byte(head), &req, Limits{MaxHead: len(head) - 1, MaxFields: 100})
		if err == nil || err == ErrNeedMore {
			t.Fatal(err)
		}
	})

	t.Run("more fields than the limit is a 431", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString("GET / HTTP/1.1\r\nHost: a\r\n")
		for i := 0; i < 100; i++ {
			sb.WriteString("X: v\r\n")
		}
		sb.WriteString("\r\n")
		wantRefusal(t, sb.String(), 431, true)
		ok := strings.Replace(sb.String(), "X: v\r\n", "", 1)
		if _, _, err := parse(t, ok); err != nil {
			t.Fatalf("exactly 100 fields must pass: %v", err)
		}
	})

	t.Run("Content-Length must be digits only and fit int64", func(t *testing.T) {
		for _, v := range []string{"", "-1", "+1", "1 2", "1,1", "0x10", "9223372036854775808", "99999999999999999999", "1.0", "１"} {
			wantRefusal(t, "POST / HTTP/1.1\r\n"+okHost+"Content-Length: "+v+"\r\n\r\n", 400, true)
		}
		req, _, err := parse(t, "POST / HTTP/1.1\r\n"+okHost+"Content-Length: 9223372036854775807\r\n\r\n")
		if err != nil || req.ContentLength != 9223372036854775807 {
			t.Fatalf("%v", err)
		}
		req, _, err = parse(t, "POST / HTTP/1.1\r\n"+okHost+"Content-Length: 0007\r\n\r\n")
		if err != nil || req.ContentLength != 7 {
			t.Fatalf("%v", err)
		}
	})

	t.Run("header names match without regard to case", func(t *testing.T) {
		req, _, err := parse(t, "POST / HTTP/1.1\r\nhOsT: h\r\nCONTENT-length: 2\r\n\r\n")
		if err != nil || req.ContentLength != 2 || string(req.Host) != "h" {
			t.Fatalf("%v %+v", err, req)
		}
	})

	t.Run("the name keeps the case it was sent in", func(t *testing.T) {
		req, _, _ := parse(t, "GET / HTTP/1.1\r\n"+okHost+"x-MiXeD: 1\r\n\r\n")
		if string(req.Headers[1].Name) != "x-MiXeD" {
			t.Fatalf("%q", req.Headers[1].Name)
		}
	})

	t.Run("pipelined heads in one buffer are consumed one at a time", func(t *testing.T) {
		a := "POST /a HTTP/1.1\r\n" + okHost + "Content-Length: 2\r\n\r\n"
		b := "GET /b HTTP/1.1\r\n" + okHost + "\r\n"
		buf := []byte(a + "hi" + b + "GET /c HTT")
		var req Request
		n, err := ParseHead(buf, &req, DefaultLimits())
		if err != nil || n != len(a) || string(req.Path) != "/a" {
			t.Fatal(n, err)
		}
		rest := buf[n+2:]
		n, err = ParseHead(rest, &req, DefaultLimits())
		if err != nil || n != len(b) || string(req.Path) != "/b" {
			t.Fatal(n, err)
		}
		if _, err = ParseHead(rest[n:], &req, DefaultLimits()); err != ErrNeedMore {
			t.Fatalf("a partial third head must wait: %v", err)
		}
	})

	t.Run("a Request is fully reset between parses", func(t *testing.T) {
		var req Request
		ParseHead([]byte("POST /a?q HTTP/1.1\r\nHost: h\r\nExpect: 100-continue\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n"), &req, DefaultLimits())
		ParseHead([]byte("GET /b HTTP/1.1\r\nHost: h\r\n\r\n"), &req, DefaultLimits())
		if req.Chunked || req.Expect100 || req.RawQuery != nil || !req.KeepAlive || req.ContentLength != -1 || len(req.Headers) != 1 {
			t.Fatalf("%+v", req)
		}
	})
}

func TestParseHeadIncremental(t *testing.T) {
	head := "POST /a/b?c=d HTTP/1.1\r\nHost: example.com\r\nContent-Length: 5\r\nUser-Agent: x\r\n\r\n"
	t.Run("every proper prefix of a valid head asks for more and the whole head parses", func(t *testing.T) {
		var req Request
		for i := 0; i < len(head); i++ {
			n, err := ParseHead([]byte(head[:i]), &req, DefaultLimits())
			if err != ErrNeedMore || n != 0 {
				t.Fatalf("split at %d: n=%d err=%v", i, n, err)
			}
		}
		n, err := ParseHead([]byte(head), &req, DefaultLimits())
		if err != nil || n != len(head) {
			t.Fatal(n, err)
		}
	})
	t.Run("the result does not depend on bytes after the head", func(t *testing.T) {
		var req Request
		n, err := ParseHead([]byte(head+"hello"), &req, DefaultLimits())
		if err != nil || n != len(head) || req.ContentLength != 5 {
			t.Fatal(n, err)
		}
	})
}

func TestParseHeadAllocations(t *testing.T) {
	t.Run("a typical ten-header request allocates nothing after warm-up", func(t *testing.T) {
		buf := []byte(typicalRequest)
		var req Request
		ParseHead(buf, &req, DefaultLimits())
		allocs := testing.AllocsPerRun(1000, func() {
			if _, err := ParseHead(buf, &req, DefaultLimits()); err != nil {
				panic(err)
			}
		})
		if allocs != 0 {
			t.Fatalf("%v allocations", allocs)
		}
	})
	t.Run("a refusal allocates nothing either", func(t *testing.T) {
		buf := []byte("POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\nContent-Length: 4\r\n\r\n")
		var req Request
		allocs := testing.AllocsPerRun(1000, func() { ParseHead(buf, &req, DefaultLimits()) })
		if allocs != 0 {
			t.Fatalf("%v allocations", allocs)
		}
	})
}

const typicalRequest = "GET /api/v1/orders/1234?expand=items&limit=20 HTTP/1.1\r\n" +
	"Host: api.example.com\r\n" +
	"User-Agent: Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36\r\n" +
	"Accept: application/json, text/plain, */*\r\n" +
	"Accept-Language: en-US,en;q=0.9\r\n" +
	"Accept-Encoding: gzip, deflate, br\r\n" +
	"Connection: keep-alive\r\n" +
	"Cookie: session=abcdef0123456789; theme=dark\r\n" +
	"Referer: https://app.example.com/orders\r\n" +
	"Sec-Fetch-Mode: cors\r\n" +
	"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.e30.abc\r\n" +
	"\r\n"

func BenchmarkParseHead(b *testing.B) {
	buf := []byte(typicalRequest)
	var req Request
	b.ReportAllocs()
	b.SetBytes(int64(len(buf)))
	for i := 0; i < b.N; i++ {
		if _, err := ParseHead(buf, &req, DefaultLimits()); err != nil {
			b.Fatal(err)
		}
	}
}
