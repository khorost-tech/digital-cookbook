package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"khorost.tech/cookbook/auth/internal/token"
)

const (
	codeTTL = 5 * time.Minute

	rateLimitMinTTL = 60 * time.Second
	rateLimitHrTTL  = time.Hour

	perMinLimit = 5
	perHrLimit  = 20
	maxAttempts = 5
)

// ErrRateLimited возвращается, когда для email превышен лимит запросов кода
// (за минуту или за час).
var ErrRateLimited = errors.New("auth: rate limited")

// ErrTooManyAttempts возвращается, когда для email исчерпан лимит попыток
// ввода кода — pending-запись удаляется, пользователю нужно запросить новый код.
var ErrTooManyAttempts = errors.New("auth: too many attempts")

// ErrInvalidCode возвращается, если код не совпал или pending-запись не найдена
// (в т.ч. истекла).
var ErrInvalidCode = errors.New("auth: invalid code")

// userIDFromEmail детерминированно производит userID из email для demo-целей:
// hex(sha256(email))[:16]. В реальной системе это был бы lookup/insert в таблице
// пользователей; здесь — чтобы не тащить БД в demo-стенд.
func userIDFromEmail(email string) string {
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:])[:16]
}

// SendCode проверяет rate-limit по email и, если он не превышен, генерирует
// одноразовый magic-code и непрозрачный токен подтверждения, сохраняя их в
// HASH auth:pending:{email} (token, code, attempts) на codeTTL. Повторный
// вызов перезаписывает pending-запись — предыдущий код становится недействителен.
func SendCode(ctx context.Context, rdb *redis.Client, email string) error {
	limited, err := checkAndBumpRateLimit(ctx, rdb, email)
	if err != nil {
		return err
	}
	if limited {
		return ErrRateLimited
	}

	tok, err := token.OpaqueHex(32)
	if err != nil {
		return err
	}
	code, err := token.Code()
	if err != nil {
		return err
	}

	pendingKey := "auth:pending:" + email

	pipe := rdb.TxPipeline()
	pipe.HSet(ctx, pendingKey, map[string]interface{}{
		"token":    tok,
		"code":     code,
		"attempts": 0,
	})
	pipe.Expire(ctx, pendingKey, codeTTL)
	_, err = pipe.Exec(ctx)
	return err
}

// checkAndBumpRateLimit проверяет минутный и часовой лимиты запросов кода для email.
// Если оба в пределах — инкрементирует оба счётчика и возвращает limited=false.
// Если хотя бы один уже достиг лимита — не инкрементирует ничего и возвращает limited=true.
func checkAndBumpRateLimit(ctx context.Context, rdb *redis.Client, email string) (limited bool, err error) {
	minKey := "auth:rl:" + email + ":min"
	hrKey := "auth:rl:" + email + ":hour"

	minCount, err := getCounter(ctx, rdb, minKey)
	if err != nil {
		return false, err
	}
	if minCount >= perMinLimit {
		return true, nil
	}

	hrCount, err := getCounter(ctx, rdb, hrKey)
	if err != nil {
		return false, err
	}
	if hrCount >= perHrLimit {
		return true, nil
	}

	pipe := rdb.TxPipeline()
	pipe.Incr(ctx, minKey)
	pipe.Expire(ctx, minKey, rateLimitMinTTL)
	pipe.Incr(ctx, hrKey)
	pipe.Expire(ctx, hrKey, rateLimitHrTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}

	return false, nil
}

func getCounter(ctx context.Context, rdb *redis.Client, key string) (int, error) {
	val, err := rdb.Get(ctx, key).Int()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return val, nil
}

// VerifyCode проверяет код, введённый для email, против pending-записи
// auth:pending:{email}. Счётчик attempts инкрементируется на КАЖДУЮ неудачную
// попытку (в т.ч. когда код в принципе не совпадает), что реально ограничивает
// перебор — в отличие от схемы "индекс по присланному коду", где неверный код
// просто не находил ключ и попытка не засчитывалась.
//
// При успехе pending-запись удаляется и возвращается детерминированный userID.
// При достижении maxAttempts pending-запись удаляется и возвращается
// ErrTooManyAttempts. При несовпадении кода счётчик увеличивается (TTL записи
// сохраняется) и возвращается ErrInvalidCode.
func VerifyCode(ctx context.Context, rdb *redis.Client, email, code string) (string, error) {
	pendingKey := "auth:pending:" + email

	data, err := rdb.HGetAll(ctx, pendingKey).Result()
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", ErrInvalidCode
	}

	attempts, _ := strconv.Atoi(data["attempts"])
	if attempts >= maxAttempts {
		rdb.Del(ctx, pendingKey)
		return "", ErrTooManyAttempts
	}

	if !token.SafeEqual(code, data["code"]) {
		if err := rdb.HIncrBy(ctx, pendingKey, "attempts", 1).Err(); err != nil {
			return "", err
		}
		return "", ErrInvalidCode
	}

	rdb.Del(ctx, pendingKey)
	return userIDFromEmail(email), nil
}
