package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Product struct {
	ID         int64             `json:"id"`
	SKU        string            `json:"sku"`
	Title      string            `json:"title"`
	PriceCents int64             `json:"price_cents"`
	Attrs      map[string]string `json:"attrs"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type View struct {
	ProductID int64     `json:"product_id"`
	UserID    int64     `json:"user_id"`
	Ts        time.Time `json:"ts"`
}

// base — фиксированная точка отсчёта: без неё датасет не воспроизводим.
var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var categories = []string{"tools", "garden", "kitchen", "auto", "sport", "books"}

func Generate(seed int64, products, users, viewsPerUser int) ([]Product, []View) {
	r := rand.New(rand.NewSource(seed))
	ps := make([]Product, 0, products)
	for i := 0; i < products; i++ {
		cat := categories[r.Intn(len(categories))]
		ps = append(ps, Product{
			ID:         int64(i + 1),
			SKU:        fmt.Sprintf("%s-%06d", cat, i+1),
			Title:      fmt.Sprintf("%s item %d", cat, i+1),
			PriceCents: int64(100 + r.Intn(999_900)),
			Attrs: map[string]string{
				"category": cat,
				"color":    []string{"red", "green", "blue", "black"}[r.Intn(4)],
				"weight_g": fmt.Sprintf("%d", 50+r.Intn(20_000)),
			},
			UpdatedAt: base.Add(time.Duration(r.Intn(180*24)) * time.Hour),
		})
	}
	// Зипфово распределение просмотров: горячих товаров мало, и именно это делает
	// кэш осмысленным. Равномерное распределение дало бы честный, но бесполезный
	// стенд — hit rate падал бы до доли рабочего набора в кэше.
	vs := make([]View, 0, users*viewsPerUser)
	zipf := rand.NewZipf(r, 1.2, 1, uint64(products-1))
	for u := 1; u <= users; u++ {
		for k := 0; k < viewsPerUser; k++ {
			vs = append(vs, View{
				ProductID: int64(zipf.Uint64() + 1),
				UserID:    int64(u),
				Ts:        base.Add(time.Duration(r.Intn(180*24)) * time.Hour),
			})
		}
	}
	return ps, vs
}

func main() {
	var (
		seed         = flag.Int64("seed", 42, "seed генератора")
		products     = flag.Int("products", 200_000, "число товаров")
		users        = flag.Int("users", 5_000, "число пользователей")
		viewsPerUser = flag.Int("views-per-user", 20, "просмотров на пользователя")
		load         = flag.Bool("load", false, "загрузить в PostgreSQL")
		out          = flag.String("out", "", "ndjson — печатать в stdout")
		dsn          = flag.String("dsn", os.Getenv("ORIGIN_DSN"), "DSN PostgreSQL")
	)
	flag.Parse()

	ps, vs := Generate(*seed, *products, *users, *viewsPerUser)
	fmt.Fprintf(os.Stderr, "сгенерировано: products=%d views=%d (seed=%d)\n", len(ps), len(vs), *seed)

	if *out == "ndjson" {
		enc := json.NewEncoder(os.Stdout)
		for _, p := range ps {
			if err := enc.Encode(p); err != nil {
				panic(err)
			}
		}
	}
	if *load {
		if *dsn == "" {
			panic("нужен -dsn или ORIGIN_DSN")
		}
		if err := loadPG(*dsn, ps, vs); err != nil {
			panic(err)
		}
	}
}

func loadPG(dsn string, ps []Product, vs []View) error {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	schema := `
CREATE TABLE IF NOT EXISTS products (
  id BIGINT PRIMARY KEY,
  sku TEXT NOT NULL,
  title TEXT NOT NULL,
  price_cents BIGINT NOT NULL,
  attrs JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS views (
  product_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  ts TIMESTAMPTZ NOT NULL
);
TRUNCATE products, views;`
	if _, err := pool.Exec(ctx, schema); err != nil {
		return err
	}

	rows := make([][]any, 0, len(ps))
	for _, p := range ps {
		attrs, _ := json.Marshal(p.Attrs)
		rows = append(rows, []any{p.ID, p.SKU, p.Title, p.PriceCents, attrs, p.UpdatedAt})
	}
	n, err := pool.CopyFrom(ctx, []string{"products"},
		[]string{"id", "sku", "title", "price_cents", "attrs", "updated_at"},
		pgxCopyRows(rows))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "загружено products: %d\n", n)

	vrows := make([][]any, 0, len(vs))
	for _, v := range vs {
		vrows = append(vrows, []any{v.ProductID, v.UserID, v.Ts})
	}
	n, err = pool.CopyFrom(ctx, []string{"views"},
		[]string{"product_id", "user_id", "ts"}, pgxCopyRows(vrows))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "загружено views: %d\n", n)
	return nil
}

func pgxCopyRows(rows [][]any) pgx.CopyFromSource { return pgx.CopyFromRows(rows) }
