package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Точечные операции — там, где противопоказан CH и выигрывает PG (Step 3
// брифа): SELECT по PK, UPDATE/DELETE по PK. id НЕ входит в CH ORDER BY
// (event_time, user_id), поэтому точечный поиск по id в CH — это скан по
// всем гранулам столбца id (нет разреженного индекса, который мог бы его
// пропустить); в PG это Index Scan/Index Only Scan по PRIMARY KEY(id).

type pointSelectResult struct {
	duration time.Duration
	found    bool
}

func pointSelectPG(ctx context.Context, pool *pgxpool.Pool, id uint64) (pointSelectResult, string, error) {
	plan, err := explainPointSelectPG(ctx, pool, id)
	if err != nil {
		return pointSelectResult{}, "", err
	}
	start := time.Now()
	var country string
	err = pool.QueryRow(ctx, "SELECT country FROM events WHERE id = $1", id).Scan(&country)
	dur := time.Since(start)
	if err != nil {
		return pointSelectResult{duration: dur, found: false}, plan, nil
	}
	return pointSelectResult{duration: dur, found: true}, plan, nil
}

func explainPointSelectPG(ctx context.Context, pool *pgxpool.Pool, id uint64) (string, error) {
	rows, err := pool.Query(ctx, fmt.Sprintf("EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) SELECT * FROM events WHERE id = %d", id))
	if err != nil {
		return "", fmt.Errorf("pg explain point select: %w", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", err
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), rows.Err()
}

// pointSelectCH выполняет точечный SELECT по id и возвращает время + число
// реально прочитанных строк (read_rows из system.query_log по query_id) —
// показывает, что CH прочитал ~весь датасет, а не одну гранулу.
func pointSelectCH(ctx context.Context, conn clickhouse.Conn, id uint64) (time.Duration, bool, uint64, error) {
	queryID := fmt.Sprintf("when-olap-point-select-%d", time.Now().UnixNano())
	qctx := clickhouse.Context(ctx, clickhouse.WithQueryID(queryID))

	start := time.Now()
	var country string
	row := conn.QueryRow(qctx, fmt.Sprintf("SELECT country FROM demo.events WHERE id = %d LIMIT 1", id))
	err := row.Scan(&country)
	dur := time.Since(start)
	found := err == nil

	// system.query_log пишется асинхронно (flush_interval_milliseconds),
	// форсируем flush перед чтением, иначе строка ещё может не появиться.
	if flushErr := conn.Exec(ctx, "SYSTEM FLUSH LOGS"); flushErr != nil {
		return dur, found, 0, fmt.Errorf("ch flush logs: %w", flushErr)
	}
	var readRows uint64
	qErr := conn.QueryRow(ctx, "SELECT read_rows FROM system.query_log WHERE query_id = ? AND type = 'QueryFinish' ORDER BY event_time DESC LIMIT 1", queryID).Scan(&readRows)
	if qErr != nil {
		// нефатально для сценария — таймингом важнее, чем read_rows
		return dur, found, 0, nil
	}
	return dur, found, readRows, nil
}

// --- Мутации ---

type mutationOutcome struct {
	submitDuration     time.Duration // время, за которое ALTER ... вернул управление клиенту
	completionDuration time.Duration // время до is_done=1 в system.mutations (реальная стоимость)
	partsToDoAtSubmit  int64
}

func chMutateUpdate(ctx context.Context, conn clickhouse.Conn, id uint64) (mutationOutcome, error) {
	return chMutation(ctx, conn, fmt.Sprintf("ALTER TABLE demo.events UPDATE revenue = 0 WHERE id = %d", id))
}

func chMutateDelete(ctx context.Context, conn clickhouse.Conn, id uint64) (mutationOutcome, error) {
	return chMutation(ctx, conn, fmt.Sprintf("ALTER TABLE demo.events DELETE WHERE id = %d", id))
}

// chMutation отправляет мутацию БЕЗ mutations_sync (реальное поведение по
// умолчанию — ALTER возвращается почти мгновенно, сама мутация асинхронна),
// затем поллит system.mutations до is_done=1 — это и есть настоящая
// стоимость операции, а не время ответа ALTER.
func chMutation(ctx context.Context, conn clickhouse.Conn, alterSQL string) (mutationOutcome, error) {
	submitStart := time.Now()
	if err := conn.Exec(ctx, alterSQL); err != nil {
		return mutationOutcome{}, fmt.Errorf("ch alter: %w", err)
	}
	submitDur := time.Since(submitStart)

	// найти мутацию по тексту команды (последняя добавленная для этой таблицы)
	var mutationID string
	var isDone uint8
	var partsToDo int64
	err := conn.QueryRow(ctx, `
		SELECT mutation_id, is_done, parts_to_do
		FROM system.mutations
		WHERE database = 'demo' AND table = 'events'
		ORDER BY create_time DESC
		LIMIT 1`).Scan(&mutationID, &isDone, &partsToDo)
	if err != nil {
		return mutationOutcome{}, fmt.Errorf("ch find mutation: %w", err)
	}
	out := mutationOutcome{submitDuration: submitDur, partsToDoAtSubmit: partsToDo}

	pollStart := time.Now()
	for {
		if isDone == 1 {
			break
		}
		if time.Since(pollStart) > 2*time.Minute {
			return out, fmt.Errorf("ch mutation %s did not finish within timeout", mutationID)
		}
		time.Sleep(200 * time.Millisecond)
		if err := conn.QueryRow(ctx, `
			SELECT is_done FROM system.mutations
			WHERE database = 'demo' AND table = 'events' AND mutation_id = ?`, mutationID).Scan(&isDone); err != nil {
			return out, fmt.Errorf("ch poll mutation: %w", err)
		}
	}
	out.completionDuration = time.Since(submitStart)
	return out, nil
}

// pgMutateUpdate/pgMutateDelete — для сравнения: обычный точечный UPDATE/DELETE
// по PK — синхронный, видим полную стоимость сразу в времени выполнения
// (никакого "submit vs completion" разрыва — в этом и разница с CH).
func pgMutateUpdate(ctx context.Context, pool *pgxpool.Pool, id uint64) (time.Duration, error) {
	start := time.Now()
	_, err := pool.Exec(ctx, "UPDATE events SET revenue = 0 WHERE id = $1", id)
	return time.Since(start), err
}

func pgMutateDelete(ctx context.Context, pool *pgxpool.Pool, id uint64) (time.Duration, error) {
	start := time.Now()
	_, err := pool.Exec(ctx, "DELETE FROM events WHERE id = $1", id)
	return time.Since(start), err
}
