package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"

	"khorost.tech/cookbook/auth/internal/token"
)

const (
	issuer        = "khorost-auth-demo"
	recoveryCount = 10
	stepupTTL     = 5 * time.Minute
	totpPeriod    = 30
	totpSkew      = 1
	totpDigits    = otp.DigitsSix

	// totpRLLimit/totpRLWindow — анти-брутфорс для /totp/verify: 6-значный
	// код (пространство 10^6) при окне допуска ~60-90с (totpSkew) реально
	// перебираем без лимита попыток. Лимит считается по userID, а не по
	// исходному IP — demo-стенд не имеет доступа к реальной сети клиента.
	totpRLLimit  = 5
	totpRLWindow = 60 * time.Second
)

// ErrTOTPRateLimited возвращается Verify, когда для userID в пределах
// totpRLWindow уже накоплено totpRLLimit неудачных попыток. Пока окно не
// истечёт (счётчик totp:rl:{userID} не истёк по TTL), Verify отклоняет ЛЮБОЙ
// код — включая корректный: иначе атакующий мог бы использовать сам факт
// "лимит не сработал" как оракул при переборе. Успешная проверка (см. Verify)
// сбрасывает счётчик, так что легитимный пользователь, однажды введший
// верный код, лимитом не блокируется.
var ErrTOTPRateLimited = errors.New("totp: rate limited")

func totpKey(userID string) string {
	return "totp:" + userID
}

func totpRLKey(userID string) string {
	return "totp:rl:" + userID
}

// checkTOTPRateLimit сообщает, достигнут ли уже лимит неудачных попыток
// verify для userID в текущем окне.
func checkTOTPRateLimit(ctx context.Context, rdb *redis.Client, userID string) (bool, error) {
	n, err := rdb.Get(ctx, totpRLKey(userID)).Int()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, fmt.Errorf("check totp rate limit: %w", err)
	}
	return n >= totpRLLimit, nil
}

// bumpTOTPRateLimit инкрементирует счётчик неудачных попыток verify для
// userID и выставляет TTL=totpRLWindow при первом инкременте окна (INCR
// создаёт ключ с TTL=-1 — без Expire счётчик копился бы вечно).
func bumpTOTPRateLimit(ctx context.Context, rdb *redis.Client, userID string) error {
	key := totpRLKey(userID)
	n, err := rdb.Incr(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("bump totp rate limit: %w", err)
	}
	if n == 1 {
		if err := rdb.Expire(ctx, key, totpRLWindow).Err(); err != nil {
			return fmt.Errorf("start totp rate limit window: %w", err)
		}
	}
	return nil
}

// resetTOTPRateLimit сбрасывает счётчик неудачных попыток после успешного verify.
func resetTOTPRateLimit(ctx context.Context, rdb *redis.Client, userID string) error {
	if err := rdb.Del(ctx, totpRLKey(userID)).Err(); err != nil {
		return fmt.Errorf("reset totp rate limit: %w", err)
	}
	return nil
}

func stepupKey(sessionID string) string {
	return "stepup:" + sessionID
}

func hashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// Enroll генерирует TOTP-секрет и recovery-коды для userID, сохраняет
// секрет и хэши recovery-кодов в Redis (totp:{userID}) и возвращает
// секрет, otpauth:// URI (для QR) и recovery-коды в открытом виде —
// это единственный момент, когда они видны в открытом виде.
func Enroll(ctx context.Context, rdb *redis.Client, userID string) (secret, otpauthURI string, recovery []string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: userID,
		Period:      totpPeriod,
		Digits:      totpDigits,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return "", "", nil, fmt.Errorf("generate totp key: %w", err)
	}

	recovery = make([]string, recoveryCount)
	fields := map[string]interface{}{"secret": key.Secret()}
	for i := 0; i < recoveryCount; i++ {
		plain, err := token.OpaqueHex(8)
		if err != nil {
			return "", "", nil, fmt.Errorf("generate recovery code: %w", err)
		}
		recovery[i] = plain
		fields["recovery:"+hashRecoveryCode(plain)] = "1"
	}

	// Re-enrollment должен начинать с чистого листа: без Del прежние
	// recovery-хэши (и last_step анти-replay) пережили бы новую регистрацию
	// и старые одноразовые коды остались бы валидны вперемешку с новыми.
	if err := rdb.Del(ctx, totpKey(userID)).Err(); err != nil {
		return "", "", nil, fmt.Errorf("reset totp enrollment: %w", err)
	}
	if err := rdb.HSet(ctx, totpKey(userID), fields).Err(); err != nil {
		return "", "", nil, fmt.Errorf("store totp enrollment: %w", err)
	}

	return key.Secret(), key.URL(), recovery, nil
}

