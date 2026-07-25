// Command s3 — стенд #7 серии "ClickHouse: глубокое погружение": MinIO +
// storage_configuration (s3-диск + storage policy hot_cold) — S3-тиринг
// (MergeTree с TTL ... TO VOLUME 'cold', форсированный ALTER TABLE ...
// MOVE PARTITION ... TO DISK 's3' для детерминизма в рамках одного хода)
// + табличная функция s3() (Parquet round-trip + ingestion S3 ->
// MergeTree).
//
// Честный контраст серии: Kafka tiered storage (../../kafka/ серия,
// стенд storage) НЕ был воспроизведён живьём (см. ../../kafka/README.md,
// секция "Проверено живьём (storage, ...)" — tiered storage зафиксирован
// как честно НЕ отработавший на использованной версии образа). Здесь,
// наоборот, CH S3-тиринг воспроизводится живьём целиком: part физически
// переезжает на MinIO (system.parts.disk_name='s3'), запрос к нему
// по-прежнему корректен — см. README "Проверено живьём (s3, ...)".
//
// Запуск (из контейнера golang:1.25, сеть clickhouse-cookbook-net, см.
// ../ops/s3-demo.sh за полный live-сценарий):
//
//	docker run --rm --network clickhouse-cookbook-net \
//	  -v "$(pwd)/s3:/app" -w /app golang:1.25 \
//	  go run . -phase=all -ch-addr=clickhouse:9000 \
//	  -s3-endpoint=http://minio:9000/chdata/ -s3-access-key=minioadmin -s3-secret-key=minioadmin123
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func main() {
	phase := flag.String("phase", "all", "policy|tiering|parquet|all")
	chAddr := flag.String("ch-addr", "clickhouse:9000", "ClickHouse native addr")
	oldRows := flag.Int("old-rows", 300_000, "число 'старых' строк (уедут на s3 при MOVE PARTITION)")
	freshRows := flag.Int("fresh-rows", 300_000, "число 'свежих' строк (остаются на default-диске)")
	batchSize := flag.Int("batch-size", 50_000, "размер батча вставки (PrepareBatch)")
	s3Endpoint := flag.String("s3-endpoint", "http://minio:9000/chdata/", "базовый URL бакета MinIO (с завершающим /), должен совпадать с бакетом в ../config/storage.xml")
	s3AccessKey := flag.String("s3-access-key", "minioadmin", "MinIO access key (dev-ключ, см. ../compose/minio.yml)")
	s3SecretKey := flag.String("s3-secret-key", "minioadmin123", "MinIO secret key (dev-ключ, см. ../compose/minio.yml)")

	flag.Parse()

	ctx := context.Background()
	ch, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{*chAddr},
		Auth:        clickhouse.Auth{Database: "demo", Username: "default"},
		DialTimeout: 10 * time.Second,
		Settings:    clickhouse.Settings{"max_execution_time": 180},
	})
	if err != nil {
		log.Fatalf("ch open: %v", err)
	}
	defer ch.Close()
	if err := ch.Ping(ctx); err != nil {
		log.Fatalf("ch ping: %v", err)
	}

	switch *phase {
	case "policy":
		phasePolicy(ctx, ch)
	case "tiering":
		phaseTiering(ctx, ch, *oldRows, *freshRows, *batchSize)
	case "parquet":
		phaseParquet(ctx, ch, *s3Endpoint, *s3AccessKey, *s3SecretKey)
	case "all":
		phasePolicy(ctx, ch)
		phaseTiering(ctx, ch, *oldRows, *freshRows, *batchSize)
		phaseParquet(ctx, ch, *s3Endpoint, *s3AccessKey, *s3SecretKey)
		fmt.Println("\n[s3] all phases completed, all asserts passed")
	default:
		fmt.Fprintf(os.Stderr, "unknown -phase=%s\n", *phase)
		os.Exit(2)
	}
}
