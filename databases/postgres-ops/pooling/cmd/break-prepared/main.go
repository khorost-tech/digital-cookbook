// break-prepared показывает, как transaction pooling ломает server-side prepared statements.
// pgx v5 по умолчанию кеширует подготовленные выражения на server-соединении. Через
// transaction pooling каждый запрос может попасть на ДРУГОЙ server, где этого prepared нет,
// — и клиент получает "prepared statement does not exist".
//
// Прогоняем один и тот же параметризованный запрос трижды в трёх режимах:
//
//  1. через PgBouncer, обычный pgx (кеш prepared)      → ломается;
//
//  2. через PgBouncer, simple protocol (без prepared)  → работает (обходной путь);
//
//  3. напрямую к postgres, обычный pgx                 → работает (server не меняется).
//
//     go run . [pgbouncer_dsn] [postgres_dsn]
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
)

const q = "SELECT count(*) FROM demo WHERE id < $1"

// прогнать q несколько раз на данном соединении; вернуть первую ошибку
func hammer(ctx context.Context, conn *pgx.Conn, times int) error {
	for i := 0; i < times; i++ {
		var n int
		if err := conn.QueryRow(ctx, q, 500).Scan(&n); err != nil {
			return fmt.Errorf("запрос #%d: %w", i+1, err)
		}
	}
	return nil
}

func trial(name, dsn string, simple bool) {
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		fmt.Printf("%-45s НАСТРОЙКА: %v\n", name, err)
		return
	}
	if simple {
		cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol // без prepared
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		fmt.Printf("%-45s ПОДКЛЮЧЕНИЕ: %v\n", name, err)
		return
	}
	defer conn.Close(ctx)

	if err := hammer(ctx, conn, 10); err != nil {
		fmt.Printf("%-45s СЛОМАЛОСЬ: %v\n", name, err)
	} else {
		fmt.Printf("%-45s ok (10 запросов прошли)\n", name)
	}
}

func main() {
	pgbouncer := "postgres://postgres:pooldemo@localhost:6432/opsdemo"
	postgres := "postgres://postgres:pooldemo@localhost:5436/opsdemo"
	if len(os.Args) > 1 {
		pgbouncer = os.Args[1]
	}
	if len(os.Args) > 2 {
		postgres = os.Args[2]
	}

	trial("PgBouncer, обычный pgx (кеш prepared)", pgbouncer, false)
	trial("PgBouncer, simple protocol", pgbouncer, true)
	trial("напрямую к postgres, обычный pgx", postgres, false)
}
