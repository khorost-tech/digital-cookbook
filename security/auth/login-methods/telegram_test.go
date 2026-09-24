package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func computeValidHash(botToken string, data map[string]string) string {
	secret := sha256.Sum256([]byte(botToken))
	keys := make([]string, 0, len(data))
	for k := range data {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+data[k])
	}
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestTelegramWidgetValid(t *testing.T) {
	bot := "123:ABC"
	// auth_date — свежее время: виджет подписывает payload в момент входа,
	// и с FIX M-1 свежесть теперь тоже проверяется (см. TestTelegramWidgetStale).
	data := map[string]string{"id": "42", "auth_date": strconv.FormatInt(time.Now().Unix(), 10), "first_name": "Bob"}
	data["hash"] = computeValidHash(bot, data)
	ok, err := VerifyTelegramWidget(bot, data, DefaultTelegramAuthMaxAge)
	if err != nil || !ok {
		t.Fatalf("valid widget rejected: ok=%v err=%v", ok, err)
	}
}

func TestTelegramWidgetTampered(t *testing.T) {
	bot := "123:ABC"
	data := map[string]string{"id": "42", "auth_date": strconv.FormatInt(time.Now().Unix(), 10), "first_name": "Bob"}
	data["hash"] = computeValidHash(bot, data)
	data["id"] = "999" // подмена после подписи
	if ok, _ := VerifyTelegramWidget(bot, data, DefaultTelegramAuthMaxAge); ok {
		t.Fatal("tampered data must be rejected")
	}
}

// TestTelegramWidgetStale проверяет FIX M-1: HMAC валиден (данные не
// подделаны), но auth_date старше maxAge — это replay ранее перехваченного
// корректного payload, он тоже должен быть отклонён.
func TestTelegramWidgetStale(t *testing.T) {
	bot := "123:ABC"
	staleAuthDate := time.Now().Add(-48 * time.Hour).Unix() // старше DefaultTelegramAuthMaxAge=24h
	data := map[string]string{"id": "42", "auth_date": strconv.FormatInt(staleAuthDate, 10), "first_name": "Bob"}
	data["hash"] = computeValidHash(bot, data) // HMAC честно валиден для этих (старых) данных

	ok, err := VerifyTelegramWidget(bot, data, DefaultTelegramAuthMaxAge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("stale (but validly signed) auth_date must be rejected as replay")
	}
}

// TestParseTelegramOAuthProfileDecodes доказывает суть небезопасного
// контраста: parseTelegramOAuthProfile принимает и корректно распаковывает
// payload JWT, подписанного ПРОИЗВОЛЬНЫМ ключом, никак не связанным с
// провайдером — подпись id_token здесь вообще не проверяется.
func TestParseTelegramOAuthProfileDecodes(t *testing.T) {
	claims := jwt.MapClaims{
		"sub":      "987654321",
		"name":     "Alice Ivanova",
		"username": "alice_tg",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	// Ключ подписи намеренно произвольный и никак не проверяется парсером —
	// это и демонстрирует контраст с VerifyTelegramWidget.
	signed, err := tok.SignedString([]byte("arbitrary-key-unrelated-to-any-provider"))
	if err != nil {
		t.Fatalf("failed to build test JWT: %v", err)
	}

	profile, err := parseTelegramOAuthProfile(signed)
	if err != nil {
		t.Fatalf("unexpected error decoding unverified id_token: %v", err)
	}
	if profile.Sub != "987654321" {
		t.Errorf("Sub = %q, want %q", profile.Sub, "987654321")
	}
	if profile.Name != "Alice Ivanova" {
		t.Errorf("Name = %q, want %q", profile.Name, "Alice Ivanova")
	}
	if profile.Username != "alice_tg" {
		t.Errorf("Username = %q, want %q", profile.Username, "alice_tg")
	}
}

// TestParseTelegramOAuthProfileRejectsMalformed проверяет, что заведомо
// битый вход возвращает ошибку, а не панику: неполные/отсутствующие
// сегменты, невалидный base64url в payload, синтаксически валидный JSON без
// обязательного поля sub.
func TestParseTelegramOAuthProfileRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"not a JWT at all":       "not-a-jwt",
		"only two segments":      "aGVhZGVy.cGF5bG9hZA",
		"invalid base64 payload": "aGVhZGVy.!!!not-base64!!!.c2ln",
		"empty string":           "",
		"missing sub claim":      "aGVhZGVy." + jwtEncodeSegment(t, map[string]any{"name": "No Sub"}) + ".c2ln",
	}

	for name, idToken := range cases {
		t.Run(name, func(t *testing.T) {
			profile, err := parseTelegramOAuthProfile(idToken)
			if err == nil {
				t.Fatalf("expected error for malformed id_token, got profile=%+v", profile)
			}
		})
	}
}

// jwtEncodeSegment кодирует claims как base64url-сегмент JWT (без padding),
// как это сделал бы реальный провайдер для payload-части токена.
func jwtEncodeSegment(t *testing.T, claims map[string]any) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims(claims))
	signed, err := tok.SignedString([]byte("k"))
	if err != nil {
		t.Fatalf("failed to build helper JWT: %v", err)
	}
	parts := strings.Split(signed, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected JWT shape: %q", signed)
	}
	return parts[1]
}
