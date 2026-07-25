package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// row — типизированная строка общего датасета (../dataset/main.go). Тот же
// паттерн, что ../go/mergetree/csvrow.go — код продублирован умышленно:
// независимые Go-модули стендов, без общего internal-пакета на этом этапе
// серии.
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

// csvRowReader — последовательное чтение CSV (с заголовком).
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
	rw, err := parseRow(rec)
	if err != nil {
		c.err = fmt.Errorf("row %d: %w", c.n, err)
		return row{}, false
	}
	return rw, true
}
