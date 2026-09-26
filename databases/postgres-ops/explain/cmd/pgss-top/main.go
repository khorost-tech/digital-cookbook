// pgss-top — читает топ pg_stat_statements по суммарному времени выполнения.
// Показывает, как ту же диагностику из сц.09 делают из кода (мониторинг, алерты).
//
//	PG_DSN=postgres://postgres:shopdemo@localhost:5434/shopdemo go run .
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5"
)

const query = `
SELECT calls,
       total_exec_time,
       mean_exec_time,
       rows,
       left(regexp_replace(query, '\s+', ' ', 'g'), 70) AS query
FROM pg_stat_statements
WHERE query NOT LIKE '%pg_stat_statements%'
ORDER BY total_exec_time DESC
LIMIT $1`

func main() {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		dsn = "postgres://postgres:shopdemo@localhost:5434/shopdemo"
	}
	limit := 10
	if len(os.Args) > 1 {
		if n, err := strconv.Atoi(os.Args[1]); err == nil {
			limit = n
		}
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx, query, limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Printf("%6s %12s %10s %10s  %s\n", "calls", "total_ms", "mean_ms", "rows", "query")
	for rows.Next() {
		var calls, nrows int64
		var total, mean float64
		var q string
		if err := rows.Scan(&calls, &total, &mean, &nrows, &q); err != nil {
			fmt.Fprintln(os.Stderr, "scan:", err)
			os.Exit(1)
		}
		fmt.Printf("%6d %12.1f %10.2f %10d  %s\n", calls, total, mean, nrows, q)
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "rows:", err)
		os.Exit(1)
	}
}
