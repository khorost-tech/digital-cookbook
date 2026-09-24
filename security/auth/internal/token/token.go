package token

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"strings"
)

const charset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func OpaqueHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func Code() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 6)
	for i, x := range b {
		out[i] = charset[int(x)%len(charset)]
	}
	return string(out[:3]) + "-" + string(out[3:]), nil
}

func NormalizeCode(s string) string {
	return strings.ToUpper(strings.ReplaceAll(s, "-", ""))
}

func SafeEqual(a, b string) bool {
	return hmac.Equal([]byte(NormalizeCode(a)), []byte(NormalizeCode(b)))
}
