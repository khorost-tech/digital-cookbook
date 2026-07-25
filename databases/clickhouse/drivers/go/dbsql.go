package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/ClickHouse/clickhouse-go/v2" // регистрирует database/sql-драйвер "clickhouse"
)

// dbsqlInsert — clickhouse-go/v2 через стандартный database/sql: sql.DB +
// Exec (Step 2 брифа). Батч в database/sql-режиме драйвера реализуется
// транзакцией: Begin -> Prepare(INSERT ...) -> N x Exec (строки копятся в
// клиентском буфере драйвера) -> Commit (один физический INSERT-блок на
// сервер) — задокументированный паттерн clickhouse-go/v2 (см.
// examples/std/open_db.go), а не N отдельных round-trip'ов.
func dbsqlInsert(ctx context.Context, dsn, table, csvPath string, batchSize int) (rows uint64, elapsed time.Duration, err error) {
	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		return 0, 0, fmt.Errorf("sql.Open: %w", err)
	}
	defer db.Close()

	cr, err := openCSVRowReader(csvPath)
	if err != nil {
		return 0, 0, err
	}
	defer cr.close()

	insertSQL := fmt.Sprintf("INSERT INTO demo.%s (event_time, user_id, event_type, url, duration_ms, country, revenue)", table)

	commitBatch := func() (int, error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return 0, fmt.Errorf("begin: %w", err)
		}
		stmt, err := tx.PrepareContext(ctx, insertSQL)
		if err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("prepare: %w", err)
		}
		n := 0
		for n < batchSize {
			rw, ok := cr.next()
			if !ok {
				break
			}
			if _, err := stmt.ExecContext(ctx, rw.eventTime, rw.userID, rw.eventType, rw.url, rw.durationMs, rw.country, rw.revenue); err != nil {
				_ = stmt.Close()
				_ = tx.Rollback()
				return n, fmt.Errorf("exec row %d: %w", n+1, err)
			}
			n++
		}
		_ = stmt.Close()
		if n == 0 {
			_ = tx.Rollback()
			return 0, nil
		}
		if err := tx.Commit(); err != nil {
			return n, fmt.Errorf("commit: %w", err)
		}
		return n, nil
	}

	start := time.Now()
	for {
		n, err := commitBatch()
		if err != nil {
			return rows, time.Since(start), fmt.Errorf("dbsql batch at row %d: %w", rows, err)
		}
		rows += uint64(n)
		if n < batchSize {
			break
		}
	}
	if cr.err != nil {
		return rows, time.Since(start), cr.err
	}
	return rows, time.Since(start), nil
}

// dbsqlSelect — тот же аналитический SELECT (aggregate.go) через
// database/sql QueryContext/Scan.
func dbsqlSelect(ctx context.Context, dsn, table string) (aggResult, time.Duration, error) {
	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		return aggResult{}, 0, fmt.Errorf("sql.Open: %w", err)
	}
	defer db.Close()

	start := time.Now()
	rows, err := db.QueryContext(ctx, fmt.Sprintf(aggregateSQLTemplate, table))
	if err != nil {
		return aggResult{}, time.Since(start), fmt.Errorf("dbsql select: %w", err)
	}
	defer rows.Close()

	var triples []triple
	for rows.Next() {
		var t triple
		if err := rows.Scan(&t.country, &t.c, &t.cents); err != nil {
			return aggResult{}, time.Since(start), fmt.Errorf("dbsql select scan: %w", err)
		}
		triples = append(triples, t)
	}
	dur := time.Since(start)
	if err := rows.Err(); err != nil {
		return aggResult{}, dur, fmt.Errorf("dbsql select rows: %w", err)
	}
	return computeAgg(triples), dur, nil
}
