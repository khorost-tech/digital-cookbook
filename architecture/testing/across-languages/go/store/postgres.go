// Package store — реализация service.Store поверх Postgres. Нужна для ПРИЁМА 4:
// интеграционный тест против настоящей СУБД в контейнере, а не против фейка.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"tech.khorost/across-languages-go/service"
)

const Schema = `
CREATE TABLE IF NOT EXISTS orders (
    id         TEXT PRIMARY KEY,
    user_id    TEXT   NOT NULL,
    total_cent BIGINT NOT NULL,
    price_cent BIGINT NOT NULL
);`

// Postgres реализует service.Store.
type Postgres struct{ Pool *pgxpool.Pool }

func (p Postgres) Save(ctx context.Context, o service.Order) error {
	_, err := p.Pool.Exec(ctx,
		`INSERT INTO orders (id, user_id, total_cent, price_cent) VALUES ($1, $2, $3, $4)`,
		o.ID, o.UserID, o.TotalCent, o.PriceCent)
	if err != nil {
		return fmt.Errorf("вставка заказа %s: %w", o.ID, err)
	}
	return nil
}

func (p Postgres) ByUser(ctx context.Context, userID string) ([]service.Order, error) {
	rows, err := p.Pool.Query(ctx,
		`SELECT id, user_id, total_cent, price_cent FROM orders WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("выборка заказов %s: %w", userID, err)
	}
	defer rows.Close()

	var out []service.Order
	for rows.Next() {
		var o service.Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.TotalCent, &o.PriceCent); err != nil {
			return nil, fmt.Errorf("чтение строки: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
