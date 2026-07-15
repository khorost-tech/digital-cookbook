package main

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// Redis: атомарность через Lua-скрипт — проверка и декремент выполняются на сервере как единая
// неделимая операция (пока скрипт идёт, других команд нет). Не транзакция в ACID-смысле, но
// достаточно для этого инварианта.
type redisStore struct {
	rdb    *redis.Client
	script *redis.Script
}

func newRedis() (Store, error) {
	rdb := redis.NewClient(&redis.Options{Addr: envOr("REDIS_ADDR", "localhost:6391"), PoolSize: 48})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return nil, err
	}
	// если баланс > 0 — уменьшить и вернуть 1, иначе вернуть 0
	script := redis.NewScript(
		`local v = tonumber(redis.call('GET', KEYS[1])); if v and v > 0 then redis.call('DECR', KEYS[1]); return 1 else return 0 end`)
	return &redisStore{rdb, script}, nil
}

func (s *redisStore) Name() string { return "redis" }

func (s *redisStore) Reset(ctx context.Context, budget int64) error {
	return s.rdb.Set(ctx, "balance", budget, 0).Err()
}

func (s *redisStore) Decrement(ctx context.Context) (bool, int64) {
	n, err := s.script.Run(ctx, s.rdb, []string{"balance"}).Int()
	if err != nil {
		return false, 0
	}
	return n == 1, 0
}

func (s *redisStore) Final(ctx context.Context) int64 {
	v, _ := s.rdb.Get(ctx, "balance").Int64()
	return v
}

func (s *redisStore) Close() { _ = s.rdb.Close() }
