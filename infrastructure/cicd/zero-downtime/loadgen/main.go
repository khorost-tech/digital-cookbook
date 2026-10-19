// Загрузчик постоянного трафика к таблице orders. Схема выбирается флагом и
// изображает версию кода в выкате:
//
//	-schema=old   — старый код: пишет и читает amount_cents;
//	-schema=dual  — промежуточная версия (expand-contract): пишет ОБЕ колонки,
//	                читает ещё старую (amount_cents). Это код, который
//	                выкатывают между «только старая» и «только новая» схемой;
//	-schema=new   — новый код: пишет и читает total_cents.
//
// Демо запускают загрузчики под нагрузкой и в этот момент меняют схему;
// загрузчик считает успешные и ошибочные операции и печатает `выполнено=N
// ошибок=M`. Так видно, роняет ли изменение схемы работающий код.
//
// Запросы идут в simple-протоколе (без серверных prepared statements и кэша
// планов): пропавшая после RENAME колонка даёт честную ошибку "column does not
// exist" сразу же. Чтение колонки — в nullable-приёмник: в переходном окне
// значение соседней колонки может быть NULL, и это валидные данные, а не сбой.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dsn := flag.String("dsn", "postgres://zdt:zdt@localhost:5457/zdt", "PostgreSQL DSN")
	dur := flag.Duration("duration", 5*time.Second, "длительность нагрузки")
	rps := flag.Int("rps", 200, "целевых операций в секунду (0 — без троттлинга)")
	schema := flag.String("schema", "old", "схема: old (amount_cents) | new (total_cents) | dual (обе)")
	flag.Parse()

	var insertSQL, selectSQL string
	switch *schema {
	case "old":
		insertSQL = "INSERT INTO orders (customer, amount_cents) VALUES ($1, $2)"
		selectSQL = "SELECT id, amount_cents FROM orders ORDER BY id DESC LIMIT 1"
	case "new":
		insertSQL = "INSERT INTO orders (customer, total_cents) VALUES ($1, $2)"
		selectSQL = "SELECT id, total_cents FROM orders ORDER BY id DESC LIMIT 1"
	case "dual":
		// Промежуточная версия пишет обе колонки одним значением, читает старую.
		insertSQL = "INSERT INTO orders (customer, amount_cents, total_cents) VALUES ($1, $2, $2)"
		selectSQL = "SELECT id, amount_cents FROM orders ORDER BY id DESC LIMIT 1"
	default:
		fmt.Fprintf(os.Stderr, "неизвестная схема %q (old|new|dual)\n", *schema)
		os.Exit(2)
	}

	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(*dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "разбор DSN: %v\n", err)
		os.Exit(1)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "подключение: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	var done, errs int64
	var firstErr atomic.Value // string

	var tick <-chan time.Time
	if *rps > 0 {
		t := time.NewTicker(time.Second / time.Duration(*rps))
		defer t.Stop()
		tick = t.C
	}

	deadline := time.Now().Add(*dur)
	i := 0
	for time.Now().Before(deadline) {
		if *rps > 0 {
			<-tick
		}
		i++
		var opErr error
		if i%2 == 0 {
			_, opErr = pool.Exec(ctx, insertSQL, fmt.Sprintf("customer-%d", rand.Intn(100)), rand.Intn(100000))
		} else {
			var id int64
			var v *int // nullable: в переходном окне соседняя колонка может быть NULL
			opErr = pool.QueryRow(ctx, selectSQL).Scan(&id, &v)
			if opErr == pgx.ErrNoRows {
				opErr = nil
			}
		}
		if opErr != nil {
			atomic.AddInt64(&errs, 1)
			firstErr.CompareAndSwap(nil, opErr.Error())
		} else {
			atomic.AddInt64(&done, 1)
		}
	}

	fmt.Printf("выполнено=%d ошибок=%d\n", done, errs)
	if e, ok := firstErr.Load().(string); ok && e != "" {
		fmt.Printf("первая ошибка: %s\n", e)
	}
}
