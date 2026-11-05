// Command bites-doc writes the Descriptions methods of a package from its doc comments (ADR 0020).
//
//	//go:generate go run github.com/nevindra/bites/cmd/bites-doc
//
// run in the package that holds the Input and output types. It writes bites_descriptions_gen.go
// beside the sources. Flags:
//
//	-dir d      the package directory (the current one)
//	-types a,b  only these types
//	-check      write nothing and exit 1 when the file on disk is not what would be written
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nevindra/bites/internal/docgen"
)

func main() {
	dir := flag.String("dir", ".", "the package directory")
	types := flag.String("types", "", "comma-separated type names; every documented struct when empty")
	check := flag.Bool("check", false, "write nothing; exit 1 if the generated file is out of date")
	flag.Parse()
	var opt docgen.Options
	if *types != "" {
		opt.Types = strings.Split(*types, ",")
	}
	if *check {
		res, err := docgen.Generate(*dir, opt)
		if err != nil {
			fatal(err)
		}
		old, _ := os.ReadFile(filepath.Join(*dir, docgen.FileName))
		if !bytes.Equal(old, res.Source) {
			fmt.Fprintf(os.Stderr, "bites-doc: %s is out of date; run go generate\n", filepath.Join(*dir, docgen.FileName))
			os.Exit(1)
		}
		return
	}
	res, changed, err := docgen.Write(*dir, opt)
	if err != nil {
		fatal(err)
	}
	for _, n := range res.Notes {
		fmt.Fprintln(os.Stderr, "bites-doc: "+n)
	}
	if changed {
		fmt.Fprintf(os.Stderr, "bites-doc: wrote %s for %s\n", docgen.FileName, strings.Join(res.Types, ", "))
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "bites-doc:", err)
	os.Exit(1)
}
