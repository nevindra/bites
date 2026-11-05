package main
import ("context";"fmt";"io";"net/http";"net/http/httptest";"strings";"testing"
 "github.com/danielgtaylor/huma/v2";"github.com/danielgtaylor/huma/v2/adapters/humago")
type In struct{ ID int `path:"id" minimum:"1" doc:"user id"` }
type Out struct{ Body struct{ ID int `json:"id"`; Name string `json:"name"` } }
func do(m http.Handler, method, p, body string) {
	r := httptest.NewRequest(method, p, strings.NewReader(body)); if body!="" { r.Header.Set("Content-Type","application/json") }
	w := httptest.NewRecorder(); m.ServeHTTP(w, r); b,_ := io.ReadAll(w.Body)
	fmt.Printf("%s %s -> %d ct=%q\n   %s\n", method, p, w.Code, w.Header().Get("Content-Type"), b)
}
func TestHumaMismatch(t *testing.T) {
	func(){ defer func(){ fmt.Println("MISMATCH (path {id}, struct has no path field) panic:", recover()) }()
		m := http.NewServeMux(); api := humago.New(m, huma.DefaultConfig("t","1"))
		huma.Get(api, "/u/{id}", func(ctx context.Context, in *struct{ Q string `query:"q"` }) (*Out, error){ return &Out{}, nil })
		fmt.Println("no panic at registration")
		do(m,"GET","/u/5","")
	}()
	func(){ defer func(){ fmt.Println("MISMATCH2 (struct path:\"id\" but route has no {id}) panic:", recover()) }()
		m := http.NewServeMux(); api := humago.New(m, huma.DefaultConfig("t","1"))
		huma.Get(api, "/u", func(ctx context.Context, in *In) (*Out, error){ return &Out{}, nil })
		fmt.Println("no panic at registration")
	}()
	func(){ defer func(){ fmt.Println("UNTAGGED field panic:", recover()) }()
		m := http.NewServeMux(); api := humago.New(m, huma.DefaultConfig("t","1"))
		huma.Get(api, "/u/{id}", func(ctx context.Context, in *struct{ ID int }) (*Out, error){ return &Out{}, nil })
		fmt.Println("no panic at registration")
		do(m,"GET","/u/5","")
	}()
}
type Create struct{ Body struct{ Name string `json:"name" minLength:"2"`; Age int `json:"age"` } }
func TestHumaErrors(t *testing.T) {
	m := http.NewServeMux(); api := humago.New(m, huma.DefaultConfig("t","1"))
	huma.Get(api, "/u/{id}", func(ctx context.Context, in *In) (*Out, error){ o:=&Out{}; o.Body.ID=in.ID; o.Body.Name="bob"; return o, nil })
	huma.Post(api, "/u", func(ctx context.Context, in *Create) (*Out, error){ return &Out{}, nil })
	do(m,"GET","/u/5",""); do(m,"GET","/u/abc",""); do(m,"GET","/u/0","")
	do(m,"POST","/u",`{"name":"x"`); do(m,"POST","/u",`{"name":"x","age":"old"}`); do(m,"POST","/u",`{"name":"bob","extra":1}`)
	do(m,"GET","/openapi.json","")
}
