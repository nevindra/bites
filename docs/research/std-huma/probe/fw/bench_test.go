package main
import ("context";"encoding/json";"net/http";"net/http/httptest";"strconv";"testing"
 "github.com/danielgtaylor/huma/v2";"github.com/danielgtaylor/huma/v2/adapters/humago";"github.com/go-fuego/fuego")
type dw struct{ h http.Header; code int; n int }
func (d *dw) Header() http.Header { return d.h }
func (d *dw) Write(b []byte) (int, error) { d.n += len(b); return len(b), nil }
func (d *dw) WriteHeader(c int) { d.code = c }
type U struct{ ID int `json:"id"`; Name string `json:"name"` }
type HOut struct{ Body U }
func run(b *testing.B, h http.Handler) {
	r := httptest.NewRequest("GET", "/users/42", nil)
	w := &dw{h: http.Header{}}
	h.ServeHTTP(w, r); if w.code != 0 && w.code != 200 || w.n == 0 { b.Fatal("bad", w.code, w.n) }
	b.ReportAllocs(); b.ResetTimer()
	for i := 0; i < b.N; i++ { clear(w.h); w.n = 0; h.ServeHTTP(w, r) }
}
func BenchmarkStd(b *testing.B) {
	m := http.NewServeMux()
	m.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request){
		id, err := strconv.Atoi(r.PathValue("id")); if err != nil { http.Error(w,"bad",400); return }
		w.Header().Set("Content-Type","application/json"); json.NewEncoder(w).Encode(U{id,"bob"}) })
	run(b, m)
}
func BenchmarkHuma(b *testing.B) {
	m := http.NewServeMux(); api := humago.New(m, huma.DefaultConfig("t","1"))
	huma.Get(api, "/users/{id}", func(ctx context.Context, in *struct{ID int `path:"id"`}) (*HOut, error){ return &HOut{U{in.ID,"bob"}}, nil })
	run(b, m)
}
func BenchmarkFuego(b *testing.B) {
	s := fuego.NewServer(fuego.WithoutStartupMessages(), fuego.WithoutLogger())
	fuego.Get(s, "/users/{id}", func(c fuego.ContextNoBody) (U, error){ id, err := c.PathParamIntErr("id"); return U{id,"bob"}, err })
	run(b, s.Mux)
}
