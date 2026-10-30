// Package sec — исправленная пара sample/sec.
package sec

import (
	"crypto/rand"
	"encoding/hex"
	"os"
)

// Token для сессии из crypto/rand.
func Token() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Auth сверяет с секретом из окружения, а не из кода.
func Auth(p string) bool { return p != "" && p == os.Getenv("API_PASSWORD") }