// acceptStepScript атомарно (Lua-скрипт исполняется в Redis однопоточно,
// без гонок между конкурентными Verify) сравнивает предъявленный time-step
// с last_step, ранее принятым для userID, и продвигает last_step вперёд.
// Возвращает 1, если step новее last_step (код принят впервые), и 0, если
// step уже <= last_step (replay — код на этом или более раннем step уже
// был использован).
var acceptStepScript = redis.NewScript(`
local last = redis.call('HGET', KEYS[1], 'last_step')
if last and tonumber(last) >= tonumber(ARGV[1]) then
    return 0
end
redis.call('HSET', KEYS[1], 'last_step', ARGV[1])
return 1
`)

// acceptStep пытается атомарно принять step как новый last_step для userID.
func acceptStep(ctx context.Context, rdb *redis.Client, userID string, step int64) (bool, error) {
	res, err := acceptStepScript.Run(ctx, rdb, []string{totpKey(userID)}, step).Int()
	if err != nil {
		return false, fmt.Errorf("advance totp last_step: %w", err)
	}
	return res == 1, nil
}

// Verify проверяет TOTP-код против секрета userID с окном допуска ±1 период
// (totpSkew) — компенсирует небольшой рассинхрон часов клиента — и защищает
// от replay: перебирает time-step'ы окна [now-skew, now, now+skew], сверяет
// ожидаемый код с предъявленным константным по времени сравнением
// (token.SafeEqual), и если совпадение найдено на step S, атомарно проверяет
// и продвигает last_step (см. acceptStep). Код с S <= last_step уже был
// использован ранее (или более поздний код уже принят) и отклоняется —
// без этого один и тот же код был бы валиден все ~60-90с окна допуска.
//
// Перед проверкой кода Verify сверяется со счётчиком неудачных попыток
// (totp:rl:{userID}, см. checkTOTPRateLimit) и при превышении totpRLLimit
// в пределах totpRLWindow возвращает ErrTOTPRateLimited, не читая и не
// проверяя код вовсе. Каждое отклонение кода (несовпадение или replay)
// увеличивает счётчик; успешная проверка его сбрасывает.
func Verify(ctx context.Context, rdb *redis.Client, userID, code string) (bool, error) {
	limited, err := checkTOTPRateLimit(ctx, rdb, userID)
	if err != nil {
		return false, err
	}
	if limited {
		return false, ErrTOTPRateLimited
	}

	secret, err := rdb.HGet(ctx, totpKey(userID), "secret").Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, fmt.Errorf("load totp secret: %w", err)
	}

	nowStep := time.Now().Unix() / totpPeriod

	matched := false
	var matchedStep int64
	for step := nowStep - totpSkew; step <= nowStep+totpSkew; step++ {
		expected, err := totp.GenerateCodeCustom(secret, time.Unix(step*totpPeriod, 0), totp.ValidateOpts{
			Period:    totpPeriod,
			Digits:    totpDigits,
			Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			return false, fmt.Errorf("generate totp code: %w", err)
		}
		if token.SafeEqual(expected, code) {
			matched = true
			matchedStep = step
			break
		}
	}
	if !matched {
		if err := bumpTOTPRateLimit(ctx, rdb, userID); err != nil {
			return false, err
		}
		return false, nil
	}

	accepted, err := acceptStep(ctx, rdb, userID, matchedStep)
	if err != nil {
		return false, err
	}
	if !accepted {
		if err := bumpTOTPRateLimit(ctx, rdb, userID); err != nil {
			return false, err
		}
		return false, nil
	}

	if err := resetTOTPRateLimit(ctx, rdb, userID); err != nil {
		return false, err
	}
	return true, nil
}

// UseRecovery сверяет sha256(code) с хранимыми хэшами recovery-кодов
// userID; при совпадении удаляет использованный код (одноразовость) и
// возвращает true. Повторное предъявление того же кода возвращает false.
func UseRecovery(ctx context.Context, rdb *redis.Client, userID, code string) (bool, error) {
	field := "recovery:" + hashRecoveryCode(code)
	exists, err := rdb.HExists(ctx, totpKey(userID), field).Result()
	if err != nil {
		return false, fmt.Errorf("check recovery code: %w", err)
	}
	if !exists {
		return false, nil
	}
	if err := rdb.HDel(ctx, totpKey(userID), field).Err(); err != nil {
		return false, fmt.Errorf("consume recovery code: %w", err)
	}
	return true, nil
}

// MarkStepUp помечает сессию sessionID как свежую по MFA-подтверждению —
// ставит stepup:{sessionID} с TTL stepupTTL.
func MarkStepUp(ctx context.Context, rdb *redis.Client, sessionID string) error {
	if err := rdb.Set(ctx, stepupKey(sessionID), "1", stepupTTL).Err(); err != nil {
		return fmt.Errorf("mark step-up: %w", err)
	}
	return nil
}

// RequireStepUp сообщает, есть ли для sessionID живое (не истёкшее) MFA-подтверждение.
func RequireStepUp(ctx context.Context, rdb *redis.Client, sessionID string) (bool, error) {
	n, err := rdb.Exists(ctx, stepupKey(sessionID)).Result()
	if err != nil {
		return false, fmt.Errorf("check step-up: %w", err)
	}
	return n > 0, nil
}
