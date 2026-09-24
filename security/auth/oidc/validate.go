// Package oidc — минимальный клиент локальной валидации OIDC id_token:
// вместо похода к провайдеру за каждым токеном (introspection) клиент тянет
// JWKS один раз (здесь — при каждом вызове, для demo этого достаточно;
// в проде JWKS кэшируется по jwks_uri с уважением к Cache-Control/kid-ротации)
// и проверяет RSA-подпись локально по публичному ключу, найденному по kid.
package oidc

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
)

// ErrUnexpectedAlg возвращается, когда алгоритм подписи id_token — не RSA
// (защита от alg-confusion: id_token с alg=none, alg=HS256 и т.п. должен
// быть отклонён ещё на этапе выбора ключа, до какой-либо проверки подписи).
var ErrUnexpectedAlg = errors.New("oidc: unexpected signing algorithm, want RS256")

// ErrKeyNotFound возвращается, когда kid из заголовка id_token отсутствует в
// JWKS провайдера.
var ErrKeyNotFound = errors.New("oidc: signing key (kid) not found in JWKS")

// jwk — один ключ из JWKS-документа (RFC 7517), только поля, нужные для RSA.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// jwksDoc — тело ответа jwks_uri.
type jwksDoc struct {
	Keys []jwk `json:"keys"`
}

// ValidateIDToken тянет JWKS с jwksURL, проверяет подпись id_token raw по
// ключу, найденному по kid из заголовка токена (принимается ТОЛЬКО RS256 —
// любой другой алгоритм, включая none и HS256, отклоняется до проверки
// подписи), и валидирует claims iss==issuer, aud содержит audience, exp не
// истёк. Возвращает claims токена как map.
func ValidateIDToken(jwksURL, issuer, audience, raw string) (map[string]any, error) {
	doc, err := fetchJWKS(jwksURL)
	if err != nil {
		return nil, fmt.Errorf("oidc: fetch jwks: %w", err)
	}

	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (interface{}, error) {
		// Явная проверка метода подписи ДО использования какого-либо ключа —
		// иначе токен, подделанный как HS256 с публичным RSA-модулем (или
		// любым иным известным атакующему значением) в роли HMAC-секрета,
		// прошёл бы проверку (классический alg-confusion, RS256 -> HS256).
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, ErrUnexpectedAlg
		}

		kid, _ := t.Header["kid"].(string)
		key, err := doc.findRSAPublicKey(kid)
		if err != nil {
			return nil, err
		}
		return key, nil
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("oidc: validate id_token: %w", err)
	}

	return map[string]any(claims), nil
}

func fetchJWKS(jwksURL string) (jwksDoc, error) {
	var doc jwksDoc

	resp, err := http.Get(jwksURL) //nolint:gosec,noctx // jwksURL — параметр демо-клиента, не пользовательский ввод HTTP-обработчика
	if err != nil {
		return doc, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return doc, fmt.Errorf("jwks endpoint returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return doc, err
	}
	return doc, nil
}

func (d jwksDoc) findRSAPublicKey(kid string) (*rsa.PublicKey, error) {
	for _, k := range d.Keys {
		if k.Kty != "RSA" || k.Kid != kid {
			continue
		}
		return rsaPublicKeyFromJWK(k)
	}
	return nil, ErrKeyNotFound
}

func rsaPublicKeyFromJWK(k jwk) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decode jwk.n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decode jwk.e: %w", err)
	}

	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: int(new(big.Int).SetBytes(eBytes).Int64()),
	}, nil
}
