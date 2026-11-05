package jwttest_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites/modules/jwt"
	"github.com/nevindra/bites/modules/jwt/jwttest"
)

type claims struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
}

var secret = []byte("a test secret that is long enough")

func TestATokenItMakesIsVerifiedByAnHS256KeyOfTheSameSecretAndKid(t *testing.T) {
	v := jwt.New[claims](jwt.Config{Keys: jwt.Keys(jwt.HS256("k1", secret))})
	tok := jwttest.Token(t, secret, "k1", claims{Sub: "ada", Exp: time.Now().Add(time.Hour).Unix()})
	got, err := v.Verify(context.Background(), tok)
	if err != nil || got.Sub != "ada" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestAnExpiredTokenItMakesIsRefusedAsExpired(t *testing.T) {
	v := jwt.New[claims](jwt.Config{Keys: jwt.Keys(jwt.HS256("", secret))})
	tok := jwttest.Token(t, secret, "", claims{Sub: "ada", Exp: time.Now().Add(-time.Hour).Unix()})
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, jwt.ErrExpired) {
		t.Fatalf("%v", err)
	}
}

// fatalTB records the failure instead of ending the test.
type fatalTB struct {
	testing.TB
	msg string
}

func (f *fatalTB) Helper() {}
func (f *fatalTB) Fatalf(format string, args ...any) {
	if f.msg == "" {
		f.msg = fmt.Sprintf(format, args...)
	}
}

func TestAShortSecretOrClaimsThatAreNotAnObjectFailTheTestSayingWhy(t *testing.T) {
	tb := &fatalTB{TB: t}
	jwttest.Token(tb, []byte("short"), "", claims{})
	if !strings.Contains(tb.msg, "requires at least 32") {
		t.Errorf("a short secret: %q", tb.msg)
	}
	tb = &fatalTB{TB: t}
	jwttest.Token(tb, secret, "", "ada")
	if !strings.Contains(tb.msg, "not a JSON object") {
		t.Errorf("a string as claims: %q", tb.msg)
	}
}
