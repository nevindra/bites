//go:build !base

package main

import nilogo "github.com/nevindra/bites"

// parkOptions is the switch of the measurement: -park=off is WithoutParking (ADR 0005).
func parkOptions(mode string) []nilogo.Option {
	if mode == "off" {
		return []nilogo.Option{nilogo.WithoutParking()}
	}
	return nil
}
