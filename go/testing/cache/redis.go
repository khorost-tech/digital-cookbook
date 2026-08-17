package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisCache — адаптер Cache поверх клиента go-redis.
type redisCache struct {
	rdb *redis.Client
}

// NewRedisCache создаёт Cache, читающий и пишущий значения через
// redis.Client (команды GET/SET EX).
func NewRedisCache(rdb *redis.Client) Cache {
	return &redisCache{rdb: rdb}
}

// Get выполняет GET key. Отсутствие ключа (redis.Nil) — не ошибка:
// возвращается ("", false, nil).
func (c *redisCache) Get(ctx context.Context, key string) (string, bool, error) {
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", false, nil
		}
		return "", false, err
	}
	return val, true, nil
}

// Set выполняет SET key val EX ttl.
func (c *redisCache) Set(ctx context.Context, key, val string, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, val, ttl).Err()
}
