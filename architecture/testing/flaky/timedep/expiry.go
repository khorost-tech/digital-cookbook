// Package timedep — класс flaky «зависимость от времени».
// Токен с TTL: проверка срока годности должна принимать «текущее время»
// параметром, а не читать time.Now() внутри — тогда тест детерминирован.
package timedep

import "time"

// Token — токен с моментом истечения.
type Token struct {
	ExpiresAt time.Time
}

// NewToken выпускает токен, годный ttl от переданного now.
func NewToken(now time.Time, ttl time.Duration) Token {
	return Token{ExpiresAt: now.Add(ttl)}
}

// ValidAt отвечает, годен ли токен в момент now. Время — аргумент, а не
// скрытое обращение к часам: это и есть шов для детерминированного теста.
func ValidAt(t Token, now time.Time) bool {
	return now.Before(t.ExpiresAt)
}
