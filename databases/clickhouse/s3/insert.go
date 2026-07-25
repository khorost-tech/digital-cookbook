package main

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/shopspring/decimal"
)

const insertColumns = "(event_time, user_id, event_type, url, duration_ms, country, revenue)"

// Синтетический генератор этого стенда — НЕ общий dataset/main.go серии
// (тот используется #1/#3/#8 для яблоки-к-яблокам сравнений; здесь нужна
// лишь пара "старая партиция / свежая партиция" с реалистичной формой
// строки для честного sum(revenue)-ассерта, полноценное распределение
// событий не требуется). Честно зафиксировано в README.
var (
	syntheticEventTypes = []string{"page_view", "click", "search", "add_to_cart", "checkout", "purchase"}
	syntheticCountries  = []string{"RU", "US", "DE", "GB", "FR"}
)

// insertResult — вставленные строки + независимо посчитанная в Go сумма
// revenue в ЦЕНТАХ (int64, без плавающей точки) — тот же приём, что
// ../drivers/go verify: toInt64(sum(revenue)*100) на стороне ClickHouse
// сверяется побайтово с суммой, накопленной здесь при генерации.
type insertResult struct {
	rows    uint64
	cents   int64
	elapsed time.Duration
}

// insertSyntheticRows — вставляет n строк батчами по batchSize через
// PrepareBatch/Append/Send. event_time = day (00:00 UTC этого календарного
// дня) + случайный джиттер В ПРЕДЕЛАХ СУТОК — все строки попадают в ОДНУ
// партицию toDate(event_time), что и нужно для тиринга (Step 2 брифа:
// старая партиция целиком уезжает на s3, свежая остаётся локально).
// seed — детерминированный (воспроизводимый прогон).
func insertSyntheticRows(ctx context.Context, ch clickhouse.Conn, table string, n int, day time.Time, startUserID uint64, seed int64, batchSize int) (insertResult, error) {
	rnd := rand.New(rand.NewSource(seed))
	insertSQL := fmt.Sprintf("INSERT INTO demo.%s %s", table, insertColumns)
	dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)

	start := time.Now()
	var totalCents int64
	var inserted uint64

	var batch driver.Batch
	inBatch := 0
	newBatch := func() error {
		b, err := ch.PrepareBatch(ctx, insertSQL)
		if err != nil {
			return err
		}
		batch = b
		inBatch = 0
		return nil
	}
	if err := newBatch(); err != nil {
		return insertResult{}, fmt.Errorf("prepare batch: %w", err)
	}

	for i := 0; i < n; i++ {
		eventTime := dayStart.Add(time.Duration(rnd.Int63n(int64(24 * time.Hour))))
		eventType := syntheticEventTypes[rnd.Intn(len(syntheticEventTypes))]
		country := syntheticCountries[rnd.Intn(len(syntheticCountries))]
		cents := int64(100 + rnd.Intn(49900)) // 1.00..500.00
		revenue := decimal.New(cents, -2)
		userID := startUserID + uint64(i)
		durationMs := uint32(50 + rnd.Intn(7950))
		reqURL := fmt.Sprintf("/product/%d", rnd.Intn(100000))

		if err := batch.Append(eventTime, userID, eventType, reqURL, durationMs, country, revenue); err != nil {
			return insertResult{}, fmt.Errorf("append row %d: %w", i, err)
		}
		totalCents += cents
		inBatch++
		inserted++

		if inBatch >= batchSize {
			if err := batch.Send(); err != nil {
				return insertResult{}, fmt.Errorf("send batch at row %d: %w", inserted, err)
			}
			if err := newBatch(); err != nil {
				return insertResult{}, fmt.Errorf("prepare next batch: %w", err)
			}
		}
	}
	if inBatch > 0 {
		if err := batch.Send(); err != nil {
			return insertResult{}, fmt.Errorf("final batch send: %w", err)
		}
	}

	return insertResult{rows: inserted, cents: totalCents, elapsed: time.Since(start)}, nil
}
