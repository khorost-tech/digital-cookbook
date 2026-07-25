package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/shopspring/decimal"
)

// phaseTTL — Step 2в брифа: TTL на партициях. Прод-DDL серии — `TTL
// event_time + INTERVAL 90 DAY` (брифа), здесь для живой проверки в рамках
// одного хода интервал сжат до 3 суток, а строки вставляются с СИНТЕТИЧЕСКИМ
// event_time (не now()) — старая партиция уже "просрочена" в момент
// вставки. PARTITION BY toDate(event_time): когда ВСЕ строки партиции
// просрочены, ClickHouse может целиком дропнуть партицию при merge, а не
// построчно переписывать part — быстрый путь TTL.
func phaseTTL(ctx context.Context, ch clickhouse.Conn) {
	fmt.Println("\n=== TTL на партициях (Step 2в брифа) ===")

	mustRun(func() error { return createTTLTable(ctx, ch, tblTTLDemo) })

	now := time.Now().UTC()
	oldTime := now.Add(-5 * 24 * time.Hour) // старше TTL (3 дня) на 2 дня — гарантированно просрочено
	freshTime := now                        // свежее TTL — должно остаться

	const nOld = 500
	const nFresh = 500

	if err := insertTTLRows(ctx, ch, tblTTLDemo, nOld, oldTime, 1); err != nil {
		log.Fatalf("insert old TTL rows: %v", err)
	}
	if err := insertTTLRows(ctx, ch, tblTTLDemo, nFresh, freshTime, 1_000_000); err != nil {
		log.Fatalf("insert fresh TTL rows: %v", err)
	}
	fmt.Printf("[ttl] вставлено: %d строк с event_time=%s (просрочено относительно TTL 3 дня), %d строк с event_time=%s (свежие)\n",
		nOld, oldTime.Format(csvTimeLayout), nFresh, freshTime.Format(csvTimeLayout))

	rowsBefore, err := countRows(ctx, ch, tblTTLDemo)
	if err != nil {
		log.Fatalf("count before TTL: %v", err)
	}
	partsBefore, err := listPartitions(ctx, ch, tblTTLDemo)
	if err != nil {
		log.Fatalf("list partitions before TTL: %v", err)
	}
	// НЕ ассертим конкретное значение здесь: TTL описывается как "ленивый"
	// (применяется фоновым merge), но на простаивающем single-node dev-
	// сервере background merge scheduler может подобрать свежевставленные
	// мелкие parts и применить TTL за миллисекунды — быстрее, чем наш
	// клиент успевает сделать следующий SELECT. Живой прогон честно может
	// показать rowsBefore ЛИБО == nOld+nFresh (TTL ещё не применился),
	// ЛИБО уже == nFresh (фон обогнал). Оба исхода корректны — TTL как
	// механизм не гарантирует момент применения без явного FINAL.
	fmt.Printf("[ttl] ДО явного OPTIMIZE FINAL: count()=%d, партиции: %s (TTL ленив — фон мог уже подобрать просроченную партицию сам, без ожидания явного FINAL)\n", rowsBefore, formatPartitions(partsBefore))

	// OPTIMIZE ... FINAL форсирует merge всей таблицы ПРЯМО СЕЙЧАС —
	// TTL-правила проверяются как часть этого merge — единственный
	// ДЕТЕРМИНИРОВАННЫЙ момент, когда результат гарантирован независимо от
	// того, успел фон отработать сам или нет.
	if err := ch.Exec(ctx, fmt.Sprintf("OPTIMIZE TABLE demo.%s FINAL", tblTTLDemo)); err != nil {
		log.Fatalf("optimize final %s: %v", tblTTLDemo, err)
	}

	rowsAfter, err := countRows(ctx, ch, tblTTLDemo)
	if err != nil {
		log.Fatalf("count after TTL: %v", err)
	}
	partsAfter, err := listPartitions(ctx, ch, tblTTLDemo)
	if err != nil {
		log.Fatalf("list partitions after TTL: %v", err)
	}
	fmt.Printf("[ttl] ПОСЛЕ OPTIMIZE FINAL (гарантированная точка): count()=%d, партиции: %s\n", rowsAfter, formatPartitions(partsAfter))

	oldPartitionKey := oldTime.Format("2006-01-02")
	freshPartitionKey := freshTime.Format("2006-01-02")
	oldRowsAfter := partitionRows(partsAfter, oldPartitionKey)
	freshRowsAfter := partitionRows(partsAfter, freshPartitionKey)

	assertFailFast(rowsAfter == uint64(nFresh), "TTL DELETE (event_time + INTERVAL 3 DAY) удалил просроченные строки при OPTIMIZE FINAL: осталось %d == только свежие %d (было %d)", rowsAfter, nFresh, rowsBefore)
	assertFailFast(oldRowsAfter == 0, "просроченная партиция %s опустела (0 строк) после OPTIMIZE FINAL: rows=%d", oldPartitionKey, oldRowsAfter)
	assertFailFast(freshRowsAfter == uint64(nFresh), "свежая партиция %s не тронута TTL: rows=%d == %d", freshPartitionKey, freshRowsAfter, nFresh)

	// Честная деталь: пустой part для просроченной партиции может ОСТАТЬСЯ
	// в system.parts (active=1, rows=0) до отдельного фонового цикла
	// очистки пустых parts — "партиция физически дропнута" и "партиция
	// опустела" (0 строк, но запись part ещё жива) не одно и то же событие.
	if partitionRowsPresent(partsAfter, oldPartitionKey) {
		fmt.Printf("[ttl] content-note: партиция %s всё ещё присутствует в system.parts КАК ЗАПИСЬ (active part с rows=0) — TTL DELETE опустошил part, но физическая очистка пустого part'а из system.parts — отдельный, более поздний фоновый цикл, не тот же момент, что удаление строк\n", oldPartitionKey)
	} else {
		fmt.Printf("[ttl] партиция %s полностью исчезла из system.parts (part физически дропнут)\n", oldPartitionKey)
	}
}

func partitionRows(parts []partitionInfo, key string) uint64 {
	for _, p := range parts {
		if p.partition == key {
			return p.rows
		}
	}
	return 0
}

func partitionRowsPresent(parts []partitionInfo, key string) bool {
	for _, p := range parts {
		if p.partition == key {
			return true
		}
	}
	return false
}

func insertTTLRows(ctx context.Context, ch clickhouse.Conn, table string, n int, eventTime time.Time, startUserID uint64) error {
	insertSQL := fmt.Sprintf("INSERT INTO demo.%s %s", table, insertColumns)
	batch, err := ch.PrepareBatch(ctx, insertSQL)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for i := 0; i < n; i++ {
		if err := batch.Append(eventTime, startUserID+uint64(i), "click", "/ttl-demo", uint32(100), "RU", decimal.Zero); err != nil {
			return fmt.Errorf("append row %d: %w", i, err)
		}
	}
	return batch.Send()
}

type partitionInfo struct {
	partition string
	rows      uint64
}

func listPartitions(ctx context.Context, ch clickhouse.Conn, table string) ([]partitionInfo, error) {
	rows, err := ch.Query(ctx, `
		SELECT partition, sum(rows) AS rows
		FROM system.parts
		WHERE database = 'demo' AND table = ? AND active
		GROUP BY partition
		ORDER BY partition`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []partitionInfo
	for rows.Next() {
		var p partitionInfo
		if err := rows.Scan(&p.partition, &p.rows); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func formatPartitions(parts []partitionInfo) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%s=%d rows", p.partition, p.rows)
	}
	if s == "" {
		return "(нет активных партиций)"
	}
	return s
}
