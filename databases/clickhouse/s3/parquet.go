package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// phaseParquet — Step 3 брифа: S3 table function/engine. Записывает
// содержимое demo.s3_events (обе партиции — одна физически на s3-диске
// через storage_policy, другая на default; НЕважно для табличной функции
// s3() — она читает/пишет ЛОГИЧЕСКИЕ строки таблицы через обычный SELECT,
// а не байты конкретного диска) в Parquet-файл в MinIO через INSERT INTO
// FUNCTION s3(...), затем читает его ОБРАТНО через SELECT ... FROM
// s3(...) — round-trip. Отдельно — ingestion: INSERT INTO <MergeTree>
// SELECT * FROM s3(...) в свежую таблицу (демонстрация "S3 -> MergeTree",
// не про тиринг).
func phaseParquet(ctx context.Context, ch clickhouse.Conn, endpoint, accessKey, secretKey string) {
	fmt.Println("\n=== S3 table function: Parquet round-trip (Step 3 брифа) ===")

	sourceCount, sourceCents := countAndCentsAll(ctx, ch, tblEvents)
	fmt.Printf("[parquet] источник demo.%s: count()=%d, sum(revenue)*100=%d центов\n", tblEvents, sourceCount, sourceCents)

	objectURL := endpoint + "s3-demo/events.parquet"

	writeSQL := fmt.Sprintf(
		"INSERT INTO FUNCTION s3('%s', '%s', '%s', 'Parquet') SELECT event_time, user_id, event_type, url, duration_ms, country, revenue FROM demo.%s",
		objectURL, accessKey, secretKey, tblEvents)
	writeStart := time.Now()
	if err := ch.Exec(ctx, writeSQL); err != nil {
		log.Fatalf("INSERT INTO FUNCTION s3: %v", err)
	}
	fmt.Printf("[parquet] записано в MinIO: %s за %s (INSERT INTO FUNCTION s3(...) SELECT ... FORMAT Parquet)\n", objectURL, time.Since(writeStart))

	roundtripCount, roundtripCents := countAndCentsS3(ctx, ch, objectURL, accessKey, secretKey)
	fmt.Printf("[parquet] прочитано обратно из MinIO: count()=%d, sum(revenue)*100=%d центов\n", roundtripCount, roundtripCents)

	assertFailFast(roundtripCount == sourceCount, "Parquet round-trip через S3: count() совпал с источником (факт %d == %d)", roundtripCount, sourceCount)
	assertFailFast(roundtripCents == sourceCents, "Parquet round-trip через S3: sum(revenue) совпал с источником побайтово (факт %d == %d центов)", roundtripCents, sourceCents)

	// Ingestion: S3 (Parquet) -> свежая MergeTree-таблица.
	if err := createFromParquetTable(ctx, ch, tblFromParquet); err != nil {
		log.Fatalf("create %s: %v", tblFromParquet, err)
	}
	ingestSQL := fmt.Sprintf(
		"INSERT INTO demo.%s SELECT * FROM s3('%s', '%s', '%s', 'Parquet')",
		tblFromParquet, objectURL, accessKey, secretKey)
	ingestStart := time.Now()
	if err := ch.Exec(ctx, ingestSQL); err != nil {
		log.Fatalf("INSERT INTO ... SELECT FROM s3: %v", err)
	}
	ingestCount, err := countRows(ctx, ch, tblFromParquet)
	if err != nil {
		log.Fatalf("count %s: %v", tblFromParquet, err)
	}
	fmt.Printf("[parquet] ingestion S3 -> MergeTree (demo.%s): count()=%d за %s\n", tblFromParquet, ingestCount, time.Since(ingestStart))
	assertFailFast(ingestCount == sourceCount, "ingestion S3(Parquet) -> MergeTree: count() совпал с источником (факт %d == %d)", ingestCount, sourceCount)

	fmt.Println("[parquet] content-note (граница Step 3 брифа): s3()/ENGINE=S3 — запрос к ОДНОМУ источнику (файл/префикс в одном бакете), не федеративный движок. Для запроса ПО МНОГИМ разнородным источникам сразу в одном SQL (S3 + Postgres + Kafka одновременно) — Trino/lakehouse-инструменты, не ClickHouse.")
}
