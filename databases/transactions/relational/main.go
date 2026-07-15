// Стенд №1 к статье «Транзакции в реляционных БД на практике: PostgreSQL».
// https://khorost.tech/databases/transactions-relational-postgres-practice/
//
// Воспроизводит две аномалии под конкуренцией и показывает их лечение, снимая метрики
// (доля аномалий, latency p50/p99, retry rate, throughput).
//
//	lost-update: N воркеров инкрементят один счётчик; стратегии naive/for-update/atomic/serializable.
//	write-skew:  два дежурных снимаются с дежурства при инварианте «останется ≥1»; iso rr vs serializable.
//
// Примеры:
//
//	go run . -scenario lost-update -strategy naive        -workers 16 -iters 100
//	go run . -scenario lost-update -strategy for-update   -workers 16 -iters 100
//	go run . -scenario lost-update -strategy atomic       -workers 16 -iters 100
//	go run . -scenario lost-update -strategy serializable -workers 16 -iters 100
//	go run . -scenario write-skew  -iso rr          -rounds 200
//	go run . -scenario write-skew  -iso serializable -rounds 200
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func isoLevel(s string) pgx.TxIsoLevel {
	switch s {
	case "rr":
		return pgx.RepeatableRead
	case "serializable", "ser":
		return pgx.Serializable
	default:
		return pgx.ReadCommitted
	}
}

// isSerializationFailure — код 40001 (serialization_failure) или 40P01 (deadlock_detected).
func isSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}

type metrics struct {
	mu        sync.Mutex
	latencies []time.Duration
	retries   int64
	txDone    int64
}

func (m *metrics) add(d time.Duration) {
	m.mu.Lock()
	m.latencies = append(m.latencies, d)
	m.mu.Unlock()
	atomic.AddInt64(&m.txDone, 1)
}

func (m *metrics) pct(p float64) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.latencies) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), m.latencies...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(float64(len(s)-1) * p)
	return s[idx]
}

func main() {
	scenario := flag.String("scenario", "lost-update", "lost-update | write-skew")
	strategy := flag.String("strategy", "naive", "naive | for-update | atomic | serializable")
	iso := flag.String("iso", "rc", "rc | rr | serializable (для write-skew)")
	workers := flag.Int("workers", 16, "конкурентных воркеров (lost-update)")
	iters := flag.Int("iters", 100, "инкрементов на воркер (lost-update)")
	rounds := flag.Int("rounds", 200, "раундов (write-skew)")
	dsn := flag.String("dsn", envOr("DSN", "postgres://demo:demo@localhost:5440/demo"), "PostgreSQL DSN")
	flag.Parse()

	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(*dsn)
	if err != nil {
		log.Fatal(err)
	}
	cfg.MaxConns = int32(*workers + 8)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	var ver string
	_ = pool.QueryRow(ctx, "SELECT version()").Scan(&ver)
	fmt.Printf("# %s\n", ver)

	switch *scenario {
	case "lost-update":
		runLostUpdate(ctx, pool, *strategy, *workers, *iters)
	case "write-skew":
		runWriteSkew(ctx, pool, *iso, *rounds)
	default:
		log.Fatalf("неизвестный scenario: %s", *scenario)
	}
}

// ------- lost-update -------

func runLostUpdate(ctx context.Context, pool *pgxpool.Pool, strategy string, workers, iters int) {
	// reset
	mustExec(ctx, pool, "INSERT INTO accounts (id, balance) VALUES (1, 0) ON CONFLICT (id) DO UPDATE SET balance = 0")

	m := &metrics{}
	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				t0 := time.Now()
				increment(ctx, pool, strategy, m)
				m.add(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	wall := time.Since(start)

	var balance int64
	_ = pool.QueryRow(ctx, "SELECT balance FROM accounts WHERE id = 1").Scan(&balance)
	expected := int64(workers * iters)

	fmt.Printf("scenario=lost-update strategy=%s workers=%d iters=%d\n", strategy, workers, iters)
	fmt.Printf("  expected=%d actual=%d lost=%d (%.1f%%)\n",
		expected, balance, expected-balance, 100*float64(expected-balance)/float64(expected))
	fmt.Printf("  retries=%d  tx=%d  throughput=%.0f tx/s\n",
		atomic.LoadInt64(&m.retries), atomic.LoadInt64(&m.txDone), float64(m.txDone)/wall.Seconds())
	fmt.Printf("  latency p50=%v p99=%v\n", m.pct(0.50), m.pct(0.99))
}

func increment(ctx context.Context, pool *pgxpool.Pool, strategy string, m *metrics) {
	switch strategy {
	case "atomic":
		// один statement — потерять нельзя, блокировка не нужна
		mustExec(ctx, pool, "UPDATE accounts SET balance = balance + 1 WHERE id = 1")
	case "for-update":
		// пессимистичная блокировка строки на Read Committed
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			return
		}
		var bal int64
		if err := tx.QueryRow(ctx, "SELECT balance FROM accounts WHERE id = 1 FOR UPDATE").Scan(&bal); err != nil {
			tx.Rollback(ctx)
			return
		}
		tx.Exec(ctx, "UPDATE accounts SET balance = $1 WHERE id = 1", bal+1)
		tx.Commit(ctx)
	case "serializable":
		// read-modify-write на SERIALIZABLE + retry на 40001
		retryTx(ctx, pool, pgx.Serializable, m, func(tx pgx.Tx) error {
			var bal int64
			if err := tx.QueryRow(ctx, "SELECT balance FROM accounts WHERE id = 1").Scan(&bal); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "UPDATE accounts SET balance = $1 WHERE id = 1", bal+1)
			return err
		})
	default: // naive: read-modify-write на Read Committed без блокировки → lost update
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			return
		}
		var bal int64
		if err := tx.QueryRow(ctx, "SELECT balance FROM accounts WHERE id = 1").Scan(&bal); err != nil {
			tx.Rollback(ctx)
			return
		}
		tx.Exec(ctx, "UPDATE accounts SET balance = $1 WHERE id = 1", bal+1)
		tx.Commit(ctx)
	}
}

