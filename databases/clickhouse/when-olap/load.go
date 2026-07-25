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

// row — типизированная строка датасета + суррогатный id (номер строки CSV,
// 1-based) — общий для CH и PG загрузчиков, гарантирует одинаковые данные и
// одинаковый id в обеих СУБД (нужно для честных point-lookup/mutation
// сравнений на "тот же самый" id).
type row struct {
	id         uint64
	eventTime  time.Time
	userID     uint64
	eventType  string
	url        string
	durationMs uint32
	country    string
	revenue    decimal.Decimal
}

const csvTimeLayout = "2006-01-02 15:04:05"

func parseRow(id uint64, rec []string) (row, error) {
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
		id:         id,
		eventTime:  t,
		userID:     userID,
		eventType:  rec[2],
		url:        rec[3],
		durationMs: uint32(durationMs),
		country:    rec[5],
		revenue:    revenue,
	}, nil
}

// csvRowReader открывает CSV (со заголовком) и последовательно отдаёт
// типизированные строки с назначенным id = порядковый номер (1-based).
// Используется ОБОИМИ загрузчиками (CH и PG) — каждый открывает свой
// собственный csv.Reader поверх того же файла, чтобы не держать весь
// датасет в памяти и не делить один io.Reader между двумя consumer'ами.
type csvRowReader struct {
	f   *os.File
	r   *csv.Reader
	n   uint64
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

// next возвращает (row, true) пока есть данные; (zero, false) в конце (в т.ч.
// при ошибке — см. c.err).
func (c *csvRowReader) next() (row, bool) {
	rec, err := c.r.Read()
	if err == io.EOF {
		return row{}, false
	}
	if err != nil {
		c.err = fmt.Errorf("csv read: %w", err)
		return row{}, false
	}
	c.n++
	rw, err := parseRow(c.n, rec)
	if err != nil {
		c.err = fmt.Errorf("row %d: %w", c.n, err)
		return row{}, false
	}
	return rw, true
}

// loadClickHouse — батч-вставка (PrepareBatch/Append/Send) чанками по
// batchSize строк. Малые батчи/построчная вставка в MergeTree — известный
// анти-паттерн (много parts, см. будущий стенд #2 "MergeTree" и #6
// "эксплуатация"); здесь сознательно используем крупные батчи.
func loadClickHouse(ctx context.Context, conn clickhouse.Conn, path string, batchSize int) (rows uint64, elapsed time.Duration, err error) {
	cr, err := openCSVRowReader(path)
	if err != nil {
		return 0, 0, err
	}
	defer cr.close()

	start := time.Now()
	const insertSQL = "INSERT INTO demo.events (id, event_time, user_id, event_type, url, duration_ms, country, revenue)"

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
		if err := batch.Append(rw.id, rw.eventTime, rw.userID, rw.eventType, rw.url, rw.durationMs, rw.country, rw.revenue); err != nil {
			return rows, time.Since(start), fmt.Errorf("ch batch append row %d: %w", rw.id, err)
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

// pgRowSource — pgx.CopyFromSource поверх csvRowReader: COPY-протокол,
// самый быстрый способ массовой загрузки в PostgreSQL (на порядок быстрее
// построчных INSERT).
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
	return []any{s.cur.id, s.cur.eventTime, s.cur.userID, s.cur.eventType, s.cur.url, int32(s.cur.durationMs), s.cur.country, s.cur.revenue}, nil
}

func (s *pgRowSource) Err() error { return s.cr.err }

func loadPostgres(ctx context.Context, pool *pgxpool.Pool, path string) (rows int64, elapsed time.Duration, err error) {
	cr, err := openCSVRowReader(path)
	if err != nil {
		return 0, 0, err
	}
	defer cr.close()

	start := time.Now()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("pg acquire conn: %w", err)
	}
	defer conn.Release()

	src := &pgRowSource{cr: cr}
	n, err := conn.Conn().CopyFrom(ctx, pgx.Identifier{"events"},
		[]string{"id", "event_time", "user_id", "event_type", "url", "duration_ms", "country", "revenue"},
		src)
	if err != nil {
		return n, time.Since(start), fmt.Errorf("pg copy from: %w", err)
	}
	if src.Err() != nil {
		return n, time.Since(start), src.Err()
	}
	return n, time.Since(start), nil
}
