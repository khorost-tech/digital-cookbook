package main

// Самодельный оркестратор на таблице состояний — то, что пишут вместо
// durable execution. Он переживает падение процесса, но всю механику
// приходится держать самому: таблицу, переходы, идемпотентность шага,
// подбор незавершённых процессов при старте, персистентный таймер.
//
// Именно ОБЪЁМ этого файла — аргумент статьи 1, а не его недостатки.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"tech.khorost/temporal-cookbook/internal/provisioning"
)

const smSchema = `
CREATE TABLE IF NOT EXISTS provisioning_state (
    id             TEXT PRIMARY KEY,
    resource       TEXT        NOT NULL,
    step           TEXT        NOT NULL,   -- started|checked|reserved|allocated
    reservation_id TEXT,
    resume_after   TIMESTAMPTZ,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);`

type stateMachine struct {
	db   *sql.DB
	acts *provisioning.Activities
}

func newStateMachine(dsn string) (*stateMachine, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(smSchema); err != nil {
		return nil, fmt.Errorf("схема: %w", err)
	}
	return &stateMachine{
		db:   db,
		acts: &provisioning.Activities{Latency: 300 * time.Millisecond},
	}, nil
}

// step читает текущее состояние и делает РОВНО один переход. Вся
// «durability» здесь — руками: запись после каждого шага, чтение при старте.
func (s *stateMachine) step(ctx context.Context, id, resource string, pause time.Duration) (bool, error) {
	var stepName string
	var reservationID sql.NullString
	var resumeAfter sql.NullTime

	err := s.db.QueryRowContext(ctx,
		`SELECT step, reservation_id, resume_after FROM provisioning_state WHERE id = $1`, id).
		Scan(&stepName, &reservationID, &resumeAfter)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO provisioning_state (id, resource, step) VALUES ($1, $2, 'started')`,
			id, resource); err != nil {
			return false, err
		}
		stepName = "started"
	case err != nil:
		return false, err
	}

	switch stepName {
	case "started":
		ok, err := s.acts.CheckAvailability(ctx, resource)
		if err != nil {
			return false, err
		}
		if !ok {
			return true, fmt.Errorf("ресурс %q недоступен", resource)
		}
		_, err = s.db.ExecContext(ctx,
			`UPDATE provisioning_state SET step='checked', updated_at=now() WHERE id=$1`, id)
		return false, err

	case "checked":
		rid, err := s.acts.Reserve(ctx, resource)
		if err != nil {
			return false, err
		}
		// Момент возобновления пишем в базу: таймер тоже приходится
		// персистить самому, иначе пауза не переживёт перезапуск.
		_, err = s.db.ExecContext(ctx,
			`UPDATE provisioning_state
			    SET step='reserved', reservation_id=$2,
			        resume_after=now()+make_interval(secs => $3), updated_at=now()
			  WHERE id=$1`,
			id, rid, pause.Seconds())
		log.Printf("[statemachine] бронь %s, пауза до resume_after", rid)
		return false, err

	case "reserved":
		if resumeAfter.Valid && time.Now().Before(resumeAfter.Time) {
			return false, nil // ещё рано, ждём
		}
		msg, err := s.acts.Allocate(ctx, reservationID.String)
		if err != nil {
			return false, err
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE provisioning_state SET step='allocated', updated_at=now() WHERE id=$1`, id); err != nil {
			return false, err
		}
		log.Printf("[statemachine] ЗАВЕРШЕНО: %s", msg)
		return true, nil

	case "allocated":
		return true, nil
	}
	return false, fmt.Errorf("неизвестный шаг %q", stepName)
}

// runStateMachine крутит переходы, пока процесс не завершится. Перезапуск
// подхватывает состояние из таблицы — но подбор, опрос и таймер написаны
// вручную, и любая новая ветка процесса означает правку схемы и этого цикла.
func runStateMachine(ctx context.Context, dsn, id, resource string, pause time.Duration) error {
	sm, err := newStateMachine(dsn)
	if err != nil {
		return err
	}
	defer func() { _ = sm.db.Close() }()

	for {
		done, err := sm.step(ctx, id, resource, pause)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}
