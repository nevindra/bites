package engine

import (
	"strings"
	"testing"

	"github.com/nevindra/bites/internal/h1"
)

func lines(v ...string) []h1.Header {
	var hs []h1.Header
	for _, s := range v {
		hs = append(hs, h1.Header{Name: []byte("Accept-Encoding"), Value: []byte(s)})
	}
	return hs
}

// acceptsGzip is what the gzip-only engine of ADR 0031 decided: gzip has a weight above 0.
func acceptsGzip(hs []h1.Header) bool { return weight(hs, "gzip") > 0 }

func TestAcceptEncodingAcceptsGzipByRFC9110(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want bool
	}{
		{nil, false},
		{[]string{""}, false},
		{[]string{"gzip"}, true},
		{[]string{"GZip"}, true},
		{[]string{"x-gzip"}, true},
		{[]string{"deflate, gzip"}, true},
		{[]string{" gzip ; q=0.5 "}, true},
		{[]string{"gzip;q=1"}, true},
		{[]string{"gzip;q=1.0"}, true},
		{[]string{"gzip;q=1.000"}, true},
		{[]string{"gzip;q=0.001"}, true},
		{[]string{"gzip;level=1;q=0.1"}, true},
		{[]string{"gzip;q=0"}, false},
		{[]string{"gzip;q=0.0"}, false},
		{[]string{"gzip;q=0.000"}, false},
		{[]string{"gzip;q=Q"}, false},
		{[]string{"gzip;q=2"}, false},
		{[]string{"gzip;q=1.5"}, false},
		{[]string{"gzip;q=0.0001"}, false},
		{[]string{"gzip;q="}, false},
		{[]string{"gzip;q=-1"}, false},
		{[]string{"*"}, true},
		{[]string{"*;q=0"}, false},
		{[]string{"*;q=0.1"}, true},
		{[]string{"br, *"}, true},
		{[]string{"gzip;q=0, *"}, false},
		{[]string{"*, gzip;q=0"}, false},
		{[]string{"*;q=0, gzip"}, true},
		{[]string{"identity"}, false},
		{[]string{"identity;q=0"}, false},
		{[]string{"gzipped"}, false},
		{[]string{"br", "gzip"}, true},
		{[]string{"gzip", "gzip;q=0"}, false},
		{[]string{"gzip;q=0", "gzip"}, false},
		{[]string{",,gzip,,"}, true},
	} {
		if got := acceptsGzip(lines(tc.in...)); got != tc.want {
			t.Errorf("Accept-Encoding %q: acceptsGzip = %v, want %v", strings.Join(tc.in, " | "), got, tc.want)
		}
	}
}

func TestOtherHeadersAreNotTakenForAcceptEncoding(t *testing.T) {
	if acceptsGzip([]h1.Header{{Name: []byte("X-Accept-Encoding"), Value: []byte("gzip")}, {Name: []byte("Accept"), Value: []byte("gzip")}}) {
		t.Fatal("accepted gzip from the wrong header")
	}
}

func TestOnlyBinaryMediaTypesAreTakenForCompressed(t *testing.T) {
	for ct, want := range map[string]bool{
		"": false, "application/json": false, "text/html; charset=utf-8": false, "image/svg+xml": false, "Image/SVG+XML; charset=utf-8": false,
		"image/png": true, "IMAGE/JPEG": true, "video/mp4": true, "audio/mpeg": true, "font/woff2": true,
		"application/zip": true, "application/gzip": true, "application/pdf": true, "application/x-7z-compressed": true,
		"application/octet-stream": false, "application/problem+json": false,
	} {
		if got := incompressible(ct); got != want {
			t.Errorf("incompressible(%q) = %v, want %v", ct, got, want)
		}
	}
}

func TestTheHeadIsScannedForTheFieldsGzipMustRespect(t *testing.T) {
	head := []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\netag:   \"abc\"\r\nCache-Control: public, max-age=5, NO-TRANSFORM\r\nvary: Origin\r\nContent-Range: bytes 0-1/2\r\ncontent-encoding: br\r\n")
	hs := scanHead(head)
	if !hs.encoded || !hs.noTransform || !hs.ranged || hs.varyCovers || hs.etag < 0 || head[hs.etag] != '"' {
		t.Fatalf("%+v", hs)
	}
	if hs := scanHead([]byte("HTTP/1.1 200 OK\r\nVary: Accept-Encoding, Origin\r\nETag: W/\"x\"\r\n")); !hs.varyCovers || hs.etag != -1 {
		t.Fatalf("%+v", hs)
	}
}
