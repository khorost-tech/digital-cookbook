package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// shardCounts — распределение строк по шардам, полученное через
// shardNum() поверх Distributed-таблицы (Step 2 брифа).
type shardCounts struct {
	shard1 uint64
	shard2 uint64
}

func (s shardCounts) total() uint64 { return s.shard1 + s.shard2 }

func queryShardCounts(ctx context.Context, coord clickhouse.Conn) shardCounts {
	rows, err := coord.Query(ctx, fmt.Sprintf("SELECT shardNum() AS shard, count() AS c FROM demo.%s GROUP BY shard ORDER BY shard", tblDistributed))
	if err != nil {
		log.Fatalf("query shardNum() counts: %v", err)
	}
	defer rows.Close()
	var sc shardCounts
	for rows.Next() {
		var shard uint32
		var c uint64
		if err := rows.Scan(&shard, &c); err != nil {
			log.Fatalf("scan shardNum() row: %v", err)
		}
		switch shard {
		case 1:
			sc.shard1 = c
		case 2:
			sc.shard2 = c
		default:
			log.Fatalf("неожиданный shardNum()=%d (ожидались только 1 и 2)", shard)
		}
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("iterate shardNum() rows: %v", err)
	}
	return sc
}

// pollLocalCount — опрашивает count() локальной (не Distributed) таблицы на
// конкретной ноде ДО достижения want, с ТАЙМАУТОМ (не бесконечный цикл) —
// используется, чтобы дождаться, пока асинхронная репликация ReplicatedMergeTree
// (через Keeper) догонит "первичную" реплику, принявшую синхронную вставку.
func pollLocalCount(ctx context.Context, conn clickhouse.Conn, table string, want uint64, timeout time.Duration) (got uint64, waited time.Duration, polls int) {
	deadline := time.Now().Add(timeout)
	start := time.Now()
	const interval = 200 * time.Millisecond
	for {
		n, err := countLocal(ctx, conn, table)
		if err != nil {
			log.Fatalf("poll count %s: %v", table, err)
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

// phaseInsertDist — Step 2 брифа: вставка через Distributed-таблицу
// (insert_distributed_sync=1). Живой факт, обнаруженный на этом прогоне
// (НЕ то, что можно было бы просто предположить из документации): даже с
// insert_distributed_sync=1 Distributed-таблица выбирает КАКУЮ-ТО одну живую
// реплику каждого шарда (load_balancing) на КАЖДЫЙ отправляемый батч
// НЕЗАВИСИМО — за несколько батчей одного INSERT это может быть то одна, то
// другая реплика шарда, а НЕ всегда "первая" (s1-r1/s2-r1). Синхронность
// гарантирует, что батч ACK'нут ОДНОЙ репликой, а не обеими сразу — второй
// реплике той же пары ещё нужно время догнать через Keeper-репликацию
// (обычно доли секунды). Поэтому сразу после вставки читаем шардовые счётчики
// с ОПРОСОМ ДО СХОДИМОСТИ обеих реплик каждого шарда (bounded timeout), а не
// доверяем первому же ответу — ровно то же самое явление, которое Step 3
// демонстрирует отдельно и умышленно (insertRows напрямую в ОДНУ реплику).
func phaseInsertDist(ctx context.Context, coord clickhouse.Conn, nodes map[string]clickhouse.Conn, csvPath string, batchSize int, expectRows int64) shardCounts {
	fmt.Println("\n=== ВСТАВКА ЧЕРЕЗ DISTRIBUTED + РАСПРЕДЕЛЕНИЕ ПО ШАРДАМ (Step 2 брифа) ===")

	syncCtx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"insert_distributed_sync": 1}))
	rows, dur, err := batchLoad(syncCtx, coord, tblDistributed, csvPath, batchSize, 0)
	if err != nil {
		log.Fatalf("insert into %s via Distributed: %v", tblDistributed, err)
	}
	fmt.Printf("[insert-dist] %d rows вставлено в demo.%s (insert_distributed_sync=1) за %s (%.0f rows/s)\n", rows, tblDistributed, dur, float64(rows)/dur.Seconds())
	if expectRows > 0 {
		assertFailFast(int64(rows) == expectRows, "вставлено rows (%d) == expected (-expect-rows=%d)", rows, expectRows)
	}

	scRaw := queryShardCounts(ctx, coord)
	fmt.Printf("[shard-dist] СРАЗУ после вставки, SELECT shardNum(), count() FROM demo.%s: shard1=%d shard2=%d total=%d (может НЕ совпадать с %d — см. ниже, почему)\n", tblDistributed, scRaw.shard1, scRaw.shard2, scRaw.total(), rows)

	shard1, s1Wait, s1Polls := pollShardConverged(ctx, nodes["s1-r1"], nodes["s1-r2"], tblEvents, 30*time.Second)
	fmt.Printf("[replication] шард 1 (ch-s1-r1/ch-s1-r2) сошёлся через Keeper-репликацию: count()=%d за %s (%d опросов)\n", shard1, s1Wait, s1Polls)
	shard2, s2Wait, s2Polls := pollShardConverged(ctx, nodes["s2-r1"], nodes["s2-r2"], tblEvents, 30*time.Second)
	fmt.Printf("[replication] шард 2 (ch-s2-r1/ch-s2-r2) сошёлся через Keeper-репликацию: count()=%d за %s (%d опросов)\n", shard2, s2Wait, s2Polls)

	sc := shardCounts{shard1: shard1, shard2: shard2}
	fmt.Printf("[shard-dist] ПОСЛЕ сходимости обеих реплик каждого шарда: shard1=%d shard2=%d total=%d\n", sc.shard1, sc.shard2, sc.total())
	assertFailFast(sc.total() == rows, "сумма по шардам ПОСЛЕ сходимости репликации (%d) == всего вставленных строк (%d)", sc.total(), rows)
	assertFailFast(sc.shard1 > 0 && sc.shard2 > 0, "оба шарда получили строки (cityHash64(user_id) реально распределяет, а не кладёт всё в один шард): shard1=%d, shard2=%d", sc.shard1, sc.shard2)

	scFinal := queryShardCounts(ctx, coord)
	fmt.Printf("[shard-dist] контрольный fan-out read через Distributed ПОСЛЕ сходимости: shard1=%d shard2=%d total=%d\n", scFinal.shard1, scFinal.shard2, scFinal.total())
	assertFailFast(scFinal.total() == rows, "fan-out read через Distributed теперь стабильно совпадает с ожидаемым total: %d == %d", scFinal.total(), rows)

	return sc
}

// pollShardConverged — опрашивает ОБЕ реплики шарда до тех пор, пока их
// count() не совпадут (сошлись через репликацию) — авторитетный способ
// узнать "сколько строк реально в этом шарде", не зависящий от того, какую
// из двух реплик выбрал бы load-balancing для read-запроса. Таймаут ограничен
// (не бесконечный цикл).
func pollShardConverged(ctx context.Context, connA, connB clickhouse.Conn, table string, timeout time.Duration) (count uint64, waited time.Duration, polls int) {
	deadline := time.Now().Add(timeout)
	start := time.Now()
	const interval = 200 * time.Millisecond
	for {
		a, err := countLocal(ctx, connA, table)
		if err != nil {
			log.Fatalf("poll shard converged (A) %s: %v", table, err)
		}
		b, err := countLocal(ctx, connB, table)
		if err != nil {
			log.Fatalf("poll shard converged (B) %s: %v", table, err)
		}
		polls++
		if a == b {
			return a, time.Since(start), polls
		}
		if time.Now().After(deadline) {
			log.Fatalf("shard did not converge within %s: A=%d B=%d (последний опрос %d)", timeout, a, b, polls)
		}
		time.Sleep(interval)
	}
}
