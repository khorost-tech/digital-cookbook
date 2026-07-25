package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/shopspring/decimal"
)

// summingRow — синтетическая строка для demo.mv_events_summing: несколько
// строк с ОДИНАКОВЫМ ключом сортировки (country, hour) — SummingMergeTree
// суммирует числовые колонки ВНЕ ORDER BY при фоновом merge (или OPTIMIZE
// ... FINAL), схлопывая их в одну строку.
type summingRow struct {
	country string
	hour    string // 'YYYY-MM-DD HH:MM:SS', парсится в time.Time перед вставкой
	revenue string // передаётся как строка в Decimal-параметр
	events  uint64
}

// phaseSumming — Step 2а брифа: SummingMergeTree как более простой случай
// суммирования, чем AggregatingMergeTree (Step 1) — не нужны -State/-Merge,
// просто числовые колонки складываются при merge по ключу ORDER BY.
func phaseSumming(ctx context.Context, ch clickhouse.Conn) {
	fmt.Println("\n=== SummingMergeTree: простое суммирование (Step 2а брифа) ===")

	mustRun(func() error { return createSummingTable(ctx, ch, tblSumming) })

	rows := []summingRow{
		{"RU", "2026-06-15 10:00:00", "10.50", 1},
		{"RU", "2026-06-15 10:00:00", "20.25", 1},
		{"RU", "2026-06-15 10:00:00", "5.00", 1},
		{"RU", "2026-06-15 10:00:00", "100.00", 1},
		{"RU", "2026-06-15 10:00:00", "0.25", 1},
		{"US", "2026-06-15 11:00:00", "300.00", 1},
		{"US", "2026-06-15 11:00:00", "199.99", 1},
	}
	insertSQL := fmt.Sprintf("INSERT INTO demo.%s (country, hour, revenue, events) VALUES (?, ?, ?, ?)", tblSumming)
	for _, r := range rows {
		rev, err := decimal.NewFromString(r.revenue)
		if err != nil {
			log.Fatalf("parse revenue %s: %v", r.revenue, err)
		}
		hourTime, err := time.Parse(csvTimeLayout, r.hour)
		if err != nil {
			log.Fatalf("parse hour %s: %v", r.hour, err)
		}
		if err := ch.Exec(ctx, insertSQL, r.country, hourTime, rev, r.events); err != nil {
			log.Fatalf("insert summing row: %v", err)
		}
	}
	fmt.Printf("[summing] вставлено %d отдельных строк (5×RU@10:00, 2×US@11:00) в demo.%s\n", len(rows), tblSumming)

	ruHour, err := time.Parse(csvTimeLayout, "2026-06-15 10:00:00")
	if err != nil {
		log.Fatalf("parse RU hour: %v", err)
	}
	usHour, err := time.Parse(csvTimeLayout, "2026-06-15 11:00:00")
	if err != nil {
		log.Fatalf("parse US hour: %v", err)
	}

	partsBeforeRU, cntBeforeRU := readSummingKey(ctx, ch, "RU", ruHour)
	fmt.Printf("[summing] ДО OPTIMIZE FINAL: SELECT для (RU, 10:00) вернул %d строк(и), сумма revenue по ним=%s\n", partsBeforeRU, cntBeforeRU.String())

	if err := ch.Exec(ctx, fmt.Sprintf("OPTIMIZE TABLE demo.%s FINAL", tblSumming)); err != nil {
		log.Fatalf("optimize final %s: %v", tblSumming, err)
	}

	nRU, sumRU := readSummingKey(ctx, ch, "RU", ruHour)
	nUS, sumUS := readSummingKey(ctx, ch, "US", usHour)
	fmt.Printf("[summing] ПОСЛЕ OPTIMIZE FINAL: (RU, 10:00) -> %d строка, revenue=%s; (US, 11:00) -> %d строка, revenue=%s\n",
		nRU, sumRU.String(), nUS, sumUS.String())

	expectedRU := decimal.RequireFromString("10.50").Add(decimal.RequireFromString("20.25")).Add(decimal.RequireFromString("5.00")).Add(decimal.RequireFromString("100.00")).Add(decimal.RequireFromString("0.25"))
	expectedUS := decimal.RequireFromString("300.00").Add(decimal.RequireFromString("199.99"))

	assertFailFast(nRU == 1, "SummingMergeTree после OPTIMIZE FINAL: ровно 1 строка на ключ (RU, 10:00): got %d", nRU)
	assertFailFast(nUS == 1, "SummingMergeTree после OPTIMIZE FINAL: ровно 1 строка на ключ (US, 11:00): got %d", nUS)
	assertFailFast(sumRU.Equal(expectedRU), "SummingMergeTree просуммировал (RU, 10:00) корректно: got=%s, expected=%s", sumRU.String(), expectedRU.String())
	assertFailFast(sumUS.Equal(expectedUS), "SummingMergeTree просуммировал (US, 11:00) корректно: got=%s, expected=%s", sumUS.String(), expectedUS.String())
}

func readSummingKey(ctx context.Context, ch clickhouse.Conn, country string, hour time.Time) (int, decimal.Decimal) {
	rows, err := ch.Query(ctx, fmt.Sprintf("SELECT revenue FROM demo.%s WHERE country = ? AND hour = ?", tblSumming), country, hour)
	if err != nil {
		log.Fatalf("query summing key (%s,%s): %v", country, hour, err)
	}
	defer rows.Close()
	n := 0
	total := decimal.Zero
	for rows.Next() {
		var rev decimal.Decimal
		if err := rows.Scan(&rev); err != nil {
			log.Fatalf("scan summing row: %v", err)
		}
		total = total.Add(rev)
		n++
	}
	if rows.Err() != nil {
		log.Fatalf("summing rows err: %v", rows.Err())
	}
	return n, total
}
