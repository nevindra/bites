// Package testuuid holds a uuid type of a package other than the standard library's, as
// github.com/google/uuid is, for the tests of how the app reads one (ADR 0042). Its import path
// names uuid, which is how the app tells such a type.
package testuuid

import (
	"encoding/hex"
	"errors"
)

// UUID is 16 bytes written as 32 hex digits.
type UUID [16]byte

// UnmarshalText reads 32 hex digits.
func (u *UUID) UnmarshalText(b []byte) error {
	if len(b) != 32 {
		return errors.New("testuuid: not 32 hex digits")
	}
	_, err := hex.Decode(u[:], b)
	return err
}

// MarshalText writes 32 hex digits.
func (u UUID) MarshalText() ([]byte, error) { return []byte(hex.EncodeToString(u[:])), nil }
