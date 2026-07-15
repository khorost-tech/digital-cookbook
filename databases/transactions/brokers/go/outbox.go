package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
)

// runOutbox: outbox pattern — мост между БД-транзакцией и брокером. Kafka-транзакции не
// покрывают внешние сайд-эффекты (запись в БД), а распределённой транзакции между PostgreSQL и
// Kafka нет. Решение: бизнес-изменение И событие пишем в ОДНОЙ БД-транзакции (атомарно); отдельный
// релей потом читает неопубликованные события из outbox и публикует в Kafka, помечая published.
// Даже если публикация упадёт — событие durable в БД, релей повторит; событие не теряется.
func runOutbox() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, envOr("DSN", "postgres://demo:demo@localhost:5441/demo"))
	if err != nil {
		fmt.Println("pg:", err)
		return
	}
	defer pool.Close()

	mustExec(ctx, pool, `CREATE TABLE IF NOT EXISTS orders (id BIGSERIAL PRIMARY KEY, item TEXT)`)
	mustExec(ctx, pool, `CREATE TABLE IF NOT EXISTS outbox (id BIGSERIAL PRIMARY KEY, payload TEXT, published BOOLEAN NOT NULL DEFAULT false)`)
	mustExec(ctx, pool, `TRUNCATE orders, outbox`)

	const n = 20
	// бизнес-операция + событие — в ОДНОЙ транзакции: либо оба, либо ни одного
	for i := 0; i < n; i++ {
		tx, err := pool.Begin(ctx)
		if err != nil {
			continue
		}
		_, _ = tx.Exec(ctx, `INSERT INTO orders (item) VALUES ($1)`, fmt.Sprintf("item-%d", i))
		_, _ = tx.Exec(ctx, `INSERT INTO outbox (payload) VALUES ($1)`, fmt.Sprintf("order-created-%d", i))
		_ = tx.Commit(ctx)
	}

	// релей: читаем неопубликованные, публикуем в Kafka, помечаем published
	prod, err := kgo.NewClient(
		kgo.SeedBrokers(envOr("KAFKA", "localhost:9095")),
		kgo.DefaultProduceTopic("orders"),
		kgo.AllowAutoTopicCreation(),
	)
	if err != nil {
		fmt.Println("kafka:", err)
		return
	}
	defer prod.Close()

	type ob struct {
		id      int64
		payload string
	}
	var pending []ob
	rows, _ := pool.Query(ctx, `SELECT id, payload FROM outbox WHERE NOT published ORDER BY id`)
	for rows.Next() {
		var o ob
		_ = rows.Scan(&o.id, &o.payload)
		pending = append(pending, o)
	}
	rows.Close()

	published := 0
	for _, o := range pending {
		if err := prod.ProduceSync(ctx, &kgo.Record{Value: []byte(o.payload)}).FirstErr(); err == nil {
			mustExec(ctx, pool, `UPDATE outbox SET published = true WHERE id = $1`, o.id)
			published++
		}
	}

	var orders, unpub int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&orders)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE NOT published`).Scan(&unpub)
	fmt.Printf("outbox: %d заказов + %d событий записаны атомарно с БД; релей опубликовал %d в Kafka; неопубликованных осталось %d\n",
		orders, n, published, unpub)
}

func mustExec(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		fmt.Printf("exec %q: %v\n", sql, err)
	}
}
