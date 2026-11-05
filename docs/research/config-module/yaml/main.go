// Probe for ADR 0080: a YAML scalar read as the text written, not as a resolved type.
// Run: go run .
package main

import (
	"fmt"

	yaml "go.yaml.in/yaml/v3"
)

func main() {
	src := "country: no\nport: 0o17\nname: \"007\"\nflag: yes\nempty:\nlist: [a, b]\nnested: {x: 1}\ndup: 1\ndup: 2\n"
	var doc yaml.Node
	err := yaml.Unmarshal([]byte(src), &doc)
	fmt.Println("duplicate keys err:", err)
	src = "country: no\nport: 0o17\nname: \"007\"\nflag: yes\nempty:\nlist: [a, b]\nnested: {x: 1}\n"
	doc = yaml.Node{}
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		fmt.Println(err)
		return
	}
	m := doc.Content[0]
	for i := 0; i < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		fmt.Printf("%s: kind=%d tag=%s value=%q line=%d\n", k.Value, v.Kind, v.Tag, v.Value, v.Line)
	}
}
