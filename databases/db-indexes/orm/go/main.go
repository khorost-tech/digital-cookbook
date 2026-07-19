// Go (pgx) — ORM-эффекты поверх PG-стенда «Индексы в базах данных» (events, 2M строк, idx_events_user).
// https://khorost.tech/databases/... (статья 5 серии)
//
//	cd db-indexes/postgres && docker compose up -d
//	./run.sh sql/00-schema.sql >/dev/null && ./run.sh sql/01-scan.sql >/dev/null
//	cd ../orm/go && go run .
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	// По умолчанию — хостовый порт стенда (localhost:5433). DATABASE_URL позволяет
	// переопределить строку подключения, если код гоняется внутри docker-сети стенда
	// (см. README: контейнер golang на сети postgres_default, host=postgres:5432).
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:idxdemo@localhost:5433/idxdemo"
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		panic(err)
	}
	defer pool.Close()

	// --- (1) N+1: типичный ORM-паттерн "выбрали список — по каждому отдельный запрос" ---
	// 20 отдельных SELECT count(*) вместо одного агрегата с IN/ANY.
	fmt.Println("=== (1) N+1 vs батч ===")
	var n1 int
	rows, err := pool.Query(ctx, "SELECT DISTINCT user_id FROM events ORDER BY user_id LIMIT 20")
	if err != nil {
		panic(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			panic(err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	for _, id := range ids {
		var c int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE user_id=$1", id).Scan(&c); err != nil {
			panic(err)
		}
		n1++
	}
	fmt.Printf("N+1: выполнено %d отдельных запросов (по одному на user_id)\n", n1)

	// Правильно: один запрос через ANY($1) — тот же результат, 1 round-trip вместо N.
	var total int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE user_id = ANY($1)", ids).Scan(&total); err != nil {
		panic(err)
	}
	fmt.Printf("Батч: 1 запрос, total=%d (сумма по тем же %d user_id)\n", total, len(ids))

	// --- (2) каст bind-параметра ломает индекс: план из кода через EXPLAIN ---
	// user_id — bigint, но ORM (или невнимательный разработчик) сравнивает его со строкой:
	// user_id::text = '555'. Индекс idx_events_user построен на bigint-выражении user_id,
	// поэтому под кастом он бесполезен — планировщик уходит в Seq Scan по 2M строк.
	fmt.Println("\n=== (2) план с кастом user_id::text = '555' (ломает индекс) ===")
	printPlan(ctx, pool, "EXPLAIN SELECT * FROM events WHERE user_id::text = '555'")

	fmt.Println("\n=== (3) для сравнения: план без каста user_id = 555 (использует индекс) ===")
	printPlan(ctx, pool, "EXPLAIN SELECT * FROM events WHERE user_id = 555")
}

// printPlan выполняет EXPLAIN через драйвер и печатает план построчно — так ORM-код
// может на лету проверить, не деградировал ли план (например, в интеграционном тесте).
func printPlan(ctx context.Context, pool *pgxpool.Pool, sql string) {
	pr, err := pool.Query(ctx, sql)
	if err != nil {
		panic(err)
	}
	defer pr.Close()
	for pr.Next() {
		var line string
		if err := pr.Scan(&line); err != nil {
			panic(err)
		}
		fmt.Println(line)
	}
}
