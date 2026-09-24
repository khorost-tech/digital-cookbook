// Package main (login-methods) — беспарольный вход по одноразовому email-коду
// (OTP), OAuth поверх встроенного mock-OIDC-провайдера (state+PKCE) и два
// варианта входа через Telegram (canonical widget-HMAC и контрастный
// небезопасный OAuth-вариант). Все методы сходятся к единой паре токенов
// через internal/session.CreateTokenPair.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"khorost.tech/cookbook/auth/internal/token"
)

const (
	// otpTTL — время жизни pending-записи auth:{email} (и per-field TTL её полей).
	otpTTL = 5 * time.Minute

	otpRateLimitMinTTL = 60 * time.Second
	otpRateLimitHrTTL  = time.Hour

	otpRateLimitMin  = 3
	otpRateLimitHour = 10

	maxOTPAttempts = 5
)

// ErrOTPRateLimited возвращается, когда для email превышен лимит запросов
// кода (за минуту — otpRateLimitMin, за час — otpRateLimitHour).
var ErrOTPRateLimited = errors.New("otp: rate limited")

// ErrTooManyAttempts возвращается, когда для email исчерпан лимит попыток
// ввода кода (maxOTPAttempts) — pending-запись удаляется, нужен новый код.
var ErrTooManyAttempts = errors.New("otp: too many attempts")

// ErrInvalidCode возвращается, если код не совпал или pending-запись не
// найдена (в т.ч. истекла по TTL).
var ErrInvalidCode = errors.New("otp: invalid code")

// RequestOTP проверяет rate-limit по email и, если он не превышен, генерирует
// одноразовый код и сохраняет его в HASH auth:{email} (поля token, code,
// attempts) с per-field TTL otpTTL (HExpire — а не Expire на весь ключ,
// как в исходной версии single-service: это гарантирует, что TTL относится
// именно к текущей pending-попытке, даже если ключ переиспользуется полями
// из смежных операций). Повторный вызов перезаписывает pending-запись —
// предыдущий код становится недействителен.
func RequestOTP(ctx context.Context, rdb *redis.Client, email string) error {
	limited, err := checkAndBumpOTPRateLimit(ctx, rdb, email)
	if err != nil {
		return err
	}
	if limited {
		return ErrOTPRateLimited
	}

	tok, err := token.OpaqueHex(32)
	if err != nil {
		return err
	}
	code, err := token.Code()
	if err != nil {
		return err
	}

	key := "auth:" + email
	pipe := rdb.TxPipeline()
	pipe.HSet(ctx, key, map[string]interface{}{
		"token":    tok,
		"code":     code,
		"attempts": 0,
	})
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}

	if err := rdb.HExpire(ctx, key, otpTTL, "token", "code", "attempts").Err(); err != nil {
		return err
	}

	return nil
}

// checkAndBumpOTPRateLimit проверяет минутный и часовой лимиты запросов кода
// для email. Если оба в пределах — инкрементирует оба счётчика и возвращает
// limited=false. Если хотя бы один уже достиг лимита — не инкрементирует
// ничего и возвращает limited=true.
func checkAndBumpOTPRateLimit(ctx context.Context, rdb *redis.Client, email string) (limited bool, err error) {
	minKey := "auth:rl:" + email + ":min"
	hrKey := "auth:rl:" + email + ":hour"

	minCount, err := getOTPCounter(ctx, rdb, minKey)
	if err != nil {
		return false, err
	}
	if minCount >= otpRateLimitMin {
		return true, nil
	}

	hrCount, err := getOTPCounter(ctx, rdb, hrKey)
	if err != nil {
		return false, err
	}
	if hrCount >= otpRateLimitHour {
		return true, nil
	}

	pipe := rdb.TxPipeline()
	pipe.Incr(ctx, minKey)
	pipe.Expire(ctx, minKey, otpRateLimitMinTTL)
	pipe.Incr(ctx, hrKey)
	pipe.Expire(ctx, hrKey, otpRateLimitHrTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}

	return false, nil
}

func getOTPCounter(ctx context.Context, rdb *redis.Client, key string) (int, error) {
	val, err := rdb.Get(ctx, key).Int()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return val, nil
}

// VerifyOTP проверяет код, введённый для email, против pending-записи
// auth:{email}. Счётчик attempts инкрементируется на КАЖДУЮ неудачную
// попытку (в т.ч. когда pending-запись не найдена вовсе не считается —
// codeMismatch инкрементируется только когда запись есть, но код не совпал),
// что реально ограничивает перебор кода злоумышленником, знающим email.
//
// При успехе pending-запись удаляется и возвращается детерминированный
// accountID (см. accountIDFromEmail). При достижении maxOTPAttempts
// pending-запись удаляется и возвращается ErrTooManyAttempts. При
// несовпадении кода счётчик увеличивается (TTL полей не трогается —
// HIncrBy не сбрасывает TTL, выставленный HExpire) и возвращается
// ErrInvalidCode.
func VerifyOTP(ctx context.Context, rdb *redis.Client, email, code string) (int64, error) {
	key := "auth:" + email

	data, err := rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, ErrInvalidCode
	}

	attempts, _ := strconv.Atoi(data["attempts"])
	if attempts >= maxOTPAttempts {
		rdb.Del(ctx, key)
		return 0, ErrTooManyAttempts
	}

	if !token.SafeEqual(code, data["code"]) {
		if err := rdb.HIncrBy(ctx, key, "attempts", 1).Err(); err != nil {
			return 0, err
		}
		return 0, ErrInvalidCode
	}

	rdb.Del(ctx, key)
	return accountIDFromEmail(email), nil
}

// accountIDFromEmail детерминированно производит accountID из email для
// demo-целей: первые 8 байт sha256(email) как положительное int64. В реальной
// системе это был бы lookup/insert в таблице пользователей; здесь — чтобы
// не тащить БД в demo-стенд (тот же приём, что в multi-service/authservice).
func accountIDFromEmail(email string) int64 {
	sum := sha256.Sum256([]byte(email))
	return int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
}
