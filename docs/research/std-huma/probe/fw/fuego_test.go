package main
import ("fmt";"testing";"net/http/httptest";"io";"strings";"github.com/go-fuego/fuego")
type FOut struct{ ID int `json:"id"`; Name string `json:"name"` }
type FIn struct{ Name string `json:"name" validate:"min=2"`; Age int `json:"age"` }
func fdo(s *fuego.Server, method, p, body string) {
	r := httptest.NewRequest(method, p, strings.NewReader(body)); if body!="" { r.Header.Set("Content-Type","application/json") }
	w := httptest.NewRecorder(); s.Mux.ServeHTTP(w, r); b,_ := io.ReadAll(w.Body)
	fmt.Printf("%s %s -> %d ct=%q\n   %s\n", method, p, w.Code, w.Header().Get("Content-Type"), b)
}
func TestFuego(t *testing.T) {
	s := fuego.NewServer(fuego.WithoutStartupMessages(), fuego.WithoutLogger())
	fuego.Get(s, "/u/{id}", func(c fuego.ContextNoBody) (FOut, error) {
		id, err := c.PathParamIntErr("id"); if err != nil { return FOut{}, err }
		return FOut{ID:id, Name:"bob"}, nil })
	fuego.Get(s, "/v/{id}", func(c fuego.ContextNoBody) (FOut, error) { return FOut{ID:c.PathParamInt("id")}, nil })
	fuego.Post(s, "/u", func(c fuego.ContextWithBody[FIn]) (FOut, error) { b, err := c.Body(); if err!=nil {return FOut{}, err}; return FOut{Name:b.Name}, nil })
	fuego.Get(s, "/w/{id}", func(c fuego.ContextNoBody) (FOut, error) { return FOut{}, nil }) // for mismatch: use wrong name below
	func(){ defer func(){ fmt.Println("fuego wrong-name PathParam: recover=", recover()) }()
		fuego.Get(s, "/x/{id}", func(c fuego.ContextNoBody) (FOut, error) { return FOut{ID:c.PathParamInt("idd")}, nil })
		fdo(s,"GET","/x/5","")
	}()
	fdo(s,"GET","/u/5",""); fdo(s,"GET","/u/abc",""); fdo(s,"GET","/v/abc","")
	fdo(s,"POST","/u",`{"name":"x"`); fdo(s,"POST","/u",`{"name":"x","age":"old"}`); fdo(s,"POST","/u",`{"name":"x"}`)
	fdo(s,"GET","/swagger/openapi.json","")
}
