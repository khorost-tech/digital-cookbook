package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func formatParts(parts []partInfo) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%s@%s(rows=%d,bytes=%s)", p.partition, p.diskName, p.rows, humanBytes(p.bytes))
	}
	if s == "" {
		return "(нет активных частей)"
	}
	return s
}

// timeQuery — время SELECT count() по одной партиции (WHERE
// toDate(event_time) = partitionKey). Используется для сравнения
// локального чтения (свежая партиция, диск default) и S3-чтения (старая
// партиция, диск s3, ПОСЛЕ MOVE PARTITION).
func timeQuery(ctx context.Context, ch clickhouse.Conn, table, partitionKey string) time.Duration {
	start := time.Now()
	var cnt uint64
	if err := ch.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM demo.%s WHERE toDate(event_time) = ?", table), partitionKey).Scan(&cnt); err != nil {
		panic(fmt.Sprintf("timeQuery %s/%s: %v", table, partitionKey, err))
	}
	return time.Since(start)
}

func medianDuration(n int, fn func() time.Duration) time.Duration {
	ds := make([]time.Duration, n)
	for i := 0; i < n; i++ {
		ds[i] = fn()
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[n/2]
}

func countAndCents(ctx context.Context, ch clickhouse.Conn, table, partitionKey string) (uint64, int64) {
	var cnt uint64
	var cents int64
	err := ch.QueryRow(ctx, fmt.Sprintf(
		"SELECT count(), toInt64(sum(revenue) * 100) FROM demo.%s WHERE toDate(event_time) = ?", table),
		partitionKey).Scan(&cnt, &cents)
	if err != nil {
		panic(fmt.Sprintf("countAndCents %s/%s: %v", table, partitionKey, err))
	}
	return cnt, cents
}

func countAndCentsAll(ctx context.Context, ch clickhouse.Conn, table string) (uint64, int64) {
	var cnt uint64
	var cents int64
	err := ch.QueryRow(ctx, fmt.Sprintf("SELECT count(), toInt64(sum(revenue) * 100) FROM demo.%s", table)).Scan(&cnt, &cents)
	if err != nil {
		panic(fmt.Sprintf("countAndCentsAll %s: %v", table, err))
	}
	return cnt, cents
}

// countAndCentsS3 — тот же агрегат, но источник — табличная функция s3()
// напрямую поверх Parquet-объекта в MinIO (Step 3 брифа round-trip), не
// таблица demo.*.
func countAndCentsS3(ctx context.Context, ch clickhouse.Conn, url, accessKey, secretKey string) (uint64, int64) {
	var cnt uint64
	var cents int64
	q := fmt.Sprintf("SELECT count(), toInt64(sum(revenue) * 100) FROM s3('%s', '%s', '%s', 'Parquet')", url, accessKey, secretKey)
	if err := ch.QueryRow(ctx, q).Scan(&cnt, &cents); err != nil {
		panic(fmt.Sprintf("countAndCentsS3 %s: %v", url, err))
	}
	return cnt, cents
}
