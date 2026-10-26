package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrVersionConflict — кто-то опередил: версия уже занята.
// Распознаётся по SQLSTATE 23505 (нарушение уникальности PK), а не по тексту.
var ErrVersionConflict = errors.New("конфликт версии стрима")

// AppendEvents пишет события начиная с expectedVersion+1 одной транзакцией.
// Атомарность обязательна: половина событий команды в сторе — это порванный
// инвариант, который потом ничем не починить.
//
// schema_ver здесь ЖЁСТКО захардкожен в 1 (см. INSERT ниже) — этот путь
// (командный код: AddOxygen/ConsumeOxygen/RegisterTank -> Decide ->
// AppendEvents) никогда не пишет v3, хотя артефакт 04 называет v3 текущей
// формой события. Стрим "tank" для профиля 04 наполняется НЕ через
// AppendEvents, а прямым SQL INSERT из scripts/04-versioning.sh
// (reset_and_seed_v3/reset_and_seed_v1) — это единственный писатель формы
// v3 в стенде. Версионирующего писателя (который решал бы, какую схему
// писать для новых событий, а не только читал бы старые через Upcast) в
// коде нет; см. FIXTURES.md, «Артефакт 04».
func AppendEvents(ctx context.Context, pool *pgxpool.Pool, streamID string,
	expectedVersion int64, evs []Event) error {

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for i, e := range evs {
		payload, err := json.Marshal(e)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO events (stream_id, version, event_type, schema_ver, data)
			 VALUES ($1, $2, $3, 1, $4)`,
			streamID, expectedVersion+int64(i)+1, e.Type, payload)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrVersionConflict
			}
			return fmt.Errorf("append: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// LoadStream читает стрим целиком и возвращает свёртку с текущей версией.
func LoadStream(ctx context.Context, pool *pgxpool.Pool, streamID string) (Tank, int64, error) {
	rows, err := pool.Query(ctx,
		`SELECT version, data FROM events WHERE stream_id = $1 ORDER BY version`, streamID)
	if err != nil {
		return Tank{}, 0, err
	}
	defer rows.Close()

	var tank Tank
	var version int64
	for rows.Next() {
		var v int64
		var payload []byte
		if err := rows.Scan(&v, &payload); err != nil {
			return Tank{}, 0, err
		}
		var e Event
		if err := json.Unmarshal(payload, &e); err != nil {
			return Tank{}, 0, err
		}
		tank = tank.Apply(e)
		version = v
	}
	return tank, version, rows.Err()
}
