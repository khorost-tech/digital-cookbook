package main

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Order — денормализованное представление заказа (то, что видит read-сторона).
// updatedSeq — позиция (seq) события, до которого «свёрнут» этот заказ; служит и
// токеном read-your-writes: «моя запись видна, когда проектор дошёл до этого seq».
type Order struct {
	OrderID    int64
	UserID     string
	Status     string // "new" | "paid"
	Amount     int64
	UpdatedSeq int64
}

// setupWriteModel создаёт write-сторону: единственную append-only таблицу событий.
// Это и есть «источник» для проекций — намеренно простой (без отдельного event store
// со снапшотами: тем занимается соседний стенд event-sourcing/). Команды только
// добавляют строки; текущее состояние получается сверткой (fold) этого лога.
func setupWriteModel(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS order_events;
		CREATE TABLE order_events (
			seq        BIGSERIAL PRIMARY KEY,   -- монотонная позиция в логе (хвост = max(seq))
			order_id   BIGINT      NOT NULL,
			user_id    TEXT        NOT NULL,
			type       TEXT        NOT NULL,     -- 'created' | 'paid'
			amount     BIGINT      NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX ON order_events (user_id);
		-- Отдельная последовательность для order_id, чтобы id заказа не совпадал с seq лога
		-- (в реальности это разные пространства: бизнес-id заказа vs позиция в журнале).
		DROP SEQUENCE IF EXISTS order_id_seq;
		CREATE SEQUENCE order_id_seq START 1000;
	`)
	return err
}

// createOrder — команда write-стороны: заводит новый заказ. Возвращает id заказа и
// seq порождённого события (токен позиции: по нему read-сторона понимает, догнала ли
// проекция эту запись). Никаких проекций тут не трогаем — только append в лог.
func createOrder(ctx context.Context, pool *pgxpool.Pool, userID string, amount int64) (orderID, seq int64, err error) {
	err = pool.QueryRow(ctx, `SELECT nextval('order_id_seq')`).Scan(&orderID)
	if err != nil {
		return 0, 0, err
	}
	err = pool.QueryRow(ctx,
		`INSERT INTO order_events (order_id, user_id, type, amount)
		 VALUES ($1, $2, 'created', $3) RETURNING seq`,
		orderID, userID, amount).Scan(&seq)
	return orderID, seq, err
}

// payOrder — команда: помечает заказ оплаченным (ещё одно событие в логе).
func payOrder(ctx context.Context, pool *pgxpool.Pool, orderID int64) (seq int64, err error) {
	var userID string
	err = pool.QueryRow(ctx,
		`SELECT user_id FROM order_events WHERE order_id=$1 AND type='created'`, orderID).Scan(&userID)
	if err != nil {
		return 0, err
	}
	err = pool.QueryRow(ctx,
		`INSERT INTO order_events (order_id, user_id, type) VALUES ($1, $2, 'paid') RETURNING seq`,
		orderID, userID).Scan(&seq)
	return seq, err
}

// tailSeq — позиция хвоста лога (максимальный seq). Вместе с чекпоинтом проектора
// даёт лаг проекции.
func tailSeq(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var t int64
	err := pool.QueryRow(ctx, `SELECT coalesce(max(seq), 0) FROM order_events`).Scan(&t)
	return t, err
}

// foldUserOrdersWriteSide — АВТОРИТЕТНОЕ текущее состояние заказов пользователя,
// свёрнутое прямо из лога событий (write-сторона). Всегда свежее — здесь нет лага.
// Именно этот путь используется для read-your-writes: собственные, только что
// записанные заказы читаем отсюда, а не из отстающей проекции.
func foldUserOrdersWriteSide(ctx context.Context, pool *pgxpool.Pool, userID string) ([]Order, error) {
	rows, err := pool.Query(ctx, `
		SELECT e.order_id, e.user_id,
		       (SELECT amount FROM order_events c
		          WHERE c.order_id = e.order_id AND c.type = 'created')            AS amount,
		       (SELECT CASE WHEN bool_or(type = 'paid') THEN 'paid' ELSE 'new' END
		          FROM order_events s WHERE s.order_id = e.order_id)               AS status,
		       max(e.seq)                                                          AS updated_seq
		  FROM order_events e
		 WHERE e.user_id = $1
		 GROUP BY e.order_id, e.user_id
		 ORDER BY e.order_id`, userID)
	if err != nil {
		return nil, err
	}
	return scanOrders(rows)
}

func scanOrders(rows pgx.Rows) ([]Order, error) {
	defer rows.Close()
	var out []Order
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.OrderID, &o.UserID, &o.Amount, &o.Status, &o.UpdatedSeq); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
