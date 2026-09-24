package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrTelegramNoHash возвращается, когда в data отсутствует поле hash — без
// него проверить подлинность данных невозможно.
var ErrTelegramNoHash = errors.New("telegram: missing hash field")

// DefaultTelegramAuthMaxAge — максимально допустимый возраст auth_date в
// данных Telegram Login Widget. Виджет подписывает свой payload один раз в
// момент входа; без проверки свежести валидный (не подделанный!) payload,
// перехваченный когда-то раньше, можно было бы реиграть сколько угодно раз
// позже (replay корректной подписи). См. FIX M-1.
const DefaultTelegramAuthMaxAge = 24 * time.Hour

// VerifyTelegramWidget проверяет подлинность данных, присланных виджетом
// Telegram Login Widget (canonical-алгоритм из документации Telegram):
//
//  1. secret = sha256(botToken)
//  2. checkString — все пары key=value из data, КРОМЕ hash, отсортированные
//     по key лексикографически, соединённые через "\n"
//  3. mac = HMAC-SHA256(checkString, secret)
//  4. hex(mac) сравнивается с data["hash"] через hmac.Equal (защита от
//     timing-атак)
//  5. auth_date (unix-время подписи) проверяется на свежесть: если
//     now - auth_date > maxAge, payload отклоняется как replay — даже если
//     HMAC валиден, это не защищает от повторного предъявления давно
//     перехваченных данных.
//
// Возвращает true, если данные подписаны предъявленным botToken, не были
// изменены после подписи и не старше maxAge.
func VerifyTelegramWidget(botToken string, data map[string]string, maxAge time.Duration) (bool, error) {
	hash, ok := data["hash"]
	if !ok || hash == "" {
		return false, ErrTelegramNoHash
	}

	secret := sha256.Sum256([]byte(botToken))

	keys := make([]string, 0, len(data))
	for k := range data {
		if k == "hash" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+data[k])
	}
	checkString := strings.Join(parts, "\n")

	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(checkString))
	computed := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(computed), []byte(strings.ToLower(hash))) {
		return false, nil
	}

	authDateUnix, err := strconv.ParseInt(data["auth_date"], 10, 64)
	if err != nil {
		// Нет/битое auth_date у иначе валидно подписанного payload — тоже
		// отклоняем: без него свежесть проверить нельзя.
		return false, nil
	}
	if time.Since(time.Unix(authDateUnix, 0)) > maxAge {
		return false, nil
	}

	return true, nil
}

// TgProfile — минимальный профиль пользователя Telegram, извлекаемый из
// id_token Telegram Login (OAuth-вариант).
type TgProfile struct {
	Sub      string `json:"sub"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

// ErrTelegramBadIDToken возвращается, если id_token не является
// синтаксически корректным JWT (три сегмента, средний — валидный base64url JSON).
var ErrTelegramBadIDToken = errors.New("telegram: malformed id_token")

// parseTelegramOAuthProfile — КОНТРАСТНЫЙ вариант получения профиля Telegram:
// декодирует payload JWT (id_token) БЕЗ проверки подписи. В отличие от
// widget-HMAC, здесь подпись id_token провайдера не проверяется — приемлемо
// только если токен получен свежим напрямую от провайдера по TLS; для
// недоверенного источника это небезопасно.
//
// Используется только для демонстрации разницы подходов: widget-HMAC
// (canonical, проверяемый) vs "просто распаковать JWT" (небезопасно вне
// доверенного TLS-канала получения токена).
func parseTelegramOAuthProfile(idToken string) (TgProfile, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return TgProfile{}, ErrTelegramBadIDToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return TgProfile{}, ErrTelegramBadIDToken
	}

	var profile TgProfile
	if err := json.Unmarshal(payload, &profile); err != nil {
		return TgProfile{}, ErrTelegramBadIDToken
	}
	if profile.Sub == "" {
		return TgProfile{}, ErrTelegramBadIDToken
	}

	return profile, nil
}

// accountIDFromTelegramID детерминированно производит accountID из
// telegram-идентификатора (числовой id из виджета либо sub из id_token) для
// demo-целей: первые 8 байт sha256("tg:"+tgID) как положительное int64. В
// реальной системе это был бы lookup/insert по (provider="telegram", tgID)
// в таблице привязок внешних аккаунтов.
func accountIDFromTelegramID(tgID string) int64 {
	sum := sha256.Sum256([]byte("tg:" + tgID))
	return int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
}
