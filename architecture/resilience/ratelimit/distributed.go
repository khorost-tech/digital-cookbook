package ratelimit

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// luaTokenBucket — ПРАВИЛЬНЫЙ распределённый token bucket: время берётся из
// самого Redis (команда TIME), а не с часов инстанса. Это принципиально: у общего
// лимита должен быть ОДИН источник времени. Если каждый инстанс присылает своё
// время, часы разъезжаются — отставший инстанс сдвигает отметку ts назад, и
// следующий запрос начисляет токены повторно (см. luaTokenBucketSkewed ниже).
const luaTokenBucket = `
local capacity = tonumber(ARGV[1])
local refill   = tonumber(ARGV[2])
local t = redis.call('TIME')
local now_ms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local data = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])
if tokens == nil then tokens = capacity; ts = now_ms end
if ts > now_ms then ts = now_ms end
local elapsed = (now_ms - ts) / 1000.0
tokens = math.min(capacity, tokens + elapsed * refill)
local allowed = 0
if tokens >= 1 then tokens = tokens - 1; allowed = 1 end
redis.call('HSET', KEYS[1], 'tokens', tokens, 'ts', now_ms)
redis.call('PEXPIRE', KEYS[1], math.ceil(capacity / refill * 1000) + 1000)
return allowed
`

// luaTokenBucketSkewed — НЕПРАВИЛЬНЫЙ вариант, оставлен как демонстрация дефекта:
// время приходит с часов инстанса (ARGV[3]) и безусловно записывается обратно.
// Запрос от отставших часов отодвигает ts в прошлое, после чего следующий инстанс
// пересчитывает «прошедшее время» от неверной отметки и начисляет лишние токены —
// общий лимит перестаёт держаться.
const luaTokenBucketSkewed = `
local capacity = tonumber(ARGV[1])
local refill   = tonumber(ARGV[2])
local now_ms   = tonumber(ARGV[3])
local data = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])
if tokens == nil then tokens = capacity; ts = now_ms end
local elapsed = math.max(0, now_ms - ts) / 1000.0
tokens = math.min(capacity, tokens + elapsed * refill)
local allowed = 0
if tokens >= 1 then tokens = tokens - 1; allowed = 1 end
redis.call('HSET', KEYS[1], 'tokens', tokens, 'ts', now_ms)
redis.call('PEXPIRE', KEYS[1], math.ceil(capacity / refill * 1000) + 1000)
return allowed
`

// RedisLimiter — распределённый token bucket. Экземпляры с одинаковым key на
// одном Redis делят общий лимит.
type RedisLimiter struct {
	rdb      redis.Scripter
	key      string
	capacity int
	refill   float64
	script   *redis.Script
	skewed   *redis.Script
}

// NewRedisLimiter: rdb — клиент Redis, key — общий ключ лимита.
func NewRedisLimiter(rdb redis.Scripter, key string, capacity int, refillPerSec float64) *RedisLimiter {
	return &RedisLimiter{
		rdb:      rdb,
		key:      key,
		capacity: capacity,
		refill:   refillPerSec,
		script:   redis.NewScript(luaTokenBucket),
		skewed:   redis.NewScript(luaTokenBucketSkewed),
	}
}

// Allow — распределённый лимит с единым источником времени (Redis TIME). Часы
// инстанса не участвуют вовсе, поэтому их рассинхрон на решение не влияет.
func (l *RedisLimiter) Allow(ctx context.Context) (bool, error) {
	res, err := l.script.Run(ctx, l.rdb, []string{l.key}, l.capacity, l.refill).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

// AllowWithInstanceClock — вариант с часами инстанса, оставлен для демонстрации
// того, как рассинхрон ломает общий лимит. В проде так делать не надо.
func (l *RedisLimiter) AllowWithInstanceClock(ctx context.Context, now time.Time) (bool, error) {
	res, err := l.skewed.Run(ctx, l.rdb, []string{l.key}, l.capacity, l.refill, now.UnixMilli()).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}
