//go:build integration

// Интеграционный тест: нужен Docker. Запуск: go test -tags integration ./...
// По умолчанию (без тега) стенд остаётся быстрым и не требует Redis.
package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func startRedis(t *testing.T) *redis.Client {
	t.Helper()
	ctx := context.Background()
	ctr, err := tcredis.Run(ctx, "redis:7.4-alpine")
	if err != nil {
		t.Fatalf("старт Redis: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	endpoint, err := ctr.Endpoint(ctx, "")
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: endpoint})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// TestDistributedVsLocal — зачем нужен распределённый лимит. Три «инстанса» шлют
// по 100 запросов при общем лимите 100. Пополнение намеренно медленное (1/сек),
// чтобы за время прогона набежало меньше токена и замер мерил лимит, а не refill.
func TestDistributedVsLocal(t *testing.T) {
	const (
		instances = 3
		limit     = 100
		perInst   = 100
	)
	ctx := context.Background()
	rdb := startRedis(t)

	// Локально: три независимых ведра — суммарно втрое больше лимита.
	local := 0
	for i := 0; i < instances; i++ {
		b := NewTokenBucket(limit, 1)
		local += countAllowed(b, time.Unix(1000, 0), perInst)
	}
	if local != instances*limit {
		t.Fatalf("локально пропущено %d, ждали %d (лимит нарушен)", local, instances*limit)
	}

	// Общий Redis: один ключ на всех — глобальный лимит держится.
	shared := 0
	for i := 0; i < instances; i++ {
		lim := NewRedisLimiter(rdb, "api:global", limit, 1)
		for j := 0; j < perInst; j++ {
			ok, err := lim.Allow(ctx)
			if err != nil {
				t.Fatalf("Redis Allow: %v", err)
			}
			if ok {
				shared++
			}
		}
	}
	// Допуск на refill за время прогона: при 1/сек за пару секунд набежит 1–2 токена.
	if shared < limit || shared > limit+5 {
		t.Fatalf("через общий Redis пропущено %d, ждали ≈%d", shared, limit)
	}
	t.Logf("локально: %d (=%d×лимит) | общий Redis: %d (≈лимит)", local, instances, shared)
}

// TestInstanceClockSkewBreaksSharedLimit — почему у общего лимита должен быть один
// источник времени. Инстанс с ОТСТАЮЩИМИ часами сдвигает отметку ts в прошлое;
// следующий запрос считает «прошедшее время» от неверной отметки и начисляет токены
// повторно, из-за чего лимит пропускает лишнее. Вариант с Redis TIME этому не подвержен.
func TestInstanceClockSkewBreaksSharedLimit(t *testing.T) {
	ctx := context.Background()
	rdb := startRedis(t)
	const limit = 5

	// (1) Часы инстансов: один «убежал вперёд», другой отстаёт на минуту.
	skewLim := NewRedisLimiter(rdb, "skew:key", limit, 10)
	ahead := time.Unix(10_000, 0)
	behind := ahead.Add(-time.Minute)

	drained := 0
	for i := 0; i < limit; i++ { // вычерпываем ведро по «быстрым» часам
		ok, err := skewLim.AllowWithInstanceClock(ctx, ahead)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			drained++
		}
	}
	if drained != limit {
		t.Fatalf("ожидали вычерпать %d, вычерпали %d", limit, drained)
	}
	if ok, err := skewLim.AllowWithInstanceClock(ctx, ahead); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("ведро должно быть пустым по тем же часам")
	}
	// Запрос от отстающего инстанса сам не проходит (пополнять нечем), но
	// ОТРАВЛЯЕТ отметку: ts записывается его отставшим временем.
	if _, err := skewLim.AllowWithInstanceClock(ctx, behind); err != nil {
		t.Fatal(err)
	}
	// Теперь «быстрый» инстанс видит разницу в минуту и начисляет токены заново,
	// хотя реального времени не прошло — общий лимит пробит.
	extra := 0
	for i := 0; i < limit; i++ {
		ok, err := skewLim.AllowWithInstanceClock(ctx, ahead)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			extra++
		}
	}
	if extra == 0 {
		t.Fatal("ожидали воспроизвести дефект: после сдвига ts назад токены должны начислиться повторно")
	}
	t.Logf("часы инстанса: после сдвига ts назад лимит пропустил ещё %d сверх %d — общий лимит сломан", extra, limit)

	// (2) Redis TIME: часы инстансов не участвуют, повторить трюк нечем.
	safeLim := NewRedisLimiter(rdb, "safe:key", limit, 1)
	passed := 0
	for i := 0; i < limit*3; i++ {
		ok, err := safeLim.Allow(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			passed++
		}
	}
	if passed > limit+1 {
		t.Fatalf("с Redis TIME пропущено %d, ждали ≈%d", passed, limit)
	}
	t.Logf("Redis TIME: пропущено %d при лимите %d — часы инстансов на решение не влияют", passed, limit)
}
