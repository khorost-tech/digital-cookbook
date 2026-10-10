package main

// Activity этого профиля пишут в РЕАЛЬНУЮ таблицу — иначе задвоение
// эффекта при ретрае нечем показать. Счётчик в памяти процесса умер бы
// вместе с процессом ровно там, где начинается интересное.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.temporal.io/sdk/activity"
)

const effectsSchema = `
CREATE TABLE IF NOT EXISTS side_effects (
    id            BIGSERIAL PRIMARY KEY,
    order_id      TEXT        NOT NULL,
    amount        BIGINT      NOT NULL,
    idempotency   TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS side_effects_idem
    ON side_effects (idempotency) WHERE idempotency IS NOT NULL;
CREATE TABLE IF NOT EXISTS import_progress (
    job_id     TEXT PRIMARY KEY,
    done       INT         NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`

// Acts — набор activity профиля.
type Acts struct {
	DB *sql.DB
	// FailTimes — сколько первых попыток должны упасть транзиентной
	// ошибкой. Так воспроизводится ретрай без ожидания настоящего сбоя.
	FailTimes int
}

func NewActs(dsn string, failTimes int) (*Acts, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(effectsSchema); err != nil {
		return nil, fmt.Errorf("схема: %w", err)
	}
	return &Acts{DB: db, FailTimes: failTimes}, nil
}

// ChargeUnsafe — НЕидемпотентная activity: каждый вызов добавляет строку.
// Ретрай после частичного успеха задваивает эффект. Ошибка бросается
// ПОСЛЕ записи — именно так выглядит реальный сбой: работа сделана,
// подтверждение до вызывающего не дошло.
func (a *Acts) ChargeUnsafe(ctx context.Context, orderID string, amount int64) error {
	info := activity.GetInfo(ctx)
	if _, err := a.DB.ExecContext(ctx,
		`INSERT INTO side_effects (order_id, amount) VALUES ($1, $2)`, orderID, amount); err != nil {
		return err
	}
	if int(info.Attempt) <= a.FailTimes {
		log.Printf(">>> ChargeUnsafe(%s) попытка %d: запись сделана, отвечаем ошибкой", orderID, info.Attempt)
		return errors.New("транзиентный сбой после записи")
	}
	log.Printf(">>> ChargeUnsafe(%s) попытка %d: успех", orderID, info.Attempt)
	return nil
}

// ChargeIdempotent — та же работа с ключом идемпотентности. Повторный
// вызов с тем же ключом не создаёт второй строки: за это отвечает
// уникальный индекс, а не аккуратность вызывающего.
func (a *Acts) ChargeIdempotent(ctx context.Context, orderID string, amount int64) error {
	info := activity.GetInfo(ctx)
	// Ключ выводится из идентификатора работы, а НЕ генерируется внутри
	// activity: сгенерированный внутри был бы новым на каждой попытке и
	// не защищал бы ни от чего.
	key := fmt.Sprintf("%s/%s", info.WorkflowExecution.ID, orderID)
	// Предикат частичного индекса в ON CONFLICT обязателен: без него
	// Postgres не выводит, какой именно индекс имеется в виду, и падает
	// с «there is no unique or exclusion constraint matching the
	// ON CONFLICT specification».
	if _, err := a.DB.ExecContext(ctx,
		`INSERT INTO side_effects (order_id, amount, idempotency) VALUES ($1, $2, $3)
		 ON CONFLICT (idempotency) WHERE idempotency IS NOT NULL DO NOTHING`,
		orderID, amount, key); err != nil {
		return err
	}
	if int(info.Attempt) <= a.FailTimes {
		log.Printf(">>> ChargeIdempotent(%s) попытка %d: запись сделана, отвечаем ошибкой", orderID, info.Attempt)
		return errors.New("транзиентный сбой после записи")
	}
	log.Printf(">>> ChargeIdempotent(%s) попытка %d: успех", orderID, info.Attempt)
	return nil
}

// LongImport — долгая activity с heartbeat. Прогресс кладётся и в
// heartbeat, и в таблицу: после убийства воркера новая попытка читает
// heartbeat-детали и продолжает с последней точки, а не с нуля.
func (a *Acts) LongImport(ctx context.Context, jobID string, total int) (int, error) {
	done := 0
	if activity.HasHeartbeatDetails(ctx) {
		if err := activity.GetHeartbeatDetails(ctx, &done); err != nil {
			return 0, fmt.Errorf("чтение heartbeat: %w", err)
		}
		log.Printf(">>> LongImport(%s) ПРОДОЛЖАЕМ с %d из %d", jobID, done, total)
	} else {
		log.Printf(">>> LongImport(%s) начинаем с нуля из %d", jobID, total)
	}

	for ; done < total; done++ {
		select {
		case <-ctx.Done():
			return done, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if _, err := a.DB.ExecContext(ctx,
			`INSERT INTO import_progress (job_id, done) VALUES ($1, $2)
			 ON CONFLICT (job_id) DO UPDATE SET done = EXCLUDED.done, updated_at = now()`,
			jobID, done+1); err != nil {
			return done, err
		}
		activity.RecordHeartbeat(ctx, done+1)
	}
	log.Printf(">>> LongImport(%s) завершён: %d", jobID, done)
	return done, nil
}

// Ping — минимальная activity для замера накладных расходов вызова.
func (a *Acts) Ping(_ context.Context) error { return nil }
