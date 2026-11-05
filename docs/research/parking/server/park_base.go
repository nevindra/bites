//go:build base

package main

import nilogo "github.com/nevindra/bites"

// parkOptions is nothing in the "base" build: the tree before step 16 has no parking and no
// WithoutParking, and -park is ignored.
func parkOptions(string) []nilogo.Option { return nil }
