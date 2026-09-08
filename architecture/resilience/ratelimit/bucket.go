package ratelimit

import (
	"math"
	"time"
)

// TokenBucket — «ведро токенов». В ведре ёмкостью capacity накапливаются токены
// со скоростью refill в секунду; запрос тратит один токен. Ключевое свойство:
// накопленные токены позволяют пропустить ВСПЛЕСК до capacity разом, а средний
// темп держится на уровне refill.
type TokenBucket struct {
	capacity float64
	refill   float64 // токенов в секунду, > 0
	tokens   float64
	last     time.Time
}

// NewTokenBucket: capacity — размер всплеска, refillPerSec — установившийся темп.
func NewTokenBucket(capacity int, refillPerSec float64) *TokenBucket {
	if refillPerSec <= 0 {
		panic("ratelimit: refillPerSec должен быть > 0")
	}
	return &TokenBucket{capacity: float64(capacity), refill: refillPerSec, tokens: float64(capacity)}
}

// AllowAt пополняет ведро по прошедшему времени и пробует списать токен.
func (b *TokenBucket) AllowAt(now time.Time) Decision {
	if !b.last.IsZero() {
		if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
			b.tokens = math.Min(b.capacity, b.tokens+elapsed*b.refill)
		}
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return Decision{Allowed: true}
	}
	need := 1 - b.tokens
	return Decision{RetryAfter: time.Duration(need / b.refill * float64(time.Second))}
}

// LeakyBucket — «дырявое ведро» как пейсер. Уровень растёт на 1 с каждым
// запросом и «утекает» со скоростью rate в секунду; запрос проходит, только
// если уровень не переполнит ёмкость. В отличие от TokenBucket, всплеск сюда
// разом не проходит — на выходе получается сглаженный поток около rate.
type LeakyBucket struct {
	rate     float64 // темп «утечки» (запросов в секунду на выходе), > 0
	capacity float64 // ёмкость (допуск всплеска); 1 — строгое сглаживание
	level    float64
	last     time.Time
}

// NewLeakyBucket: ratePerSec — выходной темп, capacity — допуск всплеска.
func NewLeakyBucket(ratePerSec float64, capacity int) *LeakyBucket {
	if ratePerSec <= 0 {
		panic("ratelimit: ratePerSec должен быть > 0")
	}
	return &LeakyBucket{rate: ratePerSec, capacity: float64(capacity)}
}

// AllowAt сначала «сливает» накопленное по времени, затем пробует долить запрос.
func (b *LeakyBucket) AllowAt(now time.Time) Decision {
	if !b.last.IsZero() {
		if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
			b.level = math.Max(0, b.level-elapsed*b.rate)
		}
	}
	b.last = now
	if b.level+1 <= b.capacity {
		b.level++
		return Decision{Allowed: true}
	}
	over := b.level + 1 - b.capacity
	return Decision{RetryAfter: time.Duration(over / b.rate * float64(time.Second))}
}
