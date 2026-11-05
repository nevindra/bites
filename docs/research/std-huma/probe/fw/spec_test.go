package main
import ("fmt";"testing";"encoding/json";"github.com/go-fuego/fuego")
func TestSpec(t *testing.T){
	s := fuego.NewServer(fuego.WithoutStartupMessages(), fuego.WithoutLogger())
	fuego.Get(s, "/u/{id}", func(c fuego.ContextNoBody) (FOut, error) { return FOut{}, nil })
	b,_ := json.Marshal(s.OutputOpenAPISpec().Paths.Find("/u/{id}"))
	fmt.Println(string(b))
}
