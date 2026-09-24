// Package jwtclaims выпускает и валидирует короткоживущие JWT access-токены
// (HS256), которые сервисы-потребители проверяют локально по общему секрету,
// без похода в auth-сервис на каждый запрос.
package jwtclaims

import (
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// issuer — значение поля iss у всех токенов, выпущенных auth-сервисом.
const issuer = "auth"

// ErrBadAlg возвращается, когда алгоритм подписи токена не HMAC (защита от
// alg-confusion: токен с alg=none или alg=RS256 должен быть отклонён ещё на
// этапе выбора ключа, до какой-либо проверки подписи).
var ErrBadAlg = errors.New("jwtclaims: unexpected signing method")

// Claims — полезная нагрузка access-токена.
type Claims struct {
	AccountID int64    `json:"aid"`
	Nick      string   `json:"nick"`
	Roles     []string `json:"roles,omitempty"`
	jwt.RegisteredClaims
}

// Issue подписывает Claims алгоритмом HS256 общим секретом secret. Issuer
// выставляется в "auth", Subject — строковое представление AccountID,
// IssuedAt — текущее время, ExpiresAt — now+ttl. Поля c.RegisteredClaims,
// если заданы вызывающим кодом, будут перезаписаны.
func Issue(secret []byte, c Claims, ttl time.Duration) (string, error) {
	now := time.Now()
	c.RegisteredClaims = jwt.RegisteredClaims{
		Issuer:    issuer,
		Subject:   strconv.FormatInt(c.AccountID, 10),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return tok.SignedString(secret)
}

// Parse проверяет подпись и стандартные claims (exp, iss) токена raw и
// возвращает распакованные Claims. keyfunc явно проверяет, что метод подписи
// токена — *jwt.SigningMethodHMAC (а не alg=none, RS256 и т.п.), иначе
// возвращает ErrBadAlg до какого-либо использования secret как ключа.
func Parse(secret []byte, raw string) (*Claims, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrBadAlg
		}
		return secret, nil
	}, jwt.WithIssuer(issuer), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}

	return &c, nil
}
