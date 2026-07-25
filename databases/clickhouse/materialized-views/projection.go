package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

const projectionName = "user_id_proj"

type queryStat struct {
	duration time.Duration
	count    uint64
	readRows uint64
}

// phaseProjection — Step 2б брифа: проекция внутри demo.mv_events под
// альтернативный ORDER BY (user_id) — тот же приём, что в мире when-olap
// выявил слабость CH на точечных выборках по колонке ВНЕ основного ORDER BY
// (id читал 12.5% таблицы). Проекция хранит второй физический порядок
// данных ВНУТРИ той же таблицы — читатель может фильтровать по user_id
// без разреженного скана по event_time.
func phaseProjection(ctx context.Context, ch clickhouse.Conn) {
	fmt.Println("\n=== PROJECTIONS: альтернативный ORDER BY внутри таблицы (Step 2б брифа) ===")

	testUserID, err := sampleUserID(ctx, ch)
	if err != nil {
		log.Fatalf("sample user_id: %v", err)
	}
	fmt.Printf("[projection] тестовый user_id = %d (выбран из demo.%s)\n", testUserID, tblEvents)

	filterSQL := fmt.Sprintf("SELECT count() FROM demo.%s WHERE user_id = %d", tblEvents, testUserID)

	noProjStat, err := readRowsWithSettings(ctx, ch, filterSQL, "mv-projection-before", clickhouse.Settings{"optimize_use_projections": 0})
	if err != nil {
		log.Fatalf("query before projection: %v", err)
	}
	fmt.Printf("[projection] ДО создания проекции (optimize_use_projections=0, форсированный полный скан): read_rows=%d, count()=%d, %s\n",
		noProjStat.readRows, noProjStat.count, noProjStat.duration)

	if err := ch.Exec(ctx, fmt.Sprintf("ALTER TABLE demo.%s ADD PROJECTION %s (SELECT * ORDER BY user_id)", tblEvents, projectionName)); err != nil {
		log.Fatalf("add projection: %v", err)
	}
	if err := ch.Exec(ctx, fmt.Sprintf("ALTER TABLE demo.%s MATERIALIZE PROJECTION %s", tblEvents, projectionName)); err != nil {
		log.Fatalf("materialize projection: %v", err)
	}
	if err := waitMutationsDone(ctx, ch, tblEvents, 60*time.Second); err != nil {
		log.Fatalf("wait projection materialize: %v", err)
	}
	fmt.Printf("[projection] проекция %s (ORDER BY user_id) добавлена и материализована на demo.%s\n", projectionName, tblEvents)

	explainText, err := explainProjections(ctx, ch, filterSQL)
	if err != nil {
		log.Fatalf("explain projections: %v", err)
	}
	fmt.Println("[projection] EXPLAIN indexes=1 (WHERE user_id = ...):")
	fmt.Println(explainText)
	usedInPlan := strings.Contains(explainText, projectionName)
	assertFailFast(usedInPlan, "EXPLAIN indexes=1 упоминает проекцию %q в плане", projectionName)

	withProjStat, err := readRowsWithSettings(ctx, ch, filterSQL, "mv-projection-after", clickhouse.Settings{"optimize_use_projections": 1})
	if err != nil {
		log.Fatalf("query with projection: %v", err)
	}
	fmt.Printf("[projection] ПОСЛЕ материализации (optimize_use_projections=1, дефолт): read_rows=%d, count()=%d, %s\n",
		withProjStat.readRows, withProjStat.count, withProjStat.duration)

	// force_optimize_projection=1 — жёсткое доказательство: если ClickHouse
	// не смог бы использовать НИ ОДНУ проекцию для этого запроса, сервер
	// вернул бы ошибку ("no projection available"). Успешное выполнение —
	// прямое подтверждение использования проекции, надёжнее, чем парсинг
	// текста EXPLAIN.
	forcedStat, err := readRowsWithSettings(ctx, ch, filterSQL, "mv-projection-forced", clickhouse.Settings{"force_optimize_projection": 1})
	if err != nil {
		log.Fatalf("force_optimize_projection=1 query FAILED — проекция НЕ используется для этого запроса: %v", err)
	}
	fmt.Printf("[projection] force_optimize_projection=1 выполнился БЕЗ ошибки — сервер обязан был использовать проекцию (иначе отказ): read_rows=%d, count()=%d\n",
		forcedStat.readRows, forcedStat.count)

	assertFailFast(noProjStat.count == withProjStat.count && withProjStat.count == forcedStat.count,
		"результат count() одинаков независимо от использования проекции: без=%d, с=%d, forced=%d", noProjStat.count, withProjStat.count, forcedStat.count)
	assertFailFast(withProjStat.readRows < noProjStat.readRows,
		"проекция сокращает read_rows: с проекцией=%d < без проекции=%d", withProjStat.readRows, noProjStat.readRows)
}

func sampleUserID(ctx context.Context, ch clickhouse.Conn) (uint64, error) {
	var id uint64
	err := ch.QueryRow(ctx, fmt.Sprintf("SELECT user_id FROM demo.%s LIMIT 1", tblEvents)).Scan(&id)
	return id, err
}

func explainProjections(ctx context.Context, ch clickhouse.Conn, sql string) (string, error) {
	rows, err := ch.Query(ctx, "EXPLAIN indexes = 1 "+sql)
	if err != nil {
		return "", err
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

func readRowsWithSettings(ctx context.Context, ch clickhouse.Conn, sql, queryIDPrefix string, settings clickhouse.Settings) (queryStat, error) {
	queryID := fmt.Sprintf("%s-%d", queryIDPrefix, time.Now().UnixNano())
	qctx := clickhouse.Context(ctx, clickhouse.WithQueryID(queryID), clickhouse.WithSettings(settings))

	start := time.Now()
	var cnt uint64
	if err := ch.QueryRow(qctx, sql).Scan(&cnt); err != nil {
		return queryStat{}, fmt.Errorf("run query: %w", err)
	}
	dur := time.Since(start)

	if err := ch.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		return queryStat{}, fmt.Errorf("flush logs: %w", err)
	}
	var readRows uint64
	err := ch.QueryRow(ctx, `
		SELECT read_rows FROM system.query_log
		WHERE query_id = ? AND type = 'QueryFinish'
		ORDER BY event_time DESC LIMIT 1`, queryID).Scan(&readRows)
	if err != nil {
		return queryStat{duration: dur, count: cnt}, fmt.Errorf("read read_rows from query_log: %w", err)
	}
	return queryStat{duration: dur, count: cnt, readRows: readRows}, nil
}

// waitMutationsDone — опрос system.mutations до is_done=1 для всех мутаций
// таблицы (MATERIALIZE PROJECTION идёт как мутация), с таймаутом.
func waitMutationsDone(ctx context.Context, ch clickhouse.Conn, table string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var notDone uint64
		err := ch.QueryRow(ctx, "SELECT count() FROM system.mutations WHERE database = 'demo' AND table = ? AND NOT is_done", table).Scan(&notDone)
		if err != nil {
			return err
		}
		if notDone == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout %s waiting for mutations on demo.%s to finish (%d still not done)", timeout, table, notDone)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
