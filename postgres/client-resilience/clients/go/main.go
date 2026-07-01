// Демонстрационный клиент pgx/pgxpool: пишет через pgbouncer (transaction pool mode),
// читает напрямую с реплики, раз при старте показывает failover-aware выбор primary
// через MULTIHOST_DSN (target_session_attrs=read-write).
// Клиент НИКОГДА не падает на ошибке: при обрыве/failover он логирует ошибку и продолжает,
// чтобы было видно окно недоступности и последующий reconnect.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const opTimeout = time.Second

func main() {
	ctx := context.Background()

	writer := buildWriterPool(ctx)
	defer writer.Close()

	reader := buildReaderPool(ctx)
	defer reader.Close()

	// Один раз при старте: подключение через MULTIHOST_DSN с target_session_attrs=read-write.
	// pgx перебирает хосты из DSN и выбирает тот, что сейчас отвечает как read-write (primary).
	// Это и есть failover-aware primary selection — при промоушене реплики следующее
	// подключение с тем же DSN уедет на новый primary без изменений в коде клиента.
	logMultihostSelection(ctx)

	fmt.Println("[go] цикл WRITE(bouncer)/READ(replica) раз в секунду, Ctrl+C для выхода")
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()

	for range tick.C {
		id, err := writeRow(ctx, writer)
		if err != nil {
			fmt.Printf("[go] ERROR write: %v\n", err)
		} else {
			fmt.Printf("[go] WROTE id=%d\n", id)
		}

		rows, err := readRows(ctx, reader)
		if err != nil {
			fmt.Printf("[go] ERROR read: %v\n", err)
		} else {
			fmt.Printf("[go] READ %v\n", rows)
		}
	}
}

// buildWriterPool — пул для записи через pgbouncer в TRANSACTION pool mode.
//
// КРИТИЧНО: pgbouncer в transaction pool mode отдаёт клиенту разное серверное соединение
// на каждую транзакцию (а то и на каждый запрос вне явной транзакции). pgx v5 по умолчанию
// использует extended protocol с server-side prepared statements — драйвер готовит запрос
// (Parse) на одном соединении и ожидает, что сможет исполнить его (Bind/Execute) там же
// на следующем вызове. Через transaction-mode бенчер это ломается: подготовленный statement
// может "уехать" на другое физическое соединение к Postgres, и сервер ответит
// "prepared statement does not exist" — непредсказуемо и трудноотлаживаемо.
// Решение: заставить pgx использовать simple protocol (без server-side prepare) —
// каждый запрос отправляется как самостоятельный SQL-текст, что безопасно для
// transaction-mode бенчера. Поэтому НЕ полагаемся на кеш подготовленных statement'ов.
func buildWriterPool(ctx context.Context) *pgxpool.Pool {
	dsn := mustGetenv("PGBOUNCER_DSN")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		fmt.Printf("[go] FATAL: не удалось разобрать PGBOUNCER_DSN: %v\n", err)
		os.Exit(1)
	}

	cfg.MaxConns = 10
	cfg.MaxConnLifetime = 5 * time.Minute

	// см. комментарий к функции: transaction-mode pgbouncer несовместим с extended
	// protocol + server-side prepared statements.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		fmt.Printf("[go] FATAL: не удалось создать writer pool: %v\n", err)
		os.Exit(1)
	}
	return pool
}

// buildReaderPool — пул для чтения напрямую с реплики (без bouncer), extended protocol
// и server-side prepared statements тут безопасны — соединение с сервером закреплено за пулом.
func buildReaderPool(ctx context.Context) *pgxpool.Pool {
	dsn := mustGetenv("REPLICA_DSN")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		fmt.Printf("[go] FATAL: не удалось разобрать REPLICA_DSN: %v\n", err)
		os.Exit(1)
	}

	cfg.MaxConns = 10
	cfg.MaxConnLifetime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		fmt.Printf("[go] FATAL: не удалось создать reader pool: %v\n", err)
		os.Exit(1)
	}
	return pool
}

// logMultihostSelection открывает одно соединение по MULTIHOST_DSN, логирует, какой хост
// был выбран как read-write (primary), и закрывает соединение. Не влияет на основной цикл.
func logMultihostSelection(ctx context.Context) {
	dsn := mustGetenv("MULTIHOST_DSN")
	cctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	conn, err := pgx.Connect(cctx, dsn)
	if err != nil {
		fmt.Printf("[go] ERROR multihost connect: %v\n", err)
		return
	}
	defer conn.Close(context.Background())

	cfg := conn.Config()
	fmt.Printf("[go] MULTIHOST: target_session_attrs=read-write -> выбран хост %s:%d\n", cfg.Host, cfg.Port)
}

// writeRow вставляет строку в users через writer pool (pgbouncer) с ретраями транзиентных ошибок.
func writeRow(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var id int64
	name := fmt.Sprintf("client-go@%s", time.Now().Format("15:04:05.000"))

	err := withRetry(func() error {
		cctx, cancel := context.WithTimeout(ctx, opTimeout)
		defer cancel()

		return pool.QueryRow(cctx,
			"INSERT INTO users (name) VALUES ($1) RETURNING id",
			name,
		).Scan(&id)
	})
	return id, err
}

// readRows читает несколько последних строк из users через reader pool (реплика).
func readRows(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var rows []string

	err := withRetry(func() error {
		cctx, cancel := context.WithTimeout(ctx, opTimeout)
		defer cancel()

		result, err := pool.Query(cctx,
			"SELECT id, name FROM users ORDER BY id DESC LIMIT 3",
		)
		if err != nil {
			return err
		}
		defer result.Close()

		rows = rows[:0]
		for result.Next() {
			var id int64
			var name string
			if err := result.Scan(&id, &name); err != nil {
				return err
			}
			rows = append(rows, fmt.Sprintf("%d:%s", id, name))
		}
		return result.Err()
	})
	return rows, err
}

const maxRetries = 2

// withRetry повторяет op при транзиентных ошибках Postgres (serialization_failure,
// deadlock_detected, admin shutdown) с небольшим backoff. Остальные ошибки не ретраятся —
// они возвращаются вызывающему коду как есть (и залогируются, не уронив цикл).
func withRetry(op func() error) error {
	var err error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		err = op()
		if err == nil {
			return nil
		}
		if !isRetryable(err) {
			return err
		}
		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
		}
	}
	return err
}

// isRetryable распознаёт транзиентные коды ошибок Postgres по SQLSTATE.
func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "40001", // serialization_failure
		"40P01", // deadlock_detected
		"57P01": // admin_shutdown
		return true
	default:
		return false
	}
}

func mustGetenv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		fmt.Printf("[go] FATAL: не задана переменная окружения %s\n", k)
		os.Exit(1)
	}
	return v
}
