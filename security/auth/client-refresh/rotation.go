// Command client-refresh — флагманский demo-сервис ротации refresh-токена:
// grace-period, распределённый refresh-lock (single-flight на аккаунт) и
// replay-successor, чтобы гонка нескольких вкладок/устройств не приводила к
// ложному разлогину. Контраст: RotateNaive — та же операция без lock/grace,
// используется только для замера проблемы «до».
package main

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"khorost.tech/cookbook/auth/internal/jwtclaims"
	"khorost.tech/cookbook/auth/internal/session"
)

const (
	// graceTTL — сколько живёт мост rotated:{oldRefresh} → newRefresh после
	// ротации. Опоздавший запрос старым токеном в этом окне получает того
	// же преемника (replay), а не разлогин.
	graceTTL = 30 * time.Second
	// lockTTL — TTL распределённого refresh-lock rl:{accountID}. Защита от
	// зависшего держателя лока (например, процесс упал между Acquire и
	// Release) — лок сам снимется через lockTTL.
	lockTTL = 5 * time.Second
)

// ErrRefreshInProgress — конкурентный refresh для этого аккаунта уже
// выполняется (проигранный single-flight lock). Вызывающий код (HTTP-слой)
// отображает это в 429 с Retry-After.
var ErrRefreshInProgress = errors.New("client-refresh: refresh already in progress")

// ErrTokenInvalid — refresh-токен не найден ни как живая сессия, ни как
// действующий мост grace-периода. Настоящий разлогин.
var ErrTokenInvalid = errors.New("client-refresh: refresh token invalid")

// AcquireRefreshLock пытается взять single-flight lock на ротацию для
// accountID. true — лок взят этим вызовом, false — лок уже удерживается
// другим запросом.
func AcquireRefreshLock(ctx context.Context, rdb *redis.Client, accountID int64) (bool, error) {
	key := "rl:" + strconv.FormatInt(accountID, 10)
	ok, err := rdb.SetNX(ctx, key, "1", lockTTL).Result()
	if err != nil {
		return false, err
	}
	return ok, nil
}

// ReleaseRefreshLock снимает lock, взятый AcquireRefreshLock.
func ReleaseRefreshLock(ctx context.Context, rdb *redis.Client, accountID int64) error {
	key := "rl:" + strconv.FormatInt(accountID, 10)
	return rdb.Del(ctx, key).Err()
}

// SetRotation регистрирует мост rotated:{oldRefresh} = "{newRefresh}:{accountID}"
// с TTL ttl (grace-период). Опоздавшие запросы старым токеном в это окно
// находят его через GetRotation вместо ложного разлогина.
func SetRotation(ctx context.Context, rdb *redis.Client, oldRefresh, newRefresh string, accountID int64, ttl time.Duration) error {
	key := "rotated:" + oldRefresh
	val := newRefresh + ":" + strconv.FormatInt(accountID, 10)
	return rdb.Set(ctx, key, val, ttl).Err()
}

