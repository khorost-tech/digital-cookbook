package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrVersionConflict — оптимистичная блокировка сработала: кто-то уже записал
// эту версию стрима, INSERT нарушил PRIMARY KEY(stream_id, version).
// Обработка на стороне вызывающего: перечитать актуальную версию и повторить команду.
var ErrVersionConflict = errors.New("version conflict")

// execer — общий интерфейс пула и транзакции (Exec/Query/QueryRow).
type execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Event — событие, прочитанное из event store.
type Event struct {
	StreamID  string
	Version   int64
	EventType string
	SchemaVer int
	Data      json.RawMessage
	GlobalPos int64
}

// EventData — событие на запись (payload сериализуется в jsonb).
type EventData struct {
	Type      string
	SchemaVer int
	Data      any
}

// Store — тонкая обёртка над event store поверх pgx.
type Store struct{ db execer }

func NewStore(db execer) *Store { return &Store{db: db} }

// Append дописывает пачку событий в стрим, ожидая, что текущая версия стрима равна
// expectedVersion. Первое событие пишется с version=expectedVersion+1, второе +2 и т.д.
// Вся пачка идёт одной транзакцией: конфликт по PK на любом событии => ErrVersionConflict
// и полный откат. Возвращает новую версию стрима при успехе.
func (s *Store) Append(ctx context.Context, streamID string, expectedVersion int64, events []EventData) (int64, error) {
	pool, ok := s.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return 0, errors.New("store: db does not support transactions")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	v := expectedVersion
	for _, e := range events {
		v++
		payload, err := json.Marshal(e.Data)
		if err != nil {
			return 0, err
		}
		sv := e.SchemaVer
		if sv == 0 {
			sv = 1
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO events (stream_id, version, event_type, schema_ver, data, metadata)
			 VALUES ($1, $2, $3, $4, $5, '{}'::jsonb)`,
			streamID, v, e.Type, sv, payload)
		if err != nil {
			if isUniqueViolation(err) {
				return 0, ErrVersionConflict
			}
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		if isUniqueViolation(err) {
			return 0, ErrVersionConflict
		}
		return 0, err
	}
	return v, nil
}

// isUniqueViolation распознаёт нарушение уникальности (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ReadStream читает все события стрима в порядке версий.
func (s *Store) ReadStream(ctx context.Context, streamID string) ([]Event, error) {
	return s.scan(ctx,
		`SELECT stream_id, version, event_type, schema_ver, data, global_pos
		   FROM events WHERE stream_id=$1 ORDER BY version`, streamID)
}

// ReadStreamAfter читает хвост событий стрима с версией строго больше afterVersion
// (используется при загрузке агрегата поверх снапшота).
func (s *Store) ReadStreamAfter(ctx context.Context, streamID string, afterVersion int64) ([]Event, error) {
	return s.scan(ctx,
		`SELECT stream_id, version, event_type, schema_ver, data, global_pos
		   FROM events WHERE stream_id=$1 AND version>$2 ORDER BY version`, streamID, afterVersion)
}

// ReadAllAfter читает события всех стримов по глобальному порядку global_pos,
// строго после afterPos, не более limit штук. Это «шина» для проекторов.
func (s *Store) ReadAllAfter(ctx context.Context, afterPos int64, limit int) ([]Event, error) {
	return s.scan(ctx,
		`SELECT stream_id, version, event_type, schema_ver, data, global_pos
		   FROM events WHERE global_pos>$1 ORDER BY global_pos LIMIT $2`, afterPos, limit)
}

func (s *Store) scan(ctx context.Context, sql string, args ...any) ([]Event, error) {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.StreamID, &e.Version, &e.EventType, &e.SchemaVer, &e.Data, &e.GlobalPos); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
