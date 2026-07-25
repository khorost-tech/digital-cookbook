// Command ops-stand — стенд #6 серии "ClickHouse: глубокое погружение":
// эксплуатация и тюнинг single-node ClickHouse. Parts/merges (Step 1),
// mutations UPDATE/DELETE (Step 2), мониторинг system.* + BACKUP/RESTORE
// round-trip (Step 3), codec-тюнинг ZSTD vs Delta+ZSTD + бонус
// index_granularity/max_threads/лимит памяти (Step 4).
//
// Запуск (из контейнера golang:1.25, сеть clickhouse-cookbook-net, см.
// ../ops/ops-demo.sh за полный live-сценарий):
//
//	docker run --rm --network clickhouse-cookbook-net \
//	  -v "$(pwd)/ops-stand:/app" -v "$(pwd)/dataset/out:/data" -w /app golang:1.25 \
//	  go run . -phase=all -csv=/data/events-ops.csv -ch-addr=clickhouse:9000
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
	phase := flag.String("phase", "all", "merges|mutations|monitoring|codec|all")
	chAddr := flag.String("ch-addr", "clickhouse:9000", "ClickHouse native addr")
	csvPath := flag.String("csv", "/data/events-ops.csv", "путь к CSV-датасету (dataset/main.go -rows=750000)")
	smallBatches := flag.Int("small-batches", 200, "число мелких батчей для демонстрации 'много parts' (Step 1)")
	smallBatchSize := flag.Int("small-batch-size", 1000, "размер каждого мелкого батча, строк (Step 1)")
	mutationTimeout := flag.Duration("mutation-timeout", 90*time.Second, "таймаут опроса system.mutations.is_done (Step 2)")
	backupPath := flag.String("backup-path", "", "путь бэкапа внутри контейнера CH (см. config/backups.xml allowed_path); пусто — сгенерировать с текущим unix-временем")
	backupTimeout := flag.Duration("backup-timeout", 60*time.Second, "таймаут BACKUP/RESTORE (Step 3)")
	codecSkip := flag.Int("codec-skip", 200_000, "число строк CSV, пропускаемых перед codec-выборкой (не пересекается с ops_events)")
	codecLimit := flag.Int64("codec-limit", 500_000, "число строк для codec-сравнения (Step 4)")
	flag.Parse()

	resolvedBackupPath := *backupPath
	if resolvedBackupPath == "" {
		resolvedBackupPath = fmt.Sprintf("/var/lib/clickhouse/backups/ops_events_backup_%d.zip", time.Now().Unix())
	}

	ctx := context.Background()
	ch, err := clickhouse.Open(&clickhouse.Options{
		Addr:         []string{*chAddr},
		Auth:         clickhouse.Auth{Database: "demo", Username: "default"},
		DialTimeout:  30 * time.Second,
		MaxOpenConns: 8,
		MaxIdleConns: 4,
		Settings:     clickhouse.Settings{"max_execution_time": 180},
	})
	if err != nil {
		log.Fatalf("ch open: %v", err)
	}
	defer ch.Close()
	if err := ch.Ping(ctx); err != nil {
		log.Fatalf("ch ping: %v", err)
	}

	switch *phase {
	case "merges":
		phaseMerges(ctx, ch, *csvPath, *smallBatches, *smallBatchSize)
	case "mutations":
		phaseMutations(ctx, ch, opsTable, *mutationTimeout)
	case "monitoring":
		phaseMonitoring(ctx, ch, opsTable, resolvedBackupPath, *backupTimeout)
	case "codec":
		phaseCodec(ctx, ch, *csvPath, *codecSkip, *codecLimit)
	case "all":
		phaseMerges(ctx, ch, *csvPath, *smallBatches, *smallBatchSize)
		phaseMutations(ctx, ch, opsTable, *mutationTimeout)
		phaseMonitoring(ctx, ch, opsTable, resolvedBackupPath, *backupTimeout)
		phaseCodec(ctx, ch, *csvPath, *codecSkip, *codecLimit)
		fmt.Println("\n[ops-stand] all phases completed, all asserts passed")
	default:
		fmt.Fprintf(os.Stderr, "unknown -phase=%s\n", *phase)
		os.Exit(2)
	}
}

func mustRun(f func() error) {
	if err := f(); err != nil {
		log.Fatalf("%v", err)
	}
}
