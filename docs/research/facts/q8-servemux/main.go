package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
)

func h(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s name=%q path=%q rawpath=%q", name, r.PathValue("name"), r.URL.Path, r.URL.RawPath)
	}
}

func try(mux *http.ServeMux, method, host, target string) {
	r := httptest.NewRequest(method, target, nil)
	if host != "" {
		r.Host = host
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	body := w.Body.String()
	if len(body) > 90 {
		body = body[:90]
	}
	fmt.Printf("  %-5s host=%-14s %-26s -> %d allow=%q loc=%q body=%q\n", method, host, target, w.Code, w.Header().Get("Allow"), w.Header().Get("Location"), body)
}

func main() {
	fmt.Println("(a) subtree pattern /static/ and exact /static (no trailing slash)")
	m := http.NewServeMux()
	m.HandleFunc("/static/", h("subtree"))
	for _, t := range []string{"/static/", "/static/a/b", "/static", "/static/../x", "/statics"} {
		try(m, "GET", "", t)
	}
	fmt.Println("(a2) subtree + /{$} exact root")
	m = http.NewServeMux()
	m.HandleFunc("/{$}", h("root"))
	m.HandleFunc("/api/", h("api"))
	for _, t := range []string{"/", "/api/x", "/api", "/other"} {
		try(m, "GET", "", t)
	}
	fmt.Println("(b) /files/{name}")
	m = http.NewServeMux()
	m.HandleFunc("/files/{name}", h("one"))
	for _, t := range []string{"/files/a", "/files/a%2Fb", "/files/a/b", "/files/a%20b", "/files/", "/files/%2e%2e"} {
		try(m, "GET", "", t)
	}
	fmt.Println("(b2) /files/{name...}")
	m = http.NewServeMux()
	m.HandleFunc("/files/{name...}", h("rest"))
	for _, t := range []string{"/files/a/b", "/files/a%2Fb/c", "/files/"} {
		try(m, "GET", "", t)
	}
	fmt.Println("(c) host-specific patterns")
	m = http.NewServeMux()
	m.HandleFunc("example.com/x", h("host"))
	m.HandleFunc("/x", h("any"))
	m.HandleFunc("/only", h("any-only"))
	m.HandleFunc("api.example.com/y", h("hostY"))
	for _, c := range [][2]string{{"example.com", "/x"}, {"other.com", "/x"}, {"example.com:8080", "/x"}, {"EXAMPLE.com", "/x"}, {"api.example.com", "/y"}, {"other.com", "/y"}, {"", "/only"}} {
		try(m, "GET", c[0], c[1])
	}
	fmt.Println("(d) method-less vs method patterns on the same path")
	m = http.NewServeMux()
	m.HandleFunc("/p", h("any-method"))
	m.HandleFunc("GET /p", h("GET"))
	for _, me := range []string{"GET", "HEAD", "POST", "DELETE"} {
		try(m, me, "", "/p")
	}
	fmt.Println("(d2) only method patterns")
	m = http.NewServeMux()
	m.HandleFunc("GET /q", h("GET"))
	m.HandleFunc("POST /q", h("POST"))
	for _, me := range []string{"GET", "HEAD", "POST", "PUT", "OPTIONS"} {
		try(m, me, "", "/q")
	}
	fmt.Println("(d3) 404 vs 405")
	try(m, "GET", "", "/nope")
	fmt.Println("(e) conflicts")
	func() {
		defer func() { fmt.Println("  panic:", recover()) }()
		m := http.NewServeMux()
		m.HandleFunc("/a/{x}/b", h("1"))
		m.HandleFunc("/a/b/{y}", h("2"))
	}()
	func() {
		defer func() { fmt.Println("  panic:", recover()) }()
		m := http.NewServeMux()
		m.HandleFunc("GET /z", h("1"))
		m.HandleFunc("GET /z", h("2"))
	}()
}
