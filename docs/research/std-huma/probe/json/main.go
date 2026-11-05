package main
import ("fmt";"encoding/json";jv2 "encoding/json/v2")
type T struct { S []int; M map[string]int; B []byte; Name string `json:"name"`; P *int }
func main(){
	for _, c := range []struct{n string; m func(any)([]byte,error)}{{"json(v1 api, v2 backed)",json.Marshal},{"json/v2",func(v any)([]byte,error){return jv2.Marshal(v)}}} {
		b,err := c.m(T{}); fmt.Println(c.n, string(b), err)
	}
	var t T
	for _, in := range []string{`{"NAME":"x"}`,`{"name":"a","name":"b"}`, `{"name":"a","extra":1}`, `{"B":"aGk="}`} {
		t = T{}
		e1 := json.Unmarshal([]byte(in), &t); a:=t
		t = T{}
		e2 := jv2.Unmarshal([]byte(in), &t); 
		fmt.Printf("in=%s\n  json: %+v err=%v\n  v2:   %+v err=%v\n", in, a, e1, t, e2)
	}
}
