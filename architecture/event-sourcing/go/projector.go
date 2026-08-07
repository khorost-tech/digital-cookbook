package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Projector — асинхронный проектор read-модели.
//
// Инвариант надёжности: чтение событий по global_pos после чекпоинта, применение
// к таблице проекции и продвижение чекпоинта происходят В ОДНОЙ транзакции. Поэтому
// проектор можно убить в любой момент: после перезапуска он продолжит ровно с
// последнего зафиксированного global_pos — ни потерь, ни дублей.
//
// Применение к проекции идемпотентно (guard `WHERE version = $ - 1` в UPSERT): даже
// если та же пачка событий прилетит повторно, уже применённые события станут no-op.
type Projector struct {
	pool  *pgxpool.Pool
	table string // целевая таблица проекции: balances / balances_v2
	name  string // имя чекпоинта в projection_checkpoint
}

func NewProjector(pool *pgxpool.Pool, table, name string) *Projector {
	return &Projector{pool: pool, table: table, name: name}
}

// RunBatch обрабатывает одну пачку (до limit событий) и коммитит чекпоинт.
// Возвращает число обработанных событий (0 — догнали хвост).
func (p *Projector) RunBatch(ctx context.Context, limit int) (int, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// Чекпоинт под блокировкой строки: два экземпляра проектора не будут топтаться.
	var lastPos int64
	err = tx.QueryRow(ctx,
		`INSERT INTO projection_checkpoint(name, last_pos) VALUES($1, 0)
		 ON CONFLICT (name) DO UPDATE SET last_pos = projection_checkpoint.last_pos
		 RETURNING last_pos`, p.name).Scan(&lastPos)
	if err != nil {
		return 0, err
	}

	rows, err := tx.Query(ctx,
		`SELECT stream_id, version, event_type, schema_ver, data, global_pos
		   FROM events WHERE global_pos>$1 ORDER BY global_pos LIMIT $2`, lastPos, limit)
	if err != nil {
		return 0, err
	}
	var batch []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.StreamID, &e.Version, &e.EventType, &e.SchemaVer, &e.Data, &e.GlobalPos); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, tx.Commit(ctx)
	}

	var newPos int64
	for _, e := range batch {
		if err := applyToProjection(ctx, tx, p.table, e); err != nil {
			return 0, err
		}
		newPos = e.GlobalPos
	}

	// Чекпоинт продвигается В ТОЙ ЖЕ транзакции — атомарно с изменениями проекции.
	if _, err := tx.Exec(ctx,
		`UPDATE projection_checkpoint SET last_pos=$1 WHERE name=$2`, newPos, p.name); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(batch), nil
}

// Drain прогоняет проектор до конца истории пачками по batchSize.
func (p *Projector) Drain(ctx context.Context, batchSize int) (int, error) {
	total := 0
	for {
		n, err := p.RunBatch(ctx, batchSize)
		if err != nil {
			return total, err
		}
		total += n
		if n == 0 {
			return total, nil
		}
	}
}

// applyToProjection идемпотентно применяет одно событие к таблице проекции.
// Guard `WHERE <table>.version = $ver - 1` не даёт применить событие дважды:
// если проекция уже на этой (или большей) версии, UPDATE ничего не трогает.
func applyToProjection(ctx context.Context, tx pgx.Tx, table string, e Event) error {
	switch e.EventType {
	case EvtRegistered:
		var d struct {
			Capacity float64 `json:"capacity"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return err
		}
		// Регистрация — первое событие стрима (version=1): ветка INSERT.
		_, err := tx.Exec(ctx, fmt.Sprintf(
			`INSERT INTO %s (stream_id, level, capacity, version) VALUES ($1, 0, $2, $3)
			 ON CONFLICT (stream_id) DO UPDATE SET capacity=$2, version=$3
			 WHERE %s.version = $3 - 1`, table, table),
			e.StreamID, d.Capacity, e.Version)
		return err
	case EvtAdded, EvtConsumed:
		var d struct {
			Amount float64 `json:"amount"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return err
		}
		delta := d.Amount
		if e.EventType == EvtConsumed {
			delta = -delta
		}
		_, err := tx.Exec(ctx, fmt.Sprintf(
			`INSERT INTO %s (stream_id, level, capacity, version) VALUES ($1, $2, 0, $3)
			 ON CONFLICT (stream_id) DO UPDATE SET level = %s.level + $2, version = $3
			 WHERE %s.version = $3 - 1`, table, table, table),
			e.StreamID, delta, e.Version)
		return err
	default:
		return fmt.Errorf("проектор: неизвестный тип события %s", e.EventType)
	}
}
