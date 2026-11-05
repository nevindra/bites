package jsoncodec

import (
	"time"
	"uuid"

	"github.com/nevindra/bites/internal/wire"
)

// Level is an enum, listed by Values the way wire reads it.
type Level string

func (Level) Values() []string { return []string{"low", "high"} }

type Audit struct {
	CreatedBy string
	Revision  uint8
}

type Line struct {
	SKU   string
	Qty   int
	Price float64
}

type Address struct {
	Street  string
	ZipCode string
}

// Order has one field of every shape the codec supports.
type Order struct {
	ID       uuid.UUID
	Customer string
	Age      int
	Small    int8
	Count    uint16
	Ratio    float32
	Active   bool
	Level    Level
	Created  time.Time
	Addr     Address
	Lines    []Line
	Tags     []string
	Scores   map[string]int
	Pair     [2]int
	Nick     wire.Opt[string]
	Deleted  wire.Null[string]
	Page     wire.Opt[int]
	Audit
	Alias  string `json:"aliasName,omitempty"`
	Skip   string `json:"-"`
	hidden int    `json:"-"`
}

const fullOrder = `{
 "id":"6ba7b810-9dad-11d1-80b4-00c04fd430c8","customer":"Ada \"A\" Lovelace","age":36,"small":-5,"count":65535,
 "ratio":1.5,"active":true,"level":"high","created":"2026-10-31T12:30:00.5+07:00",
 "addr":{"street":"1 Main St","zip_code":"12345"},
 "lines":[{"sku":"a","qty":1,"price":9.99},{"sku":"b","qty":2,"price":1e3}],
 "tags":["x","y\u00e9"],"scores":{"a":1,"b":2},"pair":[1,2],
 "nick":"ada","deleted":null,"page":3,
 "created_by":"me","revision":7,"aliasName":"al"}`

// Small is the struct of the zero-allocation and benchmark requirements: 3 strings, 2 ints,
// a nested struct and a slice of 3 small structs.
type Small struct {
	Name   string
	Email  string
	Note   string
	Age    int
	Count  int32
	Addr   Address
	Items  []Line
	Newest wire.Opt[string]
}

const smallBody = `{"name":"Ada Lovelace","email":"ada@example.com","note":"hello","age":36,"count":3,` +
	`"addr":{"street":"1 Main St","zip_code":"12345"},` +
	`"items":[{"sku":"a","qty":1,"price":9.5},{"sku":"b","qty":2,"price":1.25},{"sku":"c","qty":3,"price":100}],` +
	`"newest":"yes"}`
