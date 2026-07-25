package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// phaseMonitoring — Step 3 брифа: system.query_log (время/read_rows/memory),
// system.parts/system.columns (размер/сжатие по колонкам), system.metrics/
// asynchronous_metrics. Завершается BACKUP/RESTORE round-trip (phaseBackup).
func phaseMonitoring(ctx context.Context, ch clickhouse.Conn, table, backupPath string, backupTimeout time.Duration) {
	fmt.Println("\n=== МОНИТОРИНГ: system.query_log / system.columns / system.metrics (Step 3 брифа) ===")

	queryID := fmt.Sprintf("ops-monitoring-%d", time.Now().UnixNano())
	qctx := clickhouse.Context(ctx, clickhouse.WithQueryID(queryID))
	var cnt uint64
	if err := ch.QueryRow(qctx, fmt.Sprintf("SELECT count() FROM demo.%s WHERE country IN ('RU','US','DE')", table)).Scan(&cnt); err != nil {
		log.Fatalf("monitoring query: %v", err)
	}
	if err := ch.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		log.Fatalf("flush logs: %v", err)
	}
	var durMs uint64
	var readRows uint64
	var memUsage uint64
	if err := ch.QueryRow(ctx, `
		SELECT query_duration_ms, read_rows, memory_usage
		FROM system.query_log
		WHERE query_id = ? AND type = 'QueryFinish'
		ORDER BY event_time DESC LIMIT 1`, queryID).Scan(&durMs, &readRows, &memUsage); err != nil {
		log.Fatalf("read query_log: %v", err)
	}
	fmt.Printf("[monitoring] system.query_log (SELECT count() WHERE country IN (RU,US,DE)): count()=%d, query_duration_ms=%d, read_rows=%d, memory_usage=%s\n",
		cnt, durMs, readRows, humanBytes(int64(memUsage)))

	fmt.Println("[monitoring] размер/сжатие по колонкам (system.parts_columns — см. живую деталь в schema.go columnBytes про system.columns, оказавшийся 0 после RESTORE)")
	for _, col := range []string{"event_time", "url", "revenue", "country", "user_id"} {
		compressed, uncompressed, err := columnBytes(ctx, ch, table, col)
		if err != nil {
			log.Fatalf("column bytes %s: %v", col, err)
		}
		ratio := 0.0
		if compressed > 0 {
			ratio = float64(uncompressed) / float64(compressed)
		}
		fmt.Printf("[monitoring]   %-12s compressed=%-12s uncompressed=%-12s ratio=%.2fx\n",
			col, humanBytes(compressed), humanBytes(uncompressed), ratio)
	}

	fmt.Println("[monitoring] system.metrics (текущие значения):")
	for _, m := range []string{"Query", "TCPConnection", "MemoryTracking"} {
		v, err := metricValue(ctx, ch, m)
		if err != nil {
			log.Fatalf("metric %s: %v", m, err)
		}
		fmt.Printf("[monitoring]   %s = %d\n", m, v)
	}

	fmt.Println("[monitoring] system.asynchronous_metrics (текущие значения):")
	for _, m := range []string{"Uptime", "NumberOfTables", "TotalPartsOfMergeTreeTables"} {
		v, err := asyncMetricValue(ctx, ch, m)
		if err != nil {
			log.Fatalf("async metric %s: %v", m, err)
		}
		fmt.Printf("[monitoring]   %s = %.2f\n", m, v)
	}

	assertFailFast(cnt > 0, "monitoring query вернул >0 строк (%d)", cnt)
	assertFailFast(readRows > 0, "query_log: read_rows > 0 (запись появилась для query_id=%s)", queryID)

	phaseBackup(ctx, ch, table, backupPath, backupTimeout)
}

func metricValue(ctx context.Context, conn clickhouse.Conn, metric string) (int64, error) {
	var v int64
	err := conn.QueryRow(ctx, "SELECT value FROM system.metrics WHERE metric = ?", metric).Scan(&v)
	return v, err
}

func asyncMetricValue(ctx context.Context, conn clickhouse.Conn, metric string) (float64, error) {
	var v float64
	err := conn.QueryRow(ctx, "SELECT value FROM system.asynchronous_metrics WHERE metric = ?", metric).Scan(&v)
	return v, err
}
