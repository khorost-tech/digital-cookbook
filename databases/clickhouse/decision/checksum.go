package main

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"time"
)

// checksumAcc — тот же приём межсистемной сверки, что стенд #3 (drivers):
// канонический текст "country|date|cnt|cents\n" на группу, накапливается в
// порядке ORDER BY country, d (все 4 SQL этого стенда сортируют одинаково),
// CRC32-IEEE по всему накопленному тексту в конце — побайтовое сравнение
// между ClickHouse/PostgreSQL/TimescaleDB/DuckDB без плавающей точки
// (revenue сравнивается как целые центы, не Decimal/NUMERIC/DOUBLE).
type checksumAcc struct {
	buf        bytes.Buffer
	groups     int
	totalCents int64
}

func newChecksum() *checksumAcc { return &checksumAcc{} }

func (a *checksumAcc) add(country string, d time.Time, cnt int64, cents int64) {
	fmt.Fprintf(&a.buf, "%s|%s|%d|%d\n", country, d.Format("2006-01-02"), cnt, cents)
	a.groups++
	a.totalCents += cents
}

type checksumResult struct {
	groups     int
	totalCents int64
	checksum   uint32
}

func (a *checksumAcc) result() checksumResult {
	return checksumResult{groups: a.groups, totalCents: a.totalCents, checksum: crc32.ChecksumIEEE(a.buf.Bytes())}
}

type aggResult struct {
	duration time.Duration
}
