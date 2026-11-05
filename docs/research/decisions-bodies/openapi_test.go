package bodies

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nevindra/bites/internal/openapi"
)

type Avatar struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// What the generator says today for a []byte root (a raw body) and for a []byte field (base64).
func TestOpenAPIBytes(t *testing.T) {
	r := openapi.New(openapi.Options{})
	for _, ty := range []reflect.Type{reflect.TypeFor[[]byte](), reflect.TypeFor[Avatar](), reflect.TypeFor[[16]byte]()} {
		s, err := r.Schema(ty, openapi.Request)
		if err != nil {
			t.Fatal(err)
		}
		r.Resolve(s)
		b, _ := json.Marshal(s)
		t.Logf("%-12s -> %s", ty, b)
	}
}
