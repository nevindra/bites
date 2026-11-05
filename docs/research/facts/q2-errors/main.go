package main

import (
	"encoding/json/v2"
	"errors"
	"fmt"
)

type In struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
	Tags []int  `json:"tags"`
}

func show(label, doc string, opts ...json.Options) {
	var in In
	err := json.Unmarshal([]byte(doc), &in, opts...)
	fmt.Printf("--- %s\nerr: %v\nin: %+v\n", label, err, in)
	var se *json.SemanticError
	if errors.As(err, &se) {
		fmt.Printf("SemanticError ptr=%q off=%d gotype=%v err=%v\n", se.JSONPointer, se.ByteOffset, se.GoType, se.Err)
	}
	fmt.Printf("errors.Join-like? %T\n", err)
}

func main() {
	show("two wrong types + unknown", `{"name":5,"age":"x","tags":[1,"b"],"extra":1}`)
	show("unknown only, default", `{"name":"a","extra":1}`)
	show("unknown rejected", `{"name":"a","extra":1}`, json.RejectUnknownMembers(true))
	show("nested unknown, tags wrong", `{"name":"a","zzz":{"a":1},"age":"x"}`, json.RejectUnknownMembers(true))
	show("syntax error", `{"name":"a",`)
}

func init() {
	show("valid name then bad age then bad tags", `{"name":"a","age":"x","tags":[1,"b"]}`)
}
