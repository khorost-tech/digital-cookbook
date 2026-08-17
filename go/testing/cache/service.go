// Package cache реализует read-through кэш профилей пользователей поверх
// произвольного репозитория и произвольного кэша (интерфейсы позволяют
// подставлять фейки/моки в тестах).
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Profile — профиль пользователя.
type Profile struct {
	ID    int64
	Name  string
	Email string
}

// ProfileRepository — источник истины для профилей (например, БД).
type ProfileRepository interface {
	GetByID(ctx context.Context, id int64) (Profile, error)
}

// Cache — абстракция над кэширующим хранилищем ключ-значение.
type Cache interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, val string, ttl time.Duration) error
}

// ErrNotFound возвращается ProfileRepository, если профиль не найден.
var ErrNotFound = errors.New("profile not found")

// CacheKey строит ключ кэша для профиля пользователя с данным id.
func CacheKey(id int64) string {
	return fmt.Sprintf("user:%d", id)
}

// Service — сервис read-through кэша профилей.
type Service struct {
	Repo  ProfileRepository
	Cache Cache
	TTL   time.Duration
}

// GetProfile возвращает профиль пользователя. Второй возврат fromCache
// показывает, был ли профиль взят из кэша (true) или загружен из
// репозитория (false).
//
// Логика read-through:
//  1. Пробуем Cache.Get по CacheKey(id).
//  2. При попадании — декодируем JSON и возвращаем (p, true, nil).
//  3. При промахе — идём в Repo.GetByID; если репозиторий вернул
//     ErrNotFound, пробрасываем её как есть.
//  4. После успешного чтения из репозитория — кладём профиль в кэш с TTL.
//     Ошибка записи в кэш не приводит к ошибке запроса (кэш — best-effort),
//     в проде такая ошибка должна логироваться/метриться.
func (s *Service) GetProfile(ctx context.Context, id int64) (Profile, bool, error) {
	key := CacheKey(id)

	if raw, ok, err := s.Cache.Get(ctx, key); err == nil && ok {
		var p Profile
		if err := json.Unmarshal([]byte(raw), &p); err == nil {
			return p, true, nil
		}
		// Битые данные в кэше — не фейлим запрос, идём в репозиторий.
	}

	p, err := s.Repo.GetByID(ctx, id)
	if err != nil {
		return Profile{}, false, err
	}

	if raw, err := json.Marshal(p); err == nil {
		if err := s.Cache.Set(ctx, key, string(raw), s.TTL); err != nil {
			// в проде — лог/метрика; здесь просто игнор, кэш best-effort
			_ = err
		}
	}

	return p, false, nil
}
