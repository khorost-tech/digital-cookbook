// Package session управляет opaque refresh-сессиями в Redis и выпуском пары
// access (JWT, короткоживущий) + refresh (непрозрачный, долгоживущий) токенов
// для auth-сервиса. Consumer-сервисы session не используют — они валидируют
// access-токен локально через internal/jwtclaims.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"khorost.tech/cookbook/auth/internal/jwtclaims"
	"khorost.tech/cookbook/auth/internal/token"
)

const (
	// AccessTTL — время жизни JWT access-токена.
	AccessTTL = 15 * time.Minute
	// RefreshTTL — время жизни opaque refresh-токена (и per-field TTL записи
	// в индексе rsu:{accountID}).
	RefreshTTL = 168 * time.Hour

	touchInterval = 5 * time.Minute
)

// ErrNoSession означает, что refresh-сессия не найдена (отсутствует или истекла).
var ErrNoSession = errors.New("session: not found")

// Account — минимальные данные аккаунта, нужные, чтобы выпустить токены.
type Account struct {
	ID    int64
	Nick  string
	Roles []string
}

// CreateTokenPair выпускает пару access/refresh для acc. access — JWT
// (jwtclaims.Issue, HS256, AccessTTL). refresh — непрозрачный токен
// (token.OpaqueHex(32)), хранимый в HASH rs:{refresh} (поля aid, cat, lat, lm,
// nick, roles) с TTL RefreshTTL. Поля nick/roles дублируют то, что уже
// зашито в access, — это позволяет AccountFromSession восстановить полный
// Account при refresh/rotation, не обращаясь к источнику правды (БД/lookup),
// которого в demo-стенде нет: без этого следующий access после refresh
// выпускался бы с пустыми nick/roles, и RequireRole молча отклонял бы запросы.
// roles сериализуется как JSON-массив строк (пустой/nil Roles → "null",
// AccountFromSession читает это обратно как nil-срез).
// Дополнительно refresh регистрируется как поле в индексе
// rsu:{accountID} (HASH field=refresh, value="1") с той же per-field TTL —
// это позволяет перечислить все живые сессии аккаунта (GET /auth/sessions)
// без SCAN по всему keyspace.
func CreateTokenPair(ctx context.Context, rdb *redis.Client, secret []byte, acc Account, loginMethod string) (access, refresh string, err error) {
	access, err = jwtclaims.Issue(secret, jwtclaims.Claims{
		AccountID: acc.ID,
		Nick:      acc.Nick,
		Roles:     acc.Roles,
	}, AccessTTL)
	if err != nil {
		return "", "", err
	}

	refresh, err = token.OpaqueHex(32)
	if err != nil {
		return "", "", err
	}

	rolesJSON, err := json.Marshal(acc.Roles)
	if err != nil {
		return "", "", err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	rsKey := "rs:" + refresh
	rsuKey := "rsu:" + strconv.FormatInt(acc.ID, 10)

	pipe := rdb.TxPipeline()
	pipe.HSet(ctx, rsKey, map[string]interface{}{
		"aid":   acc.ID,
		"cat":   now,
		"lat":   now,
		"lm":    loginMethod,
		"nick":  acc.Nick,
		"roles": string(rolesJSON),
	})
	pipe.Expire(ctx, rsKey, RefreshTTL)
	pipe.HSet(ctx, rsuKey, refresh, "1")
	if _, err := pipe.Exec(ctx); err != nil {
		return "", "", err
	}

	// per-field TTL на rsu: — отдельной командой, т.к. HExpire не входит в
	// стандартный конвейер большинства клиентских обёрток так же гладко, как
	// HSet/Expire; ошибка здесь не должна ронять создание сессии (индекс —
	// вспомогательная структура, источник истины — rs:{refresh}).
	if err := rdb.HExpire(ctx, rsuKey, RefreshTTL, refresh).Err(); err != nil {
		return "", "", err
	}

	return access, refresh, nil
}

// ValidateAndTouch проверяет, что refresh-сессия жива, и продлевает её
// активность. lat (last-active) обновляется не чаще, чем раз в touchInterval,
// — throttle, чтобы не писать в Redis на каждый refresh-запрос. Возвращает
// AccountID владельца сессии.
func ValidateAndTouch(ctx context.Context, rdb *redis.Client, refresh string) (int64, error) {
	rsKey := "rs:" + refresh
	data, err := rdb.HGetAll(ctx, rsKey).Result()
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, ErrNoSession
	}

	aid, err := strconv.ParseInt(data["aid"], 10, 64)
	if err != nil {
		return 0, err
	}

	if lat, ok := data["lat"]; ok {
		if last, perr := time.Parse(time.RFC3339, lat); perr == nil {
			if time.Since(last) < touchInterval {
				return aid, nil
			}
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	pipe := rdb.TxPipeline()
	pipe.HSet(ctx, rsKey, "lat", now)
	pipe.Expire(ctx, rsKey, RefreshTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}

	return aid, nil
}

// AccountFromSession читает Account (ID, Nick, Roles) из HASH rs:{refresh}.
// Используется при refresh/rotation, чтобы новый access-токен нёс те же
// claims, что были выданы при логине (см. CreateTokenPair). ErrNoSession —
// если rs:{refresh} не найден (сессия истекла или удалена).
func AccountFromSession(ctx context.Context, rdb *redis.Client, refresh string) (Account, error) {
	rsKey := "rs:" + refresh
	data, err := rdb.HGetAll(ctx, rsKey).Result()
	if err != nil {
		return Account{}, err
	}
	if len(data) == 0 {
		return Account{}, ErrNoSession
	}

	aid, err := strconv.ParseInt(data["aid"], 10, 64)
	if err != nil {
		return Account{}, err
	}

	var roles []string
	if r, ok := data["roles"]; ok && r != "" {
		if err := json.Unmarshal([]byte(r), &roles); err != nil {
			return Account{}, err
		}
	}

	return Account{ID: aid, Nick: data["nick"], Roles: roles}, nil
}

// SessionView — представление refresh-сессии для отдачи наружу (GET /auth/sessions).
type SessionView struct {
	Refresh     string `json:"refresh"`
	CreatedAt   string `json:"created_at"`
	LastActive  string `json:"last_active"`
	LoginMethod string `json:"login_method"`
}

// ListSessions возвращает живые refresh-сессии аккаунта accountID, читая
// список из индекса rsu:{accountID} и разворачивая каждую запись из
// rs:{refresh}. Ссылки на уже истёкшие сессии (rs:{refresh} исчез по TTL, но
// поле осталось в rsu:) самоочищаются через HDel.
func ListSessions(ctx context.Context, rdb *redis.Client, accountID int64) ([]SessionView, error) {
	rsuKey := "rsu:" + strconv.FormatInt(accountID, 10)
	refreshes, err := rdb.HKeys(ctx, rsuKey).Result()
	if err != nil {
		return nil, err
	}
	if len(refreshes) == 0 {
		return nil, nil
	}

	pipe := rdb.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(refreshes))
	for i, refresh := range refreshes {
		cmds[i] = pipe.HGetAll(ctx, "rs:"+refresh)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}

	views := make([]SessionView, 0, len(refreshes))
	var stale []string
	for i, refresh := range refreshes {
		data := cmds[i].Val()
		if len(data) == 0 {
			stale = append(stale, refresh)
			continue
		}
		views = append(views, SessionView{
			Refresh:     refresh,
			CreatedAt:   data["cat"],
			LastActive:  data["lat"],
			LoginMethod: data["lm"],
		})
	}

	if len(stale) > 0 {
		rdb.HDel(ctx, rsuKey, stale...)
	}

	return views, nil
}