// GetRotation читает мост rotated:{oldRefresh}. Значение хранится как
// "{newRefresh}:{accountID}" — парсим по ПОСЛЕДНЕМУ ":" (refresh — hex без
// ":", accountID — число), это безопасно даже если newRefresh случайно
// содержал бы ":".
func GetRotation(ctx context.Context, rdb *redis.Client, oldRefresh string) (newRefresh string, accountID int64, found bool) {
	key := "rotated:" + oldRefresh
	val, err := rdb.Get(ctx, key).Result()
	if err != nil {
		return "", 0, false
	}

	idx := strings.LastIndex(val, ":")
	if idx < 0 {
		return "", 0, false
	}

	newRefresh = val[:idx]
	aid, err := strconv.ParseInt(val[idx+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}

	return newRefresh, aid, true
}

// Rotate — идемпотентная ротация refresh-токена с grace-period,
// распределённым refresh-lock (single-flight на аккаунт) и
// replay-successor.
//
// Алгоритм:
//  1. ValidateAndTouch(oldRefresh). Если сессия жива — блок ротации (2-4).
//     Если сессии нет (ErrNoSession) — replayRotated (5). Иная ошибка —
//     вернуть её как есть.
//  2. Lock: AcquireRefreshLock(accountID). Проигравший (lock уже занят) →
//     ErrRefreshInProgress. Победитель освобождает лок через defer.
//  3. Повторная проверка под локом: если конкурент уже успел
//     ротировать oldRefresh между шагом 1 (validate) и взятием лока —
//     GetRotation найдёт мост. В этом случае просто выпускаем свежий
//     access для уже существующего преемника — новую сессию НЕ создаём.
//  4. Иначе — собственно ротация: новая сессия (CreateTokenPair),
//     мост rotated:{old}→new (non-fatal при ошибке — не роняет ротацию),
//     удаление старой сессии (rs:{old} + запись в rsu:{aid}).
//  5. replayRotated: если мост для oldRefresh существует (например,
//     сессия уже была удалена другим победителем гонки, а этот запрос
//     пришёл позже) — вернуть того же преемника. Если моста тоже нет —
//     настоящий разлогин, ErrTokenInvalid.
func Rotate(ctx context.Context, rdb *redis.Client, secret []byte, oldRefresh string) (access, newRefresh string, err error) {
	aid, err := session.ValidateAndTouch(ctx, rdb, oldRefresh)
	if err != nil {
		if errors.Is(err, session.ErrNoSession) {
			return replayRotated(ctx, rdb, secret, oldRefresh)
		}
		return "", "", err
	}

	ok, lockErr := AcquireRefreshLock(ctx, rdb, aid)
	if lockErr != nil {
		return "", "", lockErr
	}
	if !ok {
		return "", "", ErrRefreshInProgress
	}
	defer func() {
		if relErr := ReleaseRefreshLock(ctx, rdb, aid); relErr != nil {
			slog.Error("rotate: release refresh lock failed", "aid", aid, "err", relErr)
		}
	}()

	// Повторная проверка под локом: конкурент мог ротировать oldRefresh
	// между ValidateAndTouch и взятием лока.
	if existingNew, _, found := GetRotation(ctx, rdb, oldRefresh); found {
		// Старая сессия конкурентом уже удалена — полный Account читаем из
		// НОВОЙ (уже созданной победителем) сессии, а не из oldRefresh.
		acc, acctErr := session.AccountFromSession(ctx, rdb, existingNew)
		if acctErr != nil {
			return "", "", acctErr
		}
		freshAccess, issueErr := jwtclaims.Issue(secret, jwtclaims.Claims{
			AccountID: acc.ID,
			Nick:      acc.Nick,
			Roles:     acc.Roles,
		}, session.AccessTTL)
		if issueErr != nil {
			return "", "", issueErr
		}
		return freshAccess, existingNew, nil
	}

	// Полный Account читаем из oldRefresh ДО его удаления ниже — иначе
	// новая сессия (и, соответственно, следующий access после неё) осталась
	// бы без nick/roles.
	acc, acctErr := session.AccountFromSession(ctx, rdb, oldRefresh)
	if acctErr != nil {
		return "", "", acctErr
	}

	newAccess, newRef, createErr := session.CreateTokenPair(ctx, rdb, secret, acc, "refresh")
	if createErr != nil {
		return "", "", createErr
	}

	if rotErr := SetRotation(ctx, rdb, oldRefresh, newRef, aid, graceTTL); rotErr != nil {
		slog.Error("rotate: set rotation bridge failed", "aid", aid, "err", rotErr)
	}

	if delErr := rdb.Del(ctx, "rs:"+oldRefresh).Err(); delErr != nil {
		slog.Error("rotate: delete old session failed", "aid", aid, "err", delErr)
	}
	rsuKey := "rsu:" + strconv.FormatInt(aid, 10)
	if delErr := rdb.HDel(ctx, rsuKey, oldRefresh).Err(); delErr != nil {
		slog.Error("rotate: delete old session index failed", "aid", aid, "err", delErr)
	}

	return newAccess, newRef, nil
}

// replayRotated обслуживает опоздавший запрос старым токеном, чья сессия
// уже удалена: если мост grace-периода ещё жив — возвращает того же
// преемника (нет новой сессии, нет фантомов, нет разлогина). Если моста
// тоже нет — настоящий разлогин.
func replayRotated(ctx context.Context, rdb *redis.Client, secret []byte, oldRefresh string) (access, newRefresh string, err error) {
	newRef, _, found := GetRotation(ctx, rdb, oldRefresh)
	if !found {
		return "", "", ErrTokenInvalid
	}

	// oldRefresh уже удалён — полный Account читаем из НОВОЙ сессии.
	acc, acctErr := session.AccountFromSession(ctx, rdb, newRef)
	if acctErr != nil {
		return "", "", acctErr
	}

	freshAccess, issueErr := jwtclaims.Issue(secret, jwtclaims.Claims{
		AccountID: acc.ID,
		Nick:      acc.Nick,
		Roles:     acc.Roles,
	}, session.AccessTTL)
	if issueErr != nil {
		return "", "", issueErr
	}
	return freshAccess, newRef, nil
}

// RotateNaive — та же операция БЕЗ lock и БЕЗ grace/replay. Используется
// только как контраст для замера проблемы, которую решает Rotate: без
// single-flight конкурентный refresh тем же токеном может привести к
// ложным разлогинам у части параллельных запросов.
func RotateNaive(ctx context.Context, rdb *redis.Client, secret []byte, oldRefresh string) (access, newRefresh string, err error) {
	aid, err := session.ValidateAndTouch(ctx, rdb, oldRefresh)
	if err != nil {
		if errors.Is(err, session.ErrNoSession) {
			return "", "", ErrTokenInvalid
		}
		return "", "", err
	}

	newAccess, newRef, createErr := session.CreateTokenPair(ctx, rdb, secret, session.Account{ID: aid}, "refresh")
	if createErr != nil {
		return "", "", createErr
	}

	if delErr := rdb.Del(ctx, "rs:"+oldRefresh).Err(); delErr != nil {
		slog.Error("rotate-naive: delete old session failed", "aid", aid, "err", delErr)
	}
	rsuKey := "rsu:" + strconv.FormatInt(aid, 10)
	if delErr := rdb.HDel(ctx, rsuKey, oldRefresh).Err(); delErr != nil {
		slog.Error("rotate-naive: delete old session index failed", "aid", aid, "err", delErr)
	}

	return newAccess, newRef, nil
}
