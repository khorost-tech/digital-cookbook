package cache

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestIntegration_ReadThrough поднимает настоящие PostgreSQL и Redis через
// testcontainers-go и прогоняет Service.GetProfile на реальных адаптерах
// (pg.go/redis.go): первый вызов должен пойти в БД (fromCache=false),
// второй — попасть в кэш (fromCache=true), и значение действительно должно
// лежать в Redis.
//
// Требует Docker. При -short пропускается.
func TestIntegration_ReadThrough(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: требуется Docker; пропуск при -short")
	}

	ctx := context.Background()

	// --- Postgres ---
	// postgres.BasicWaitStrategies() уже ждёт двойной лог "database system
	// is ready to accept connections" (postgres перезапускается после первого
	// старта) И wait.ForListeningPort — последнее критично на не-Linux Docker
	// (Mac/Windows), где порт проксируется отдельно и может быть готов позже
	// лога. Отдельный testcontainers.WithWaitStrategy(...) здесь не нужен —
	// он бы ПОЛНОСТЬЮ заменил (не дополнил) стратегию из BasicWaitStrategies
	// и потерял бы ожидание порта.
	pgC, err := postgres.Run(ctx, "postgres:17.2-alpine",
		postgres.WithDatabase("app"),
		postgres.WithUsername("app"),
		postgres.WithPassword("app"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("запуск postgres-контейнера: %v", err)
	}
	t.Cleanup(func() {
		if err := pgC.Terminate(context.Background()); err != nil {
			t.Logf("terminate postgres: %v", err)
		}
	})

	// --- Redis ---
	redisC, err := tcredis.Run(ctx, "redis:7.4-alpine",
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready to accept connections").
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("запуск redis-контейнера: %v", err)
	}
	t.Cleanup(func() {
		if err := redisC.Terminate(context.Background()); err != nil {
			t.Logf("terminate redis: %v", err)
		}
	})

	// --- подключение к Postgres, схема + сид ---
	dsn, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("postgres ping: %v", err)
	}

	const schema = `
		CREATE TABLE profiles (
			id    BIGINT PRIMARY KEY,
			name  TEXT NOT NULL,
			email TEXT NOT NULL
		);
		INSERT INTO profiles (id, name, email) VALUES (42, 'Ada Lovelace', 'ada@example.com');
	`
	if _, err := pool.Exec(ctx, schema); err != nil {
		t.Fatalf("применение схемы/сида: %v", err)
	}

	// --- подключение к Redis ---
	redisURL, err := redisC.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}
	redisOpts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("redis.ParseURL(%q): %v", redisURL, err)
	}
	rdb := redis.NewClient(redisOpts)
	t.Cleanup(func() {
		_ = rdb.Close()
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis ping: %v", err)
	}

	// --- сервис на реальных адаптерах ---
	svc := &Service{
		Repo:  NewPGRepo(pool),
		Cache: NewRedisCache(rdb),
		TTL:   time.Minute,
	}

	// Операции сервиса гоняем под контекстом с дедлайном, а не
	// context.Background(): так тест заодно фиксирует, что реальные адаптеры
	// (pgx, go-redis) работают с отменяемым контекстом, а не игнорируют его.
	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// 1-й вызов: кэш пуст → идём в БД.
	p1, fromCache1, err := svc.GetProfile(opCtx, 42)
	if err != nil {
		t.Fatalf("GetProfile (1-й вызов): %v", err)
	}
	if fromCache1 {
		t.Fatalf("1-й вызов: ожидали fromCache=false, получили true")
	}
	if p1.ID != 42 || p1.Name != "Ada Lovelace" || p1.Email != "ada@example.com" {
		t.Fatalf("1-й вызов: неожиданный профиль: %+v", p1)
	}

	// Значение должно реально попасть в Redis и совпадать с профилем из БД
	// (не просто быть непустым).
	key := CacheKey(42)
	raw, err := rdb.Get(opCtx, key).Result()
	if err != nil {
		t.Fatalf("значение отсутствует в Redis после 1-го вызова: %v", err)
	}
	var cached Profile
	if err := json.Unmarshal([]byte(raw), &cached); err != nil {
		t.Fatalf("значение в Redis не JSON-профиль: %v (raw=%q)", err, raw)
	}
	if cached != p1 {
		t.Fatalf("значение в Redis не совпадает с профилем из БД: %+v != %+v", cached, p1)
	}

	// TTL реально доехал до Redis: у ключа положительный PTTL, не больше
	// сконфигурированного в Service. Unit-тест проверяет лишь передачу TTL
	// фейку; только интеграция доказывает, что go-redis выставил EXPIRE в
	// самом Redis, а не молча записал ключ без срока жизни (PTTL=-1).
	pttl, err := rdb.PTTL(opCtx, key).Result()
	if err != nil {
		t.Fatalf("PTTL(%s): %v", key, err)
	}
	if pttl <= 0 || pttl > time.Minute {
		t.Fatalf("PTTL(%s) = %v, ожидали интервал (0, 1m] — TTL не доехал до Redis", key, pttl)
	}

	// 2-й вызов: должен попасть в кэш.
	p2, fromCache2, err := svc.GetProfile(opCtx, 42)
	if err != nil {
		t.Fatalf("GetProfile (2-й вызов): %v", err)
	}
	if !fromCache2 {
		t.Fatalf("2-й вызов: ожидали fromCache=true, получили false")
	}
	if p2 != p1 {
		t.Fatalf("2-й вызов: профиль из кэша не совпадает с профилем из БД: %+v != %+v", p2, p1)
	}

	// Негативный контракт через РЕАЛЬНЫЙ pg-адаптер: несуществующий id →
	// pgx.ErrNoRows внутри pg.go должен превратиться в cache.ErrNotFound.
	// Фейк подменял это вручную; здесь путь настоящий — до самого драйвера.
	if _, _, err := svc.GetProfile(opCtx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetProfile(999) на реальном Postgres: err = %v, want ErrNotFound", err)
	}
}
