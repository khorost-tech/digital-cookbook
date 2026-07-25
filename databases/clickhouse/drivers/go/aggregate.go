package main

import (
	"fmt"
	"hash/crc32"
	"strings"
)

// aggregateSQLTemplate — ЕДИНЫЙ аналитический запрос для ВСЕХ 6 драйверов
// (4 Go + 2 Java, ops/drivers-bench.sh). toInt64(sum(revenue) * 100)
// фиксирует РЕЗУЛЬТИРУЮЩИЙ тип cents (Int64) одинаково для ch-go (ручной
// proto.Col*), clickhouse-go native/database-sql, JDBC и client-v2 — без
// этого sum(revenue) вернулся бы Decimal с "плавающей" шириной (Decimal(20,2)
// vs Decimal(38,2) у разных клиентов), что мешало бы побайтовому сравнению
// чексуммы. revenue Decimal(10,2) кодирует ровно 2 дробных знака — умножение
// на 100 и truncation в Int64 даёт "центы" без потери точности (нет
// плавающей арифметики). country намеренно БЕЗ toString(...): ClickHouse
// сохраняет LowCardinality(String) в результате GROUP BY по
// LowCardinality-ключу даже через toString() (identity-оптимизация не
// снимает обёртку) — ch-go декодирует country как LowCardinality(String)
// напрямую (chgo.go), остальные драйверы декодируют это прозрачно в string.
const aggregateSQLTemplate = `
SELECT country, count() AS c, toInt64(sum(revenue) * 100) AS cents
FROM demo.%s
GROUP BY country
ORDER BY country
`

// triple — одна строка результата аналитического SELECT.
type triple struct {
	country string
	c       uint64
	cents   int64
}

// aggResult — свод результата аналитического SELECT для сравнения между
// драйверами: totalRows/totalCents — быстрая числовая проверка, checksum —
// CRC32-IEEE (совпадает с java.util.zip.CRC32 — тот же полином) от
// канонической строки "country|c|cents\n" по всем группам В ПОРЯДКЕ ORDER BY
// country — более строгая проверка, чем просто суммы (ловит расхождение по
// отдельным странам, даже если итоговые суммы случайно совпали).
type aggResult struct {
	groups     int
	totalRows  uint64
	totalCents int64
	checksum   uint32
}

func computeAgg(triples []triple) aggResult {
	var sb strings.Builder
	var totalRows uint64
	var totalCents int64
	for _, t := range triples {
		totalRows += t.c
		totalCents += t.cents
		fmt.Fprintf(&sb, "%s|%d|%d\n", t.country, t.c, t.cents)
	}
	return aggResult{
		groups:     len(triples),
		totalRows:  totalRows,
		totalCents: totalCents,
		checksum:   crc32.ChecksumIEEE([]byte(sb.String())),
	}
}

// assertAggEqual — сравнивает агрегат got с эталонным ref (первый успешно
// отработавший драйвер в этом прогоне), fail-loud при любом расхождении.
func assertAggEqual(name string, got, ref aggResult, expectRows uint64) {
	assertFailFast(got.totalRows == expectRows, "%s: aggregate totalRows (%d) == M (%d)", name, got.totalRows, expectRows)
	assertFailFast(got.groups == ref.groups, "%s: aggregate groups (%d) == reference groups (%d)", name, got.groups, ref.groups)
	assertFailFast(got.totalCents == ref.totalCents, "%s: aggregate totalCents (%d) == reference totalCents (%d)", name, got.totalCents, ref.totalCents)
	assertFailFast(got.checksum == ref.checksum, "%s: aggregate checksum (%08x) == reference checksum (%08x) — SELECT дал одинаковый результат", name, got.checksum, ref.checksum)
}