// ------- write-skew -------

func runWriteSkew(ctx context.Context, pool *pgxpool.Pool, iso string, rounds int) {
	level := isoLevel(iso)
	m := &metrics{}
	var mu sync.Mutex
	violations, aborts := 0, 0
	start := time.Now()

	for r := 0; r < rounds; r++ {
		// reset: оба дежурят
		mustExec(ctx, pool, `INSERT INTO on_call (doctor, on_duty) VALUES ('A', true), ('B', true)
			ON CONFLICT (doctor) DO UPDATE SET on_duty = true`)

		// Барьер на двоих: оба читают снимок ДО того, как любой запишет, — так снимки
		// гарантированно пересекаются и write skew воспроизводится, если уровень его допускает.
		read := make(chan struct{}, 2)
		release := make(chan struct{})
		var wg sync.WaitGroup
		for _, who := range []string{"A", "B"} {
			wg.Add(1)
			go func(me string) {
				defer wg.Done()
				t0 := time.Now()

				tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: level})
				if err != nil {
					return
				}
				var onDuty int
				if err = tx.QueryRow(ctx, "SELECT count(*) FROM on_call WHERE on_duty = true").Scan(&onDuty); err != nil {
					tx.Rollback(ctx)
					return
				}
				read <- struct{}{} // «я прочитал снимок»
				<-release          // ждём, пока прочитает и второй

				// инвариант: снимаюсь, только если останется хотя бы один
				if onDuty >= 2 {
					_, err = tx.Exec(ctx, "UPDATE on_call SET on_duty = false WHERE doctor = $1", me)
				}
				if err != nil {
					tx.Rollback(ctx)
				} else {
					err = tx.Commit(ctx)
				}
				if isSerializationFailure(err) { // SSI откатил одну из транзакций — это и есть защита
					mu.Lock()
					aborts++
					mu.Unlock()
				}
				m.add(time.Since(t0))
			}(who)
		}
		<-read // оба прочитали
		<-read
		close(release) // отпускаем к фазе записи
		wg.Wait()

		var left int
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM on_call WHERE on_duty = true").Scan(&left)
		if left == 0 {
			violations++ // инвариант нарушен: не осталось ни одного дежурного
		}
	}
	wall := time.Since(start)

	fmt.Printf("scenario=write-skew iso=%s rounds=%d\n", iso, rounds)
	fmt.Printf("  invariant_violations=%d (%.1f%% раундов)  serialization_aborts=%d\n",
		violations, 100*float64(violations)/float64(rounds), aborts)
	fmt.Printf("  tx=%d  throughput=%.0f tx/s\n", atomic.LoadInt64(&m.txDone), float64(m.txDone)/wall.Seconds())
	fmt.Printf("  latency p50=%v p99=%v\n", m.pct(0.50), m.pct(0.99))
}

// ------- helpers -------

// retryTx выполняет fn в транзакции нужного уровня, повторяя на 40001/40P01.
func retryTx(ctx context.Context, pool *pgxpool.Pool, level pgx.TxIsoLevel, m *metrics, fn func(pgx.Tx) error) {
	for attempt := 0; ; attempt++ {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: level})
		if err != nil {
			return
		}
		err = fn(tx)
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			tx.Rollback(ctx)
		}
		if err == nil {
			return
		}
		if isSerializationFailure(err) {
			atomic.AddInt64(&m.retries, 1)
			continue
		}
		return
	}
}

func mustExec(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		log.Fatalf("exec %q: %v", sql, err)
	}
}
