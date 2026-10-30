// Package sec — секрет в коде и предсказуемый токен. Секрет — заведомо тестовый.
package sec

import (
	"fmt"
	"math/rand"
)

const apiPassword = "demo-not-a-real-secret-1234" // want gosec

// Token для сессии из math/rand: последовательность предсказуема.
func Token() string {
	return fmt.Sprintf("%016x", rand.Int63()) // want gosec
}

// Auth — заглушка, чтобы секрет был использован.
func Auth(p string) bool { return p == apiPassword }
