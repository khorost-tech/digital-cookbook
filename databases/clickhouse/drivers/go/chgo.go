package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/ClickHouse/ch-go/proto"
)

// chGoInsert — низкоуровневый колоночный INSERT через ch-go (нативный
// протокол напрямую, без обёртки clickhouse-go/v2): КАЖДАЯ колонка
// заполняется вручную через proto.Col* (Step 2 брифа), без ORM/маппинга
// структур. Это самый "тонкий" путь к серверу из всех 6 драйверов стенда —
// именно такой ценой достигается минимальный клиентский оверхед.
//
//   - event_time  DateTime          -> proto.ColDateTime
//   - user_id     UInt64            -> proto.ColUInt64
//   - event_type  LowCardinality(String) -> *proto.ColStr LowCardinality()
//   - url         String            -> proto.ColStr
//   - duration_ms UInt32            -> proto.ColUInt32
//   - country     LowCardinality(String) -> *proto.ColStr LowCardinality()
//   - revenue     Decimal(10,2)     -> proto.ColDecimal64 + proto.Alias(...,
//     "Decimal(10, 2)") — Decimal(10,2) на сервере хранится как Decimal64
//     (precision 10 попадает в диапазон 10..18), сырое значение колонки —
//     это ЦЕЛОЕ число "центов" (revenue * 10^scale), без плавающей точки.
//
// Батчи по batchSize строк (тот же размер, что у clickhouse-go native —
// apples-to-apples), каждый батч — отдельный client.Do() с proto.Input.
func chGoInsert(ctx context.Context, addr, table, csvPath string, batchSize int) (rows uint64, elapsed time.Duration, err error) {
	client, err := ch.Dial(ctx, ch.Options{Address: addr, Database: "demo"})
	if err != nil {
		return 0, 0, fmt.Errorf("ch-go dial: %w", err)
	}
	defer client.Close()

	cr, err := openCSVRowReader(csvPath)
	if err != nil {
		return 0, 0, err
	}
	defer cr.close()

	insertSQL := fmt.Sprintf("INSERT INTO demo.%s (event_time, user_id, event_type, url, duration_ms, country, revenue) VALUES", table)

	start := time.Now()
	for {
		var (
			eventTime  proto.ColDateTime
			userID     proto.ColUInt64
			eventType  = new(proto.ColStr).LowCardinality()
			urlCol     proto.ColStr
			durationMs proto.ColUInt32
			country    = new(proto.ColStr).LowCardinality()
			revenue    proto.ColDecimal64
		)

		n := 0
		for n < batchSize {
			rw, ok := cr.next()
			if !ok {
				break
			}
			eventTime.Append(rw.eventTime)
			userID.Append(rw.userID)
			eventType.Append(rw.eventType)
			urlCol.Append(rw.url)
			durationMs.Append(rw.durationMs)
			country.Append(rw.country)
			revenue.Append(proto.Decimal64(rw.revenueCents))
			n++
		}
		if cr.err != nil {
			return rows, time.Since(start), cr.err
		}

		if n > 0 {
			input := proto.Input{
				{Name: "event_time", Data: &eventTime},
				{Name: "user_id", Data: &userID},
				{Name: "event_type", Data: eventType},
				{Name: "url", Data: &urlCol},
				{Name: "duration_ms", Data: &durationMs},
				{Name: "country", Data: country},
				{Name: "revenue", Data: proto.Alias(&revenue, "Decimal(10, 2)")},
			}
			if err := client.Do(ctx, ch.Query{Body: insertSQL, Input: input}); err != nil {
				return rows, time.Since(start), fmt.Errorf("ch-go insert batch at row %d: %w", rows, err)
			}
			rows += uint64(n)
		}
		if n < batchSize {
			break
		}
	}
	return rows, time.Since(start), nil
}

// chGoSelect — тот же аналитический SELECT (aggregate.go), декодируемый
// вручную в типизированные proto.Col* (country String, c UInt64, cents
// Int64 — см. комментарий у aggregateSQLTemplate), без auto-инференса типов
// (proto.Results.Auto()) — раз уж стенд демонстрирует "ручной" путь ch-go,
// SELECT тоже разбирается вручную, симметрично INSERT.
func chGoSelect(ctx context.Context, addr, table string) (aggResult, time.Duration, error) {
	client, err := ch.Dial(ctx, ch.Options{Address: addr, Database: "demo"})
	if err != nil {
		return aggResult{}, 0, fmt.Errorf("ch-go dial: %w", err)
	}
	defer client.Close()

	var (
		// country приходит из GROUP BY по LowCardinality(String)-колонке —
		// ClickHouse сохраняет обёртку LowCardinality в результате (см.
		// комментарий у aggregateSQLTemplate) даже без запроса такого типа
		// клиентом, поэтому декодируем его именно так, а не как ColStr.
		countryCol = new(proto.ColStr).LowCardinality()
		cCol       proto.ColUInt64
		centsCol   proto.ColInt64
		triples    []triple
	)

	start := time.Now()
	err = client.Do(ctx, ch.Query{
		Body: fmt.Sprintf(aggregateSQLTemplate, table),
		Result: proto.Results{
			{Name: "country", Data: countryCol},
			{Name: "c", Data: &cCol},
			{Name: "cents", Data: &centsCol},
		},
		OnResult: func(ctx context.Context, block proto.Block) error {
			for i := 0; i < block.Rows; i++ {
				triples = append(triples, triple{
					country: countryCol.Row(i),
					c:       cCol.Row(i),
					cents:   centsCol.Row(i),
				})
			}
			return nil
		},
	})
	dur := time.Since(start)
	if err != nil {
		return aggResult{}, dur, fmt.Errorf("ch-go select: %w", err)
	}
	return computeAgg(triples), dur, nil
}
