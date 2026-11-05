// Command bites-site renders the documentation to static HTML (ADR 0073).
//
//	go run ./cmd/bites-site -out /tmp/bites-site
//
// It reads README.md, GLOSSARY.md and docs/ under -src and writes a self-contained site under
// -out, with relative links so it can be served from any path or opened from disk. It publishes
// nothing and it fails, listing them, when a link between pages or an anchor is broken.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/nevindra/bites/internal/site"
)

func main() {
	var o site.Options
	flag.StringVar(&o.Src, "src", ".", "the repository root")
	flag.StringVar(&o.Out, "out", "site", "the directory to write")
	flag.StringVar(&o.RepoURL, "repo-url", "", "base URL of the repository; links to files that are not documentation point there")
	flag.StringVar(&o.Branch, "branch", "main", "the branch -repo-url links to")
	flag.StringVar(&o.Title, "title", "bites", "the name of the site")
	flag.Parse()
	rep, err := site.Build(o)
	if rep != nil {
		for _, b := range rep.Broken {
			fmt.Fprintln(os.Stderr, "broken link:", b)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bites-site:", err)
		os.Exit(1)
	}
	fmt.Printf("bites-site: %d pages written to %s", rep.Pages, o.Out)
	if rep.Unlinked > 0 {
		fmt.Printf(" (%d links to files outside the documentation left as text; pass -repo-url to link them)", rep.Unlinked)
	}
	fmt.Println()
}
