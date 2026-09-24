package jwtclaims

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestIssueParse(t *testing.T) {
	sec := []byte("secret")
	raw, err := Issue(sec, Claims{AccountID: 7, Nick: "bob"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(sec, raw)
	if err != nil || c.AccountID != 7 || c.Nick != "bob" {
		t.Fatalf("c=%+v err=%v", c, err)
	}
}

func TestParseRejectsAlgNone(t *testing.T) {
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"aid": 7})
	raw, _ := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := Parse([]byte("secret"), raw); err == nil {
		t.Fatal("alg=none must be rejected")
	}
}

func TestParseExpired(t *testing.T) {
	sec := []byte("s")
	raw, _ := Issue(sec, Claims{AccountID: 1}, -time.Minute)
	if _, err := Parse(sec, raw); err == nil {
		t.Fatal("expired must fail")
	}
}
