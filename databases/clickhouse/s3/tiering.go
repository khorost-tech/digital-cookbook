package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// phaseTiering — Step 2 брифа: S3-тиринг. Таблица demo.s3_events
// (storage_policy=hot_cold, ../config/storage.xml) получает старые (уедут
// на s3) и свежие (останутся на default) строки в РАЗНЫХ партициях
// (PARTITION BY toDate(event_time)) -> явный ALTER TABLE ... MOVE
// PARTITION ... TO DISK 's3' (форсированное перемещение для детерминизма
// — брифа Step 2, тот же выбор, что "OPTIMIZE ... FINAL" в TTL-демо
// стенда #4: НЕ ждём фоновый TTL-scheduler бесконечно) -> system.parts.
// disk_name подтверждает физическое размещение -> запрос по обеим
// партициям всё ещё работает и даёт правильный результат (данные читаются
// с S3 прозрачно).
func phaseTiering(ctx context.Context, ch clickhouse.Conn, oldRows, freshRows, batchSize int) {
	fmt.Println("\n=== S3-тиринг: MergeTree + storage_policy=hot_cold (Step 2 брифа) ===")

	if err := createEventsTable(ctx, ch, tblEvents); err != nil {
		log.Fatalf("create %s: %v", tblEvents, err)
	}

	now := time.Now().UTC()
	oldDay := now.AddDate(0, 0, -45) // старше TTL (30 дней) на 15 дней — за пределами "hot"-окна
	freshDay := now

	oldRes, err := insertSyntheticRows(ctx, ch, tblEvents, oldRows, oldDay, 1, 1001, batchSize)
	if err != nil {
		log.Fatalf("insert old rows: %v", err)
	}
	freshRes, err := insertSyntheticRows(ctx, ch, tblEvents, freshRows, freshDay, 1_000_000, 1002, batchSize)
	if err != nil {
		log.Fatalf("insert fresh rows: %v", err)
	}
	oldPartitionKey := oldDay.Format("2006-01-02")
	freshPartitionKey := freshDay.Format("2006-01-02")
	fmt.Printf("[tiering] вставлено: %d старых строк (партиция %s) за %s (%.0f rows/s), %d свежих строк (партиция %s) за %s (%.0f rows/s)\n",
		oldRes.rows, oldPartitionKey, oldRes.elapsed, float64(oldRes.rows)/oldRes.elapsed.Seconds(),
		freshRes.rows, freshPartitionKey, freshRes.elapsed, float64(freshRes.rows)/freshRes.elapsed.Seconds())

	partsBefore, err := listParts(ctx, ch, tblEvents)
	if err != nil {
		log.Fatalf("list parts before move: %v", err)
	}
	fmt.Printf("[tiering] ДО explicit MOVE PARTITION: %s\n", formatParts(partsBefore))

	// Живая находка (тот же класс, что ../materialized-views/ttl.go): TTL
	// этой таблицы — `event_time + INTERVAL 30 DAY TO VOLUME 'cold'`, а
	// старая партиция УЖЕ на 45 дней старше "сейчас" в момент вставки —
	// условие TTL выполнено СРАЗУ, не через 30 дней ожидания. На
	// простаивающем single-node dev-инстансе фоновый merge scheduler может
	// подобрать это условие и переместить партицию на s3 САМ, ещё до нашего
	// explicit ALTER ниже — TTL "ленив" (нет гарантированного момента), но
	// это не значит "долго ждать". Явный ALTER TABLE ... MOVE PARTITION
	// остаётся ЕДИНСТВЕННЫМ детерминированным способом ГАРАНТИРОВАТЬ
	// результат в рамках одного хода независимо от того, успел фон сам или
	// нет — выполняется всегда, идемпотентно (если фон уже переместил
	// партицию, ALTER — no-op).
	oldPartsSeenBefore, oldPartsOnS3Before := 0, 0
	for _, p := range partsBefore {
		if p.partition == oldPartitionKey {
			oldPartsSeenBefore++
			if p.diskName == "s3" {
				oldPartsOnS3Before++
			}
		}
	}
	oldAlreadyOnS3 := oldPartsSeenBefore > 0 && oldPartsSeenBefore == oldPartsOnS3Before

	if oldAlreadyOnS3 {
		fmt.Printf("[tiering] content-note: фоновый merge scheduler УЖЕ переместил партицию %s на диск 's3' по правилу TTL ... TO VOLUME 'cold' ДО нашего explicit ALTER (TTL-условие было выполнено сразу при вставке — старая партиция на 45 дней старше 30-дневного TTL) — тот же честный эффект, что TTL DELETE в стенде #4 (лениво, но фон может успеть раньше клиента). ALTER TABLE ... MOVE PARTITION ниже пропущен как no-op (ClickHouse отказывает кодом 479 'All parts ... are already on disk', если реально его выполнить) — а не потому что мы поверили фону на слово: именно ЭТА проверка system.parts.disk_name и есть детерминированное подтверждение.\n", oldPartitionKey)
	} else {
		moveStart := time.Now()
		if err := ch.Exec(ctx, fmt.Sprintf("ALTER TABLE demo.%s MOVE PARTITION '%s' TO DISK 's3'", tblEvents, oldPartitionKey)); err != nil {
			log.Fatalf("move partition %s to disk s3: %v", oldPartitionKey, err)
		}
		moveElapsed := time.Since(moveStart)
		fmt.Printf("[tiering] ALTER TABLE ... MOVE PARTITION '%s' TO DISK 's3': %s (форсированное перемещение — детерминизм Step 2 брифа, НЕ TTL-поллинг)\n", oldPartitionKey, moveElapsed)
	}

	partsAfter, err := listParts(ctx, ch, tblEvents)
	if err != nil {
		log.Fatalf("list parts after move: %v", err)
	}
	fmt.Printf("[tiering] ПОСЛЕ MOVE PARTITION: %s\n", formatParts(partsAfter))

	var oldBytes, freshBytes int64
	var oldPartsSeen, freshPartsSeen int
	for _, p := range partsAfter {
		switch p.partition {
		case oldPartitionKey:
			assertFailFast(p.diskName == "s3", "часть перемещённой партиции %s на disk_name='s3' (факт: %s)", oldPartitionKey, p.diskName)
			oldBytes += p.bytes
			oldPartsSeen++
		case freshPartitionKey:
			assertFailFast(p.diskName == "default", "часть НЕтронутой партиции %s осталась на disk_name='default' (факт: %s)", freshPartitionKey, p.diskName)
			freshBytes += p.bytes
			freshPartsSeen++
		}
	}
	assertFailFast(oldPartsSeen > 0, "после MOVE PARTITION в system.parts есть хотя бы одна часть перемещённой партиции %s", oldPartitionKey)
	assertFailFast(freshPartsSeen > 0, "в system.parts есть хотя бы одна часть НЕтронутой партиции %s", freshPartitionKey)
	fmt.Printf("[tiering] размер на диске: старая партиция (s3)=%s, свежая партиция (default)=%s\n", humanBytes(oldBytes), humanBytes(freshBytes))

	// Запрос всё ещё работает: данные читаются С S3 (Step 2 брифа) — не
	// просто "не упал", а корректный count()/sum(revenue), плюс реальное
	// время чтения относительно локальной партиции того же прогона.
	const runs = 5
	oldQueryElapsed := medianDuration(runs, func() time.Duration { return timeQuery(ctx, ch, tblEvents, oldPartitionKey) })
	freshQueryElapsed := medianDuration(runs, func() time.Duration { return timeQuery(ctx, ch, tblEvents, freshPartitionKey) })

	oldCount, oldCents := countAndCents(ctx, ch, tblEvents, oldPartitionKey)
	freshCount, freshCents := countAndCents(ctx, ch, tblEvents, freshPartitionKey)

	fmt.Printf("[tiering] SELECT по перемещённой (s3) партиции %s: count()=%d, median времени за %d прогонов=%s\n", oldPartitionKey, oldCount, runs, oldQueryElapsed)
	fmt.Printf("[tiering] SELECT по локальной (default) партиции %s: count()=%d, median времени за %d прогонов=%s\n", freshPartitionKey, freshCount, runs, freshQueryElapsed)

	assertFailFast(oldCount == uint64(oldRows), "count() по перемещённой на s3 партиции == вставленным старым строкам (факт %d == %d)", oldCount, oldRows)
	assertFailFast(freshCount == uint64(freshRows), "count() по локальной партиции == вставленным свежим строкам (факт %d == %d)", freshCount, freshRows)
	assertFailFast(oldCents == oldRes.cents, "sum(revenue) по перемещённой на s3 партиции == независимо посчитанной сумме в Go, побайтово (факт %d == %d центов)", oldCents, oldRes.cents)
	assertFailFast(freshCents == freshRes.cents, "sum(revenue) по локальной партиции == независимо посчитанной сумме в Go, побайтово (факт %d == %d центов)", freshCents, freshRes.cents)

	totalCount, err := countRows(ctx, ch, tblEvents)
	if err != nil {
		log.Fatalf("count total: %v", err)
	}
	assertFailFast(totalCount == uint64(oldRows+freshRows), "count() по ВСЕЙ таблице (одна партиция на s3, другая локально) == сумме вставленного (факт %d == %d)", totalCount, oldRows+freshRows)

	fmt.Println("[tiering] content-note: запрос к части, физически лежащей на MinIO (диск 's3'), возвращает ТОТ ЖЕ корректный результат, что и запрос к локальной части — ClickHouse читает данные с s3-диска прозрачно для SQL-слоя, различие только в физическом расположении part'а (system.parts.disk_name) и, ожидаемо, в latency чтения (сеть до MinIO вместо локального диска).")
}
