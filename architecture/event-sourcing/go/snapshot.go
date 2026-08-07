package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SaveSnapshot сохраняет снимок состояния агрегата на его текущей версии.
// Идемпотентно: повторный снапшот той же версии — no-op.
func SaveSnapshot(ctx context.Context, pool *pgxpool.Pool, t *Tank) error {
	state, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx,
		`INSERT INTO snapshots (stream_id, version, state) VALUES ($1, $2, $3)
		 ON CONFLICT (stream_id, version) DO NOTHING`, t.StreamID, t.Version, state)
	return err
}

// LoadAggregate загружает агрегат = последний снапшот + хвост событий после него.
// Если снапшота нет — свёртка с нуля. Второй возврат — был ли использован снапшот
// (для наглядности в логах: сколько событий удалось не читать).
func LoadAggregate(ctx context.Context, store *Store, pool *pgxpool.Pool, streamID string) (*Tank, bool, error) {
	var (
		snapVer int64
		state   []byte
	)
	err := pool.QueryRow(ctx,
		`SELECT version, state FROM snapshots WHERE stream_id=$1 ORDER BY version DESC LIMIT 1`,
		streamID).Scan(&snapVer, &state)
	usedSnapshot := true
	if errors.Is(err, pgx.ErrNoRows) {
		usedSnapshot = false
		snapVer = 0
	} else if err != nil {
		return nil, false, err
	}

	t := &Tank{StreamID: streamID}
	if usedSnapshot {
		if err := json.Unmarshal(state, t); err != nil {
			return nil, false, err
		}
	}

	tail, err := store.ReadStreamAfter(ctx, streamID, snapVer)
	if err != nil {
		return nil, false, err
	}
	for _, e := range tail {
		if err := t.Apply(e); err != nil {
			return nil, false, err
		}
	}
	return t, usedSnapshot, nil
}
