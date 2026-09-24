package main

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"khorost.tech/cookbook/auth/internal/token"
)

const (
	sessionTTL    = 7 * 24 * time.Hour
	touchInterval = 20 * time.Second
)

// ErrNoSession означает, что сессия с данным sid не найдена (отсутствует или истекла).
var ErrNoSession = errors.New("session: not found")

// SessionView — представление сессии для отдачи наружу (список активных сессий пользователя).
type SessionView struct {
	SID        string
	UserID     string
	CreatedAt  string
	LastActive string
}

// CreateSession создаёт новую opaque-сессию для пользователя userID.
// Хранится в HASH s:{sid} (поля uid, cat, lat) + ссылка в SET su:{uid}.
func CreateSession(ctx context.Context, rdb *redis.Client, userID string) (string, error) {
	sid, err := token.OpaqueHex(16)
	if err != nil {
		return "", err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	sKey := "s:" + sid
	suKey := "su:" + userID

	pipe := rdb.TxPipeline()
	pipe.HSet(ctx, sKey, map[string]interface{}{
		"uid": userID,
		"cat": now,
		"lat": now,
	})
	pipe.Expire(ctx, sKey, sessionTTL)
	pipe.SAdd(ctx, suKey, sid)
	pipe.Expire(ctx, suKey, sessionTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", err
	}

	return sid, nil
}

// ValidateAndTouch проверяет существование сессии sid и обновляет отметку последней
// активности (lat), но не чаще, чем раз в touchInterval — throttle, чтобы не писать
// в Redis на каждый запрос.
func ValidateAndTouch(ctx context.Context, rdb *redis.Client, sid string) (string, error) {
	sKey := "s:" + sid
	data, err := rdb.HGetAll(ctx, sKey).Result()
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", ErrNoSession
	}

	uid := data["uid"]

	if lat, ok := data["lat"]; ok {
		if last, perr := time.Parse(time.RFC3339, lat); perr == nil {
			if time.Since(last) < touchInterval {
				return uid, nil
			}
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	pipe := rdb.TxPipeline()
	pipe.HSet(ctx, sKey, "lat", now)
	pipe.Expire(ctx, sKey, sessionTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", err
	}

	return uid, nil
}

// ListSessions возвращает живые сессии пользователя userID. Ссылки на уже истёкшие
// сессии (HASH исчез по TTL, но sid остался в su:{uid}) самоочищаются через SRem.
func ListSessions(ctx context.Context, rdb *redis.Client, userID string) ([]SessionView, error) {
	suKey := "su:" + userID
	sids, err := rdb.SMembers(ctx, suKey).Result()
	if err != nil {
		return nil, err
	}
	if len(sids) == 0 {
		return nil, nil
	}

	pipe := rdb.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(sids))
	for i, sid := range sids {
		cmds[i] = pipe.HGetAll(ctx, "s:"+sid)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}

	views := make([]SessionView, 0, len(sids))
	var stale []string
	for i, sid := range sids {
		data := cmds[i].Val()
		if len(data) == 0 {
			stale = append(stale, sid)
			continue
		}
		views = append(views, SessionView{
			SID:        sid,
			UserID:     data["uid"],
			CreatedAt:  data["cat"],
			LastActive: data["lat"],
		})
	}

	if len(stale) > 0 {
		rdb.SRem(ctx, suKey, toInterfaceSlice(stale)...)
	}

	return views, nil
}

// DeleteSession удаляет сессию sid и её ссылку из su:{userID}, только если sid
// принадлежит userID. Если sid не найден в su:{userID} (чужая или несуществующая
// сессия), возвращает ErrNoSession и НЕ трогает s:{sid}.
func DeleteSession(ctx context.Context, rdb *redis.Client, sid, userID string) error {
	owned, err := rdb.SIsMember(ctx, "su:"+userID, sid).Result()
	if err != nil {
		return err
	}
	if !owned {
		return ErrNoSession
	}

	pipe := rdb.TxPipeline()
	pipe.Del(ctx, "s:"+sid)
	pipe.SRem(ctx, "su:"+userID, sid)
	_, err = pipe.Exec(ctx)
	return err
}

func toInterfaceSlice(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
