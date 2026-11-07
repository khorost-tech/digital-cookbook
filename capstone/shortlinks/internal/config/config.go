// Package config читает конфигурацию из переменных окружения и проверяет её
// целиком до старта: сервис с неверной конфигурацией не должен начать
// принимать трафик и упасть на первом запросе.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Service — конфигурация сервиса ссылок.
type Service struct {
	Addr             string        // HTTP_ADDR, по умолчанию :8080
	DatabaseURL      string        // DATABASE_URL, обязательна
	RedisAddr        string        // REDIS_ADDR, пусто — без кэша
	NatsURL          string        // NATS_URL, пусто — клики не публикуются
	PublishMode      string        // PUBLISH_MODE: async | sync
	PublishBuffer    int           // PUBLISH_BUFFER: ёмкость буфера async-публикации
	EventMode        string        // EVENT_MODE: outbox | direct — событие создания ссылки
	CacheTTL         time.Duration // CACHE_TTL
	Singleflight     bool          // SINGLEFLIGHT: склеивать одновременные промахи кэша
	DBMaxConns       int32         // DB_MAX_CONNS
	ShutdownDelay    time.Duration // SHUTDOWN_DELAY: пауза после снятия готовности до остановки приёма
	ShutdownTimeout  time.Duration // SHUTDOWN_TIMEOUT: сколько ждать незавершённые запросы
	Graceful         bool          // GRACEFUL: false — выход сразу по сигналу (для опыта)
	BaseURL          string        // BASE_URL: префикс коротких ссылок в ответе
	OtelEndpoint     string        // OTEL_EXPORTER_OTLP_ENDPOINT, пусто — без трейсов
	CrashAfterInsert bool          // CRASH_AFTER_INSERT: падение между записью ссылки и публикацией (опыт)
}

// LoadService собирает конфигурацию и возвращает все ошибки сразу, а не первую.
func LoadService() (Service, error) {
	r := reader{}
	c := Service{
		Addr:             r.str("HTTP_ADDR", ":8080"),
		DatabaseURL:      r.required("DATABASE_URL"),
		RedisAddr:        r.str("REDIS_ADDR", ""),
		NatsURL:          r.str("NATS_URL", ""),
		PublishMode:      r.oneOf("PUBLISH_MODE", "async", "async", "sync"),
		PublishBuffer:    r.intRange("PUBLISH_BUFFER", 1024, 1, 1<<20),
		EventMode:        r.oneOf("EVENT_MODE", "outbox", "outbox", "direct"),
		CacheTTL:         r.duration("CACHE_TTL", 10*time.Minute),
		Singleflight:     r.boolean("SINGLEFLIGHT", true),
		DBMaxConns:       int32(r.intRange("DB_MAX_CONNS", 10, 1, 1000)),
		ShutdownDelay:    r.duration("SHUTDOWN_DELAY", 0),
		ShutdownTimeout:  r.duration("SHUTDOWN_TIMEOUT", 15*time.Second),
		Graceful:         r.boolean("GRACEFUL", true),
		BaseURL:          r.str("BASE_URL", "http://localhost:8080"),
		OtelEndpoint:     r.str("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		CrashAfterInsert: r.boolean("CRASH_AFTER_INSERT", false),
	}
	if c.DatabaseURL != "" {
		if u, err := url.Parse(c.DatabaseURL); err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
			r.fail("DATABASE_URL: ожидается postgres://…")
		}
	}
	if c.ShutdownDelay >= c.ShutdownTimeout && c.Graceful {
		r.fail("SHUTDOWN_DELAY должна быть меньше SHUTDOWN_TIMEOUT")
	} else if c.OtelEndpoint != "" && c.Graceful && c.ShutdownTimeout-c.ShutdownDelay <= 2*time.Second {
		// секунда из срока уходит на выгрузку трейсов — на дренаж должно остаться больше секунды
		r.fail("при OTEL_EXPORTER_OTLP_ENDPOINT SHUTDOWN_TIMEOUT должна превышать SHUTDOWN_DELAY больше чем на 2s")
	}
	return c, r.err()
}

type reader struct{ errs []string }

func (r *reader) fail(msg string) { r.errs = append(r.errs, msg) }

func (r *reader) err() error {
	if len(r.errs) == 0 {
		return nil
	}
	return errors.New("конфигурация: " + strings.Join(r.errs, "; "))
}

func (r *reader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func (r *reader) required(key string) string {
	v := os.Getenv(key)
	if v == "" {
		r.fail(key + " не задана")
	}
	return v
}

func (r *reader) oneOf(key, def string, allowed ...string) string {
	v := r.str(key, def)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	r.fail(fmt.Sprintf("%s=%q: допустимо %s", key, v, strings.Join(allowed, ", ")))
	return def
}

func (r *reader) intRange(key string, def, lo, hi int) int {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		r.fail(fmt.Sprintf("%s=%q: целое от %d до %d", key, v, lo, hi))
		return def
	}
	return n
}

func (r *reader) duration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		r.fail(fmt.Sprintf("%s=%q: длительность вида 5s", key, v))
		return def
	}
	return d
}

func (r *reader) boolean(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.fail(fmt.Sprintf("%s=%q: true или false", key, v))
		return def
	}
	return b
}
