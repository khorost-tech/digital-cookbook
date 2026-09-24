package token

import (
	"regexp"
	"strings"
	"testing"
)

func TestCodeFormat(t *testing.T) {
	re := regexp.MustCompile(`^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{3}-[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{3}$`)
	for i := 0; i < 200; i++ {
		c, err := Code()
		if err != nil {
			t.Fatalf("Code() error: %v", err)
		}
		if !re.MatchString(c) {
			t.Fatalf("bad code %q", c)
		}
		if strings.ContainsAny(c, "01OI") {
			t.Fatalf("ambiguous chars in %q", c)
		}
	}
}

func TestSafeEqualNormalizes(t *testing.T) {
	if !SafeEqual("abc-def", "ABCDEF") {
		t.Fatal("normalized codes must be equal")
	}
	if SafeEqual("abc-def", "abc-deg") {
		t.Fatal("different codes must differ")
	}
}

func TestOpaqueHexLength(t *testing.T) {
	s, err := OpaqueHex(16)
	if err != nil || len(s) != 32 {
		t.Fatalf("want 32 hex chars, got %q err=%v", s, err)
	}
}
