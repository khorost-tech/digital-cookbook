package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQL: атомарный условный UPDATE — списание и проверка инварианта в одном statement,
// под защитой строчной блокировки СУБД. Ни retry, ни изоляции сверху не нужно.
type pgStore struct{ pool *pgxpool.Pool }

func newPG() (Store, error) {
	cfg, err := pgxpool.ParseConfig(envOr("PG_DSN", "postgres://demo:demo@localhost:5442/demo"))
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 48
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(context.Background(),
		"CREATE TABLE IF NOT EXISTS accounts (id BIGINT PRIMARY KEY, balance BIGINT)"); err != nil {
		pool.Close()
		return nil, err
	}
	return &pgStore{pool}, nil
}

func (s *pgStore) Name() string { return "pg" }

func (s *pgStore) Reset(ctx context.Context, budget int64) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO accounts (id, balance) VALUES (1, $1) ON CONFLICT (id) DO UPDATE SET balance = $1", budget)
	return err
}

func (s *pgStore) Decrement(ctx context.Context) (bool, int64) {
	ct, err := s.pool.Exec(ctx, "UPDATE accounts SET balance = balance - 1 WHERE id = 1 AND balance > 0")
	if err != nil {
		return false, 0
	}
	return ct.RowsAffected() == 1, 0
}

func (s *pgStore) Final(ctx context.Context) int64 {
	var b int64
	_ = s.pool.QueryRow(ctx, "SELECT balance FROM accounts WHERE id = 1").Scan(&b)
	return b
}

func (s *pgStore) Close() { s.pool.Close() }
