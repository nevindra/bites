// Probe: which negative Limits fields does nilogo accept today without a panic?
package main

import (
	"fmt"
	"time"

	nilogo "github.com/nevindra/bites"
)

func try(name string, l nilogo.Limits) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%-18s panics: %v\n", name, r)
		}
	}()
	a := nilogo.New(nilogo.WithLimits(l))
	_ = a
	fmt.Printf("%-18s New accepts silently (defaults applied)\n", name)
}

func main() {
	try("MaxHead", nilogo.Limits{MaxHead: -1})
	try("MaxFields", nilogo.Limits{MaxFields: -1})
	try("MaxBody", nilogo.Limits{MaxBody: -1})
	try("HeadTimeout", nilogo.Limits{HeadTimeout: -time.Second})
	try("BodyTimeout", nilogo.Limits{BodyTimeout: -time.Second})
	try("IdleTimeout", nilogo.Limits{IdleTimeout: -time.Second})
	try("WriteTimeout", nilogo.Limits{WriteTimeout: -time.Second})
	try("MaxConns", nilogo.Limits{MaxConns: -1})
	try("MaxDepth", nilogo.Limits{MaxDepth: -1})
	try("HandshakeTimeout", nilogo.Limits{HandshakeTimeout: -time.Second})
}
