// Probe for ADR 0080: what the standard library lets a configuration module rely on.
// Run: go run .   (stdlib only; nothing is written)
package main

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	// 1. flag: Visit reports only the flags that were set; an unknown flag is an error with ContinueOnError.
	fs := flag.NewFlagSet("app", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("db-url", "", "")
	fs.String("port", "", "")
	err := fs.Parse([]string{"-db-url=x", "--port", "", "-nope=1"})
	fmt.Printf("1a unknown flag: %v\n", err)
	fs2 := flag.NewFlagSet("app", flag.ContinueOnError)
	fs2.SetOutput(io.Discard)
	fs2.String("db-url", "", "")
	fs2.String("port", "", "")
	err = fs2.Parse([]string{"-db-url=x", "--port", ""})
	var set []string
	fs2.Visit(func(f *flag.Flag) { set = append(set, f.Name+"="+fmt.Sprintf("%q", f.Value.String())) })
	fmt.Printf("1b err=%v set=%v args=%v\n", err, set, fs2.Args())
	err = fs2.Parse([]string{"-h"})
	fmt.Printf("1c -h: %v (flag.ErrHelp=%v)\n", err, err == flag.ErrHelp)

	// 2. environment: an empty variable is set; unset is not.
	os.Setenv("PROBE_EMPTY", "")
	_, ok1 := os.LookupEnv("PROBE_EMPTY")
	_, ok2 := os.LookupEnv("PROBE_UNSET")
	fmt.Printf("2a empty set=%v unset set=%v\n", ok1, ok2)
	os.Setenv("PROBE_DB_URL", "x")
	os.Setenv("PROBE_TYPO", "y")
	n := 0
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PROBE_") {
			n++
		}
	}
	fmt.Printf("2b variables with the prefix: %d\n", n)

	// 3. JSON: v1 map decoding keeps the last of two equal keys silently; jsontext refuses them.
	doc := []byte(`{"port": 80, "port": 81}`)
	var m map[string]any
	err = json.Unmarshal(doc, &m)
	fmt.Printf("3a json/v2 map: m=%v err=%v\n", m, err)
	d := jsontext.NewDecoder(bytes.NewReader(doc))
	var perr error
	for {
		_, e := d.ReadToken()
		if e != nil {
			if e != io.EOF {
				perr = e
			}
			break
		}
	}
	fmt.Printf("3b jsontext duplicate: %v\n", perr)
	d = jsontext.NewDecoder(strings.NewReader(`{"a": 1e3, "b": 12345678901234567890, "c": null, "d": "x"}`))
	for {
		t, e := d.ReadValue()
		_ = t
		if e != nil {
			break
		}
		fmt.Printf("3c value text: %s\n", t)
		break
	}
	d = jsontext.NewDecoder(strings.NewReader(`{"a": 1e3, "b": 12345678901234567890, "c": null, "d": "x"}`))
	for {
		tok, e := d.ReadToken()
		if e != nil {
			break
		}
		fmt.Printf("3d token kind=%q text=%s\n", tok.Kind(), tok.String())
	}
}
