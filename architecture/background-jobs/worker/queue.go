package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Job — джоба, выданная воркеру во владение на срок аренды.
type Job struct {
	ID          int64
	Kind        string
	Payload     []byte
	Attempt     int32
	MaxAttempts int32
}

// claimSQL — сердце очереди. Разбор по частям:
//   - подзапрос выбирает ОДНУ queued-джобу, готовую к запуску (run_at <= now());
//   - FOR UPDATE SKIP LOCKED: строки, уже заблокированные другими воркерами,
//     ПРОПУСКАЮТСЯ, а не ставятся в очередь ожидания. Без SKIP LOCKED N воркеров
//     выстроились бы в очередь за одной строкой и работали бы по очереди;
//   - внешний UPDATE переводит джобу в running и ставит аренду: до leased_until
//     она принадлежит leased_by и невидима для чужих claim (state != 'queued').
const claimSQL = `
UPDATE jobs SET
    state        = 'running',
    attempt      = attempt + 1,
    leased_by    = $1,
    leased_until = now() + $2::interval,
    updated_at   = now()
WHERE id = (
    SELECT id FROM jobs
    WHERE state = 'queued' AND run_at <= now()
    ORDER BY run_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING id, kind, payload, attempt, max_attempts`

func Claim(ctx context.Context, db *pgxpool.Pool, workerID string, lease time.Duration) (*Job, error) {
	var j Job
	err := db.QueryRow(ctx, claimSQL, workerID, lease.String()).
		Scan(&j.ID, &j.Kind, &j.Payload, &j.Attempt, &j.MaxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // очередь пуста — это не ошибка
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// Heartbeat продлевает аренду. Условие leased_by = $2 обязательно: если аренда
// уже истекла и джобу забрал другой воркер, продлевать её НЕЛЬЗЯ — иначе два
// воркера считали бы себя владельцами. false здесь означает «ты больше не
// владелец», и обработку надо прекращать.
const heartbeatSQL = `
UPDATE jobs SET leased_until = now() + $3::interval, updated_at = now()
WHERE id = $1 AND leased_by = $2 AND state = 'running'`

func Heartbeat(ctx context.Context, db *pgxpool.Pool, id int64, workerID string, lease time.Duration) (bool, error) {
	tag, err := db.Exec(ctx, heartbeatSQL, id, workerID, lease.String())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

const completeSQL = `
UPDATE jobs SET state='done', leased_by=NULL, leased_until=NULL, updated_at=now()
WHERE id = $1 AND leased_by = $2 AND state = 'running'`

func Complete(ctx context.Context, db *pgxpool.Pool, id int64, workerID string) (bool, error) {
	tag, err := db.Exec(ctx, completeSQL, id, workerID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Fail возвращает джобу в очередь, пока не исчерпаны попытки, иначе помечает
// failed. Политика повторов (интервалы, backoff) НЕ разбирается в этой серии —
// см. architecture/resilience-patterns; здесь важно лишь то, что джоба не
// теряется и не остаётся вечно running.
const failSQL = `
UPDATE jobs SET
    state      = CASE WHEN attempt >= max_attempts THEN 'failed' ELSE 'queued' END,
    last_error = $3,
    leased_by  = NULL,
    leased_until = NULL,
    updated_at = now()
WHERE id = $1 AND leased_by = $2 AND state = 'running'`

func Fail(ctx context.Context, db *pgxpool.Pool, id int64, workerID string, reason string) (bool, error) {
	tag, err := db.Exec(ctx, failSQL, id, workerID, reason)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ReclaimExpired возвращает в очередь джобы, у которых истекла аренда. Это и
// есть ответ на «воркер умер посередине»: джоба не потеряна и не выполняется
// дважды одновременно — она просто снова становится доступной.
const reclaimSQL = `
UPDATE jobs SET state='queued', leased_by=NULL, leased_until=NULL, updated_at=now()
WHERE state='running' AND leased_until < now()
RETURNING id`

func ReclaimExpired(ctx context.Context, db *pgxpool.Pool) ([]int64, error) {
	rows, err := db.Query(ctx, reclaimSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
