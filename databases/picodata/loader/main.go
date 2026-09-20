// Загрузчик датасета из источника истины в Picodata.
//
// Обе стороны — PostgreSQL-протокол, и клиент ровно один: pgx/v5. К источнику
// (настоящий PostgreSQL) и к Picodata подключение отличается только строкой
// DSN. Это и есть практический смысл PG-протокола в Picodata: драйвер, пул,
// синтаксис запросов остаются теми же, что у PostgreSQL.
//
// В источнике категория лежит в JSONB-поле attrs, а просмотры — в отдельной
// таблице views; плоскую форму (category, views) собирает запрос ниже — тот же,
// которым пользуется стенд Tarantool, чтобы наборы совпадали построчно.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const selectFlat = `
SELECT p.id, p.sku, p.title, p.price_cents,
       p.attrs->>'category' AS category,
       COALESCE(v.views, 0) AS views
FROM products p
LEFT JOIN (
    SELECT product_id, count(*) AS views FROM views GROUP BY product_id
) v ON v.product_id = p.id
ORDER BY p.id`

type row struct {
	id         int64
	sku        string
	title      string
	priceCents int64
	category   string
	views      int64
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	var (
		originDSN = flag.String("origin", envOr("ORIGIN_DSN", ""), "DSN источника истины")
		picoDSN   = flag.String("pico", envOr("PICO_DSN", ""), "DSN Picodata (PG-протокол)")
		batch     = flag.Int("batch", 500, "строк в одном INSERT")
	)
	flag.Parse()

	if *originDSN == "" || *picoDSN == "" {
		log.Fatal("нужны ORIGIN_DSN и PICO_DSN")
	}

	ctx := context.Background()

	origin, err := pgxpool.New(ctx, *originDSN)
	if err != nil {
		log.Fatalf("подключение к источнику: %v", err)
	}
	defer origin.Close()

	// Тот же самый клиент, что и для PostgreSQL выше.
	pico, err := pgxpool.New(ctx, *picoDSN)
	if err != nil {
		log.Fatalf("подключение к Picodata: %v", err)
	}
	defer pico.Close()

	rows, err := origin.Query(ctx, selectFlat)
	if err != nil {
		log.Fatalf("чтение источника: %v", err)
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.id, &x.sku, &x.title, &x.priceCents, &x.category, &x.views)
		return x, err
	})
	if err != nil {
		log.Fatalf("разбор строк источника: %v", err)
	}
	fmt.Fprintf(os.Stderr, "прочитано из источника: %d строк\n", len(all))

	start := time.Now()
	for i := 0; i < len(all); i += *batch {
		end := min(i+*batch, len(all))
		chunk := all[i:end]

		var sb strings.Builder
		args := make([]any, 0, len(chunk)*6)
		sb.WriteString("INSERT INTO products (id, sku, title, price_cents, category, views) VALUES ")
		for j, r := range chunk {
			if j > 0 {
				sb.WriteString(", ")
			}
			b := j * 6
			fmt.Fprintf(&sb, "($%d, $%d, $%d, $%d, $%d, $%d)", b+1, b+2, b+3, b+4, b+5, b+6)
			args = append(args, r.id, r.sku, r.title, r.priceCents, r.category, r.views)
		}
		if _, err := pico.Exec(ctx, sb.String(), args...); err != nil {
			log.Fatalf("вставка партии [%d:%d): %v", i, end, err)
		}
	}
	fmt.Fprintf(os.Stderr, "загружено в Picodata: %d строк за %s\n", len(all), time.Since(start).Round(time.Millisecond))

	var n int64
	if err := pico.QueryRow(ctx, "SELECT count(*) FROM products").Scan(&n); err != nil {
		log.Fatalf("контрольный count(*): %v", err)
	}
	if int(n) != len(all) {
		log.Fatalf("АССЕРТ: в Picodata %d строк, в источнике %d — загрузка неполная", n, len(all))
	}
	fmt.Fprintf(os.Stderr, "контроль: count(*) = %d, совпадает с источником\n", n)
}
