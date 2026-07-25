package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

const opsTable = "ops_events"

// phaseMerges — Step 1 брифа: вставить много мелких батчей -> много parts,
// наблюдать фоновые merges, форсировать OPTIMIZE TABLE ... FINAL, реальные
// числа parts до/после.
//
// SYSTEM STOP MERGES вокруг вставки — иначе фоновый merge scheduler может
// схлопнуть parts за миллисекунды на простаивающем single-node dev-инстансе
// (урок стенда #4 — materialized-views/README «Проверено живьём»: SummingMergeTree
// и TTL там уже схлопнулись ДО явного OPTIMIZE FINAL) и "много parts" не
// будет видно вообще. Это свойство MergeTree на idle-хосте, а не искусственная
// пауза ради теста.
func phaseMerges(ctx context.Context, ch clickhouse.Conn, csvPath string, smallBatches, smallBatchSize int) {
	fmt.Println("\n=== PARTS И MERGES (Step 1 брифа) ===")

	mustRun(func() error { return createTable(ctx, ch, opsTable, "", 8192) })

	if err := ch.Exec(ctx, fmt.Sprintf("SYSTEM STOP MERGES demo.%s", opsTable)); err != nil {
		log.Fatalf("stop merges: %v", err)
	}

	rows, loadDur, err := loadManySmallBatches(ctx, ch, opsTable, csvPath, smallBatches, smallBatchSize)
	if err != nil {
		log.Fatalf("small batches load: %v", err)
	}

	partsBefore, err := activeParts(ctx, ch, opsTable)
	if err != nil {
		log.Fatalf("active parts (before start merges): %v", err)
	}
	fmt.Printf("[merges] %d батчей по %d строк (SYSTEM STOP MERGES во время вставки): %d rows in %s, %d active parts\n",
		smallBatches, smallBatchSize, rows, loadDur, partsBefore)

	if err := ch.Exec(ctx, fmt.Sprintf("SYSTEM START MERGES demo.%s", opsTable)); err != nil {
		log.Fatalf("start merges: %v", err)
	}

	fmt.Println("[merges] SYSTEM START MERGES; наблюдение фона 10s (system.parts + system.merges, честно — может схлопнуться раньше, чем мы опросим)")
	observeStart := time.Now()
	lastParts := partsBefore
	for time.Since(observeStart) < 10*time.Second {
		time.Sleep(1 * time.Second)
		p, err := activeParts(ctx, ch, opsTable)
		if err != nil {
			log.Fatalf("active parts (observe loop): %v", err)
		}
		merging, err := mergesInProgress(ctx, ch, opsTable)
		if err != nil {
			log.Fatalf("merges in progress: %v", err)
		}
		if p != lastParts || merging > 0 {
			fmt.Printf("[merges]   t+%s: active_parts=%d (было %d), system.merges в процессе=%d\n",
				time.Since(observeStart).Round(time.Second), p, lastParts, merging)
			lastParts = p
		}
	}
	partsAfterBackground, err := activeParts(ctx, ch, opsTable)
	if err != nil {
		log.Fatalf("active parts (after background window): %v", err)
	}
	fmt.Printf("[merges] после 10s фонового наблюдения (без явного OPTIMIZE): %d active parts (было %d до START MERGES)\n",
		partsAfterBackground, partsBefore)

	if err := ch.Exec(ctx, fmt.Sprintf("OPTIMIZE TABLE demo.%s FINAL", opsTable)); err != nil {
		log.Fatalf("optimize final: %v", err)
	}
	partsAfterForced, err := activeParts(ctx, ch, opsTable)
	if err != nil {
		log.Fatalf("active parts (after optimize final): %v", err)
	}
	fmt.Printf("[merges] после OPTIMIZE TABLE ... FINAL (форсированное схлопывание): %d active parts\n", partsAfterForced)

	assertFailFast(int64(rows) == int64(smallBatches*smallBatchSize),
		"загружено rows (%d) == smallBatches*smallBatchSize (%d)", rows, smallBatches*smallBatchSize)
	assertFailFast(partsBefore >= uint64(smallBatches)/2,
		"%d мелких батчей под SYSTEM STOP MERGES создают много parts: active_parts (%d) >= smallBatches/2 (%d)",
		smallBatches, partsBefore, smallBatches/2)
	assertFailFast(partsAfterForced < partsBefore,
		"OPTIMIZE TABLE ... FINAL схлопнул parts: после (%d) < до (%d)", partsAfterForced, partsBefore)
}
