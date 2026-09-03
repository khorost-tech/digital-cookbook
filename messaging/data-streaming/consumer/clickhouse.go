package main

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/shopspring/decimal"
)

// total — одна строка витрины.
type total struct {
	CustomerID uint64 `json:"customer_id"`
	Orders     uint64 `json:"orders"`
	// Total — деньги во float64 при разборе JSON: осознанное упрощение ради
	// краткости примера, как и в Kafka Streams выше по конвейеру (см.
	// StreamsApp.java, TotalsProcessor.process()). В проде — целые копейки
	// (int64) или decimal-тип целиком, не float64. Здесь это не бесплатно:
	// колонка ds.customer_totals/raw_totals типизирована как Decimal(12,2), а
	// clickhouse-go в батч-протоколе принимает под Decimal только
	// decimal.Decimal (github.com/shopspring/decimal) — конвертация
	// decimal.NewFromFloat() в insertInto ниже уже наследует неточность
	// float64, а не устраняет её.
	Total float64 `json:"total"`
}

// orderEvent — одна строка изолированного дедуп-эксперимента (режим
// -mode dedup-demo, см. main.go): читает orders.events (сырые события outbox,
// формат payload `{"amount":..,"customer_id":..,"order_id":..}` — сверено с
// реальным топиком в FIXTURES.md), а не customer.totals. amount здесь не
// нужен — эксперимент считает СОБЫТИЯ (+1 на заказ), а не деньги; JSON-поля,
// не упомянутые в структуре (amount, currency и т.п.), encoding/json молча
// игнорирует.
type orderEvent struct {
	OrderID    uint64 `json:"order_id"`
	CustomerID uint64 `json:"customer_id"`
}

const (
	insertCustomerTotals = "INSERT INTO ds.customer_totals (customer_id, orders, total, version)"
	insertRawTotals      = "INSERT INTO ds.raw_totals (customer_id, orders, total, version)"
	insertOrderCountsRaw = "INSERT INTO ds.order_counts_raw (customer_id, cnt)"
	insertOrderDedup     = "INSERT INTO ds.order_dedup (order_id, customer_id, version)"
)

func chConnect(addr string) (driver.Conn, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{addr},
		Auth: clickhouse.Auth{Database: "ds", Username: "ds", Password: "ds"},
		// optimize_on_insert=0: сервер по умолчанию (1) дедуплицирует дубли
		// ReplacingMergeTree ВНУТРИ одного INSERT-батча ещё на этапе записи —
		// до всякого фонового merge. При батч-вставке (см. insertBatch ниже)
		// это означало бы, что baseline (один INSERT на весь топик) сразу
		// ложится в customer_totals уже дедуплицированным, а -dup (тоже один
		// INSERT на прогон, дубли внутри него же) — тоже, и разница между
		// прогонами с -dup и без исчезает: cм. Critical 1/Important 2 круга 2
		// ревью. Явный 0 здесь — не "трюк ради красивых чисел", а снятие
		// зависимости демонстрации от серверного дефолта, который читатель
		// вполне может сменить у себя (или который сменится с версией CH).
		Settings: clickhouse.Settings{
			"optimize_on_insert": 0,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("clickhouse open: %w", err)
	}
	if err := conn.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("clickhouse ping: %w", err)
	}
	return conn, nil
}

// insertBatch пишет пачку строк ОДНИМ INSERT в customer_totals (витрина с
// дедупом через ReplacingMergeTree) и ОДНИМ INSERT в raw_totals (контрольная
// витрина без дедупа) — то же самое сообщение в обе таблицы, чтобы разница
// между ними была следствием движка, а не случайности момента вставки.
//
// version = offset исходного сообщения Kafka: ReplacingMergeTree оставит при
// слиянии запись с максимальным version, погасив дубли; raw_totals version
// хранит для полноты структуры, но сама таблица его не использует.
//
// Один INSERT на пачку, а не один на сообщение: построчные INSERT в ClickHouse
// на живом потоке — канонический антипаттерн (каждый создаёт отдельный part).
func insertBatch(ctx context.Context, conn driver.Conn, totals []total, versions []uint64) error {
	if len(totals) == 0 {
		return nil
	}
	if err := insertInto(ctx, conn, insertCustomerTotals, totals, versions); err != nil {
		return fmt.Errorf("customer_totals: %w", err)
	}
	if err := insertInto(ctx, conn, insertRawTotals, totals, versions); err != nil {
		return fmt.Errorf("raw_totals: %w", err)
	}
	return nil
}

func insertInto(ctx context.Context, conn driver.Conn, query string, totals []total, versions []uint64) error {
	batch, err := conn.PrepareBatch(ctx, query)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for i, t := range totals {
		if err := batch.Append(t.CustomerID, t.Orders, decimal.NewFromFloat(t.Total), versions[i]); err != nil {
			return fmt.Errorf("append: %w", err)
		}
	}
	return batch.Send()
}

// insertOrderCountBatch — сток режима -mode dedup-demo: пишет пачку СОБЫТИЙ
// (не снимков состояния) в обе таблицы изолированного дедуп-эксперимента,
// той же парой ОДИН И ТОТ ЖЕ вход -> два движка, что и insertBatch выше для
// customer_totals/raw_totals (см. комментарий у sql/02-clickhouse.sql про то,
// почему нужен именно этот, отдельный от §3, эксперимент).
func insertOrderCountBatch(ctx context.Context, conn driver.Conn, orders []orderEvent, versions []uint64) error {
	if len(orders) == 0 {
		return nil
	}
	if err := insertOrderCounts(ctx, conn, orders); err != nil {
		return fmt.Errorf("order_counts_raw: %w", err)
	}
	if err := insertOrderDedupRows(ctx, conn, orders, versions); err != nil {
		return fmt.Errorf("order_dedup: %w", err)
	}
	return nil
}

// insertOrderCounts пишет cnt=1 на КАЖДОЕ событие в SummingMergeTree —
// движок складывает cnt при слиянии, поэтому дубль (повторная доставка,
// -from-start поверх уже прочитанного) реально удваивает sum(cnt), в отличие
// от customer_totals/raw_totals, где такого прямого эффекта нет (см. комментарий
// у sql/02-clickhouse.sql).
func insertOrderCounts(ctx context.Context, conn driver.Conn, orders []orderEvent) error {
	batch, err := conn.PrepareBatch(ctx, insertOrderCountsRaw)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, o := range orders {
		if err := batch.Append(o.CustomerID, uint64(1)); err != nil {
			return fmt.Errorf("append: %w", err)
		}
	}
	return batch.Send()
}

// insertOrderDedupRows пишет один ряд (order_id, customer_id, version) на
// событие в ReplacingMergeTree(version) с ORDER BY order_id — при слиянии и
// чтении через FINAL повторная доставка ТОГО ЖЕ order_id схлопывается в одну
// строку, поэтому count() FINAL не растёт от дублей/replay, в отличие от
// order_counts_raw выше.
func insertOrderDedupRows(ctx context.Context, conn driver.Conn, orders []orderEvent, versions []uint64) error {
	batch, err := conn.PrepareBatch(ctx, insertOrderDedup)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for i, o := range orders {
		if err := batch.Append(o.OrderID, o.CustomerID, versions[i]); err != nil {
			return fmt.Errorf("append: %w", err)
		}
	}
	return batch.Send()
}
