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

// row — типизированная строка общего датасета (../../dataset/main.go), тот
// же паттерн, что ../../go/mergetree/csvrow.go. revenueCents добавлен для
// низкоуровневой ch-go-вставки (Decimal64 хранит значение, умноженное на
// 10^scale, целым числом — парсим его напрямую из строки без потери
// точности, минуя decimal.Decimal, который используется остальными
// Go-драйверами (clickhouse-go native/database-sql).
type row struct {
	eventTime    time.Time
	userID       uint64
	eventType    string
	url          string
	durationMs   uint32
	country      string
	revenue      decimal.Decimal
	revenueCents int64
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
	cents, err := parseCentsExact(rec[6])
	if err != nil {
		return row{}, fmt.Errorf("revenue cents: %w", err)
	}
	return row{
		eventTime:    t,
		userID:       userID,
		eventType:    rec[2],
		url:          rec[3],
		durationMs:   uint32(durationMs),
		country:      rec[5],
		revenue:      revenue,
		revenueCents: cents,
	}, nil
}

// parseCentsExact парсит десятичную строку с РОВНО 2 дробными знаками
// (гарантировано dataset/main.go: strconv.FormatFloat(..., 'f', 2, 64)) в
// целые "центы" без округления float/decimal — устраняет любую
// неоднозначность при ручном заполнении ch-go Decimal64-колонки (revenue
// Decimal(10,2) хранит значение, умноженное на 10^2, т.е. именно эти центы).
func parseCentsExact(s string) (int64, error) {
	neg := false
	if len(s) > 0 && s[0] == '-' {
		neg = true
		s = s[1:]
	}
	dot := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 || len(s)-dot-1 != 2 {
		return 0, fmt.Errorf("expected exactly 2 fractional digits: %q", s)
	}
	intPart, err := strconv.ParseInt(s[:dot], 10, 64)
	if err != nil {
		return 0, err
	}
	fracPart, err := strconv.ParseInt(s[dot+1:], 10, 64)
	if err != nil {
		return 0, err
	}
	cents := intPart*100 + fracPart
	if neg {
		cents = -cents
	}
	return cents, nil
}

// csvRowReader — последовательное чтение CSV (с заголовком), тот же паттерн,
// что в ../../go/mergetree/csvrow.go и ../../when-olap/load.go (каждый
// стенд — независимый Go-модуль, код продублирован умышленно).
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
