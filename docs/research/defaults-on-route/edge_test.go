package defaultsproto

import "testing"

type UUID [16]byte // what google/uuid and stdlib uuid look like
type Money struct {
	Minor int64
	Cur   string
} // an app's TextUnmarshaler-style struct

type byOwner struct {
	Query struct {
		OwnerID UUID
		Floor   Money
	}
}

func TestTypesThePoisonDoesNotCover(t *testing.T) {
	r, err := Register(func(in *byOwner) {}) // NO defaults at all
	t.Logf("no defaults given -> defaulted=%v err=%v", r.Defaulted(), err)
}
