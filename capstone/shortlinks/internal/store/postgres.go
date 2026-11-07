// Package store — ссылки в PostgreSQL через pgx.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"shortlinks/internal/links"
	"shortlinks/internal/telemetry"
)

type Postgres struct {
	Pool    *pgxpool.Pool
	Outbox  bool         // событие создания — строкой outbox в той же транзакции
	Reads   atomic.Int64 // число чтений ссылки из базы (для опыта со stampede)
	OnWrite func(links.Link)
}

func Open(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = maxConns
	return pgxpool.NewWithConfig(ctx, cfg)
}

func (p *Postgres) Create(ctx context.Context, l links.Link) error {
	err := p.create(ctx, l)
	if err == nil && p.OnWrite != nil {
		p.OnWrite(l)
	}
	return err
}

func (p *Postgres) create(ctx context.Context, l links.Link) error {
	ctx, span := telemetry.Tracer().Start(ctx, "pg insert link")
	defer span.End()
	return pgx.BeginFunc(ctx, p.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO links (code, url, created_at) VALUES ($1, $2, $3)`, l.Code, l.URL, l.CreatedAt)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return links.ErrCodeTaken
		}
		if err != nil {
			return err
		}
		if !p.Outbox {
			return nil
		}
		payload, _ := json.Marshal(map[string]any{"code": l.Code, "url": l.URL, "created_at": l.CreatedAt})
		_, err = tx.Exec(ctx, `INSERT INTO outbox (subject, payload) VALUES ('links.created', $1)`, payload)
		return err
	})
}

func (p *Postgres) Get(ctx context.Context, code string) (links.Link, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "pg select link")
	defer span.End()
	p.Reads.Add(1)
	var l links.Link
	err := p.Pool.QueryRow(ctx, `SELECT code, url, created_at FROM links WHERE code = $1`, code).Scan(&l.Code, &l.URL, &l.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return links.Link{}, links.ErrNotFound
	}
	return l, err
}

func (p *Postgres) Clicks(ctx context.Context, code string) (int64, error) {
	var n int64
	err := p.Pool.QueryRow(ctx, `SELECT coalesce(sum(clicks), 0) FROM clicks_by_minute WHERE code = $1`, code).Scan(&n)
	return n, err
}

// OutboxBatch — неопубликованные события по порядку; publish вызывается для каждого,
// отметка ставится только после успешной публикации. Повтор после сбоя публикует
// событие ещё раз — получатель обязан быть идемпотентным.
func (p *Postgres) OutboxBatch(ctx context.Context, limit int, publish func(id int64, subject string, payload []byte) error) (int, error) {
	rows, err := p.Pool.Query(ctx, `SELECT id, subject, payload FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type item struct {
		id      int64
		subject string
		payload []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.subject, &it.payload); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	done := 0
	for _, it := range items {
		if err := publish(it.id, it.subject, it.payload); err != nil {
			return done, err
		}
		if _, err := p.Pool.Exec(ctx, `UPDATE outbox SET published_at = $2 WHERE id = $1`, it.id, time.Now()); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}
