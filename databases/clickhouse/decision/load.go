package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

// row — типизированная строка общего датасета (dataset/main.go): event_time,
// user_id, event_type, url, duration_ms, country, revenue. Без суррогатного
// id (в отличие от when-olap) — этот стенд не делает точечных операций.
type row struct {
	eventTime  time.Time
	userID     uint64
	eventType  string
	url        string
	durationMs uint32
	country    string
	revenue    decimal.Decimal
}

const csvTimeLayout = "2006-01-02 15:04:05"

func parseRow(rec []string) (row, error) {
	if len(rec) != 7 {
		return row{}, fmt.Errorf("expected 7 columns, got %d", len(rec))
	}
	t, err := time.Parse(csvTimeLayout, rec[0])
	if err != nil {
		return row{}, fmt.Errorf("event_time: %w", err)
	}
	userID, err := strconv.ParseUint(rec[1], 10, 64)
	if err != nil {
		return row{}, fmt.Errorf("user_id: %w", err)
	}
	durationMs, err := strconv.ParseUint(rec[4], 10, 32)
	if err != nil {
		return row{}, fmt.Errorf("duration_ms: %w", err)
	}
	revenue, err := decimal.NewFromString(rec[6])
	if err != nil {
		return row{}, fmt.Errorf("revenue: %w", err)
	}
	return row{
		eventTime:  t,
		userID:     userID,
		eventType:  rec[2],
		url:        rec[3],
		durationMs: uint32(durationMs),
		country:    rec[5],
		revenue:    revenue,
	}, nil
}

// csvRowReader — последовательное чтение общего CSV (заголовок пропускается).
// Каждый загрузчик (CH/PG/Timescale) открывает СВОЙ csv.Reader поверх ТОГО
// ЖЕ файла — не делит один io.Reader между несколькими consumer'ами, не
// держит весь датасет в памяти.
type csvRowReader struct {
	f   *os.File
	r   *csv.Reader
	err error
}

func openCSVRowReader(path string) (*csvRowReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(f, 1<<20)
	r := csv.NewReader(br)
	if _, err := r.Read(); err != nil { // header
		f.Close()
		return nil, fmt.Errorf("read header: %w", err)
	}
	return &csvRowReader{f: f, r: r}, nil
}

func (c *csvRowReader) close() { c.f.Close() }

func (c *csvRowReader) next() (row, bool) {
	rec, err := c.r.Read()
	if err == io.EOF {
		return row{}, false
	}
	if err != nil {
		c.err = fmt.Errorf("csv read: %w", err)
		return row{}, false
	}
	rw, err := parseRow(rec)
	if err != nil {
		c.err = fmt.Errorf("row parse: %w", err)
		return row{}, false
	}
	return rw, true
}

// loadClickHouse — батч-вставка (PrepareBatch/Append/Send), тот же приём,
// что стенды #1/#2 серии. Малые батчи/построчная вставка — анти-паттерн
// MergeTree (см. README стенда #2), здесь используется крупный батч.
func loadClickHouse(ctx context.Context, conn clickhouse.Conn, path string, batchSize int) (rows uint64, elapsed time.Duration, err error) {
	cr, err := openCSVRowReader(path)
	if err != nil {
		return 0, 0, err
	}
	defer cr.close()

	start := time.Now()
	const insertSQL = "INSERT INTO demo.decision_events (event_time, user_id, event_type, url, duration_ms, country, revenue)"

	var batch driver.Batch
	inBatch := 0
	newBatch := func() error {
		b, err := conn.PrepareBatch(ctx, insertSQL)
		if err != nil {
			return err
		}
		batch = b
		inBatch = 0
		return nil
	}
	if err := newBatch(); err != nil {
		return 0, 0, fmt.Errorf("ch prepare batch: %w", err)
	}

	for {
		rw, ok := cr.next()
		if !ok {
			break
		}
		if err := batch.Append(rw.eventTime, rw.userID, rw.eventType, rw.url, rw.durationMs, rw.country, rw.revenue); err != nil {
			return rows, time.Since(start), fmt.Errorf("ch batch append: %w", err)
		}
		inBatch++
		rows++
		if inBatch >= batchSize {
			if err := batch.Send(); err != nil {
				return rows, time.Since(start), fmt.Errorf("ch batch send at row %d: %w", rows, err)
			}
			if err := newBatch(); err != nil {
				return rows, time.Since(start), fmt.Errorf("ch prepare next batch: %w", err)
			}
		}
	}
	if cr.err != nil {
		return rows, time.Since(start), cr.err
	}
	if inBatch > 0 {
		if err := batch.Send(); err != nil {
			return rows, time.Since(start), fmt.Errorf("ch final batch send: %w", err)
		}
	}
	return rows, time.Since(start), nil
}

// pgRowSource — pgx.CopyFromSource поверх csvRowReader: COPY-протокол
// (НЕ построчный INSERT) — используется И для baseline PG, И для
// TimescaleDB (одна и та же таблица/колонки, hypertable маршрутизирует
// строки по чанкам прозрачно для COPY).
type pgRowSource struct {
	cr      *csvRowReader
	cur     row
	haveCur bool
}

func (s *pgRowSource) Next() bool {
	rw, ok := s.cr.next()
	if !ok {
		return false
	}
	s.cur = rw
	s.haveCur = true
	return true
}

func (s *pgRowSource) Values() ([]any, error) {
	if !s.haveCur {
		return nil, fmt.Errorf("Values called before Next")
	}
	return []any{s.cur.eventTime, s.cur.userID, s.cur.eventType, s.cur.url, int32(s.cur.durationMs), s.cur.country, s.cur.revenue}, nil
}

func (s *pgRowSource) Err() error { return s.cr.err }

var decisionEventsColumns = []string{"event_time", "user_id", "event_type", "url", "duration_ms", "country", "revenue"}

// loadPGLike — общий bulk-COPY загрузчик для baseline PG И TimescaleDB
// (идентичная схема decision_events на обеих сторонах).
func loadPGLike(ctx context.Context, pool *pgxpool.Pool, path string) (rows int64, elapsed time.Duration, err error) {
	cr, err := openCSVRowReader(path)
	if err != nil {
		return 0, 0, err
	}
	defer cr.close()

	start := time.Now()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()

	src := &pgRowSource{cr: cr}
	n, err := conn.Conn().CopyFrom(ctx, pgx.Identifier{"decision_events"}, decisionEventsColumns, src)
	if err != nil {
		return n, time.Since(start), fmt.Errorf("copy from: %w", err)
	}
	if src.Err() != nil {
		return n, time.Since(start), src.Err()
	}
	return n, time.Since(start), nil
}
