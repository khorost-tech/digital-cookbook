package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

const markerWhere = "country = 'ZZ' AND event_type = 'replication_probe'"

// phaseReplication — Step 3 брифа: вставка НАПРЯМУЮ в одну реплику (минуя
// Distributed — insertRows целится в конкретное соединение ch-s1-r1) с
// последующим наблюдением появления той же строки на ВТОРОЙ реплике того же
// шарда (ch-s1-r2) через асинхронную репликацию ReplicatedMergeTree/Keeper.
// Затем — дедупликация: та же самая (байт-в-байт идентичная) пачка строк
// отправляется ВТОРЫМ отдельным INSERT в ту же ch-s1-r1 — ReplicatedMergeTree
// сравнивает хеш вставляемого блока с недавней историей (Keeper-координация,
// replicated_deduplication_window) и отбрасывает дубликат целиком: count()
// НЕ увеличивается второй раз.
func phaseReplication(ctx context.Context, nodes map[string]clickhouse.Conn) uint64 {
	fmt.Println("\n=== РЕПЛИКАЦИЯ + ДЕДУПЛИКАЦИЯ ВСТАВОК (Step 3 брифа) ===")

	const n = 3000
	baseTime := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) // фиксировано (не time.Now()) — детерминированный, воспроизводимый блок
	marker := syntheticMarkerRows(n, baseTime)

	beforeS1R1, err := countLocalWhere(ctx, nodes["s1-r1"], tblEvents, markerWhere)
	if err != nil {
		log.Fatalf("count marker before insert (ch-s1-r1): %v", err)
	}

	if err := insertRows(ctx, nodes["s1-r1"], tblEvents, marker); err != nil {
		log.Fatalf("insert marker rows into ch-s1-r1: %v", err)
	}
	afterS1R1, err := countLocalWhere(ctx, nodes["s1-r1"], tblEvents, markerWhere)
	if err != nil {
		log.Fatalf("count marker after insert (ch-s1-r1): %v", err)
	}
	fmt.Printf("[replication] вставлено %d marker-строк напрямую в ch-s1-r1 (МИМО Distributed): count(marker) %d -> %d\n", n, beforeS1R1, afterS1R1)
	assertFailFast(afterS1R1 == beforeS1R1+uint64(n), "ch-s1-r1: count(marker) вырос ровно на %d: %d == %d+%d", n, afterS1R1, beforeS1R1, n)

	got, waited, polls := pollLocalMarker(ctx, nodes["s1-r2"], afterS1R1, 30*time.Second)
	fmt.Printf("[replication] ch-s1-r2 (вторая реплика шарда 1, ВСТАВКА НЕ ПОСЫЛАЛАСЬ ей напрямую) увидела marker-строки через Keeper-репликацию: %d/%d rows за %s (%d опросов)\n", got, afterS1R1, waited, polls)
	assertFailFast(got == afterS1R1, "ch-s1-r2 count(marker) == ch-s1-r1 count(marker) после ожидания репликации: %d == %d", got, afterS1R1)

	// Дедупликация: та же самая пачка (byte-identical marker) — ВТОРОЙ раз.
	if err := insertRows(ctx, nodes["s1-r1"], tblEvents, marker); err != nil {
		log.Fatalf("insert marker rows into ch-s1-r1 (2nd, dedup check): %v", err)
	}
	afterDedup, err := countLocalWhere(ctx, nodes["s1-r1"], tblEvents, markerWhere)
	if err != nil {
		log.Fatalf("count marker after 2nd insert (ch-s1-r1): %v", err)
	}
	fmt.Printf("[dedup] тот же самый блок (%d байт-в-байт идентичных строк) отправлен в ch-s1-r1 ВТОРОЙ раз: count(marker) %d -> %d\n", n, afterS1R1, afterDedup)
	assertFailFast(afterDedup == afterS1R1, "ReplicatedMergeTree дедупликация: повторная вставка идентичного блока НЕ увеличила count(marker): %d == %d (не %d)", afterDedup, afterS1R1, afterS1R1+uint64(n))

	// Раз на первой реплике ничего не изменилось (дубликат отброшен ДО
	// записи part'а), реплицировать на s1-r2 нечего — сверяем, что там тоже
	// без изменений.
	gotAfterDedup, err := countLocalWhere(ctx, nodes["s1-r2"], tblEvents, markerWhere)
	if err != nil {
		log.Fatalf("count marker after dedup (ch-s1-r2): %v", err)
	}
	fmt.Printf("[dedup] ch-s1-r2 после дубля: count(marker)=%d (без изменений, дубликат отброшен на приёмной ноде до появления в логе репликации)\n", gotAfterDedup)
	assertFailFast(gotAfterDedup == afterS1R1, "ch-s1-r2 count(marker) не изменился после дедупа на ch-s1-r1: %d == %d", gotAfterDedup, afterS1R1)

	return afterDedup
}

func pollLocalMarker(ctx context.Context, conn clickhouse.Conn, want uint64, timeout time.Duration) (got uint64, waited time.Duration, polls int) {
	deadline := time.Now().Add(timeout)
	start := time.Now()
	const interval = 200 * time.Millisecond
	for {
		n, err := countLocalWhere(ctx, conn, tblEvents, markerWhere)
		if err != nil {
			log.Fatalf("poll marker count: %v", err)
		}
		got = n
		polls++
		if n >= want {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(interval)
	}
	return got, time.Since(start), polls
}
