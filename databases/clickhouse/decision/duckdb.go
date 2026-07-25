package main

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// duckdbConvertCSVToParquet — единственный "серверный" шаг DuckDB-варианта:
// сконвертировать общий CSV-датасет в Parquet ОДНИМ SQL-выражением внутри
// embedded-процесса (никакого отдельного сервера/контейнера — DuckDB здесь
// работает как БИБЛИОТЕКА внутри самого Go-бинаря decision, через CGO, а не
// как сетевой сервис). Время конвертации — DuckDB-аналог "load" у остальных
// трёх СУБД (подготовка постоянного артефакта на диске); в отличие от них
// это НЕ вставка в СУБД-резидентное хранилище, а перекодирование файла в
// колоночный Parquet.
func duckdbConvertCSVToParquet(csvPath, parquetPath string) (rows int64, elapsed time.Duration, err error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return 0, 0, fmt.Errorf("duckdb open: %w", err)
	}
	defer db.Close()

	start := time.Now()
	copySQL := fmt.Sprintf(`
COPY (
    SELECT event_time, user_id, event_type, url, duration_ms, country, revenue
    FROM read_csv(
        '%s',
        header = true,
        columns = {
            'event_time': 'TIMESTAMP',
            'user_id': 'UBIGINT',
            'event_type': 'VARCHAR',
            'url': 'VARCHAR',
            'duration_ms': 'UINTEGER',
            'country': 'VARCHAR',
            'revenue': 'DECIMAL(10,2)'
        }
    )
) TO '%s' (FORMAT PARQUET)`, csvPath, parquetPath)
	if _, err := db.Exec(copySQL); err != nil {
		return 0, time.Since(start), fmt.Errorf("duckdb copy csv->parquet: %w", err)
	}
	elapsed = time.Since(start)

	var cnt int64
	if err := db.QueryRow(fmt.Sprintf("SELECT count(*) FROM read_parquet('%s')", parquetPath)).Scan(&cnt); err != nil {
		return 0, elapsed, fmt.Errorf("duckdb count after convert: %w", err)
	}
	return cnt, elapsed, nil
}

func duckdbParquetFileSize(parquetPath string) (int64, error) {
	fi, err := os.Stat(parquetPath)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// duckdbAggregateSQLTemplate — тот же агрегат, что остальные три системы,
// выполненный НАПРЯМУЮ над файлом Parquet (read_parquet) — БЕЗ загрузки в
// какую-либо резидентную таблицу СУБД. Это и есть "embedded, ноль серверной
// эксплуатации" из Step 3 брифа: одна SQL-строка, один процесс, файл на
// диске.
const duckdbAggregateSQLTemplate = `
SELECT
    country,
    CAST(event_time AS DATE) AS d,
    count(*) AS cnt,
    CAST(ROUND(SUM(revenue) * 100) AS BIGINT) AS cents
FROM read_parquet('%s')
GROUP BY country, d
ORDER BY country, d
`

func runDuckDBAggregate(parquetPath string) (aggResult, checksumResult, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return aggResult{}, checksumResult{}, fmt.Errorf("duckdb open: %w", err)
	}
	defer db.Close()

	start := time.Now()
	rows, err := db.Query(fmt.Sprintf(duckdbAggregateSQLTemplate, parquetPath))
	if err != nil {
		return aggResult{}, checksumResult{}, fmt.Errorf("duckdb aggregate query: %w", err)
	}
	defer rows.Close()

	cs := newChecksum()
	for rows.Next() {
		var country string
		var d time.Time
		var cnt int64
		var cents int64
		if err := rows.Scan(&country, &d, &cnt, &cents); err != nil {
			return aggResult{}, checksumResult{}, fmt.Errorf("duckdb aggregate scan: %w", err)
		}
		cs.add(country, d, cnt, cents)
	}
	if err := rows.Err(); err != nil {
		return aggResult{}, checksumResult{}, fmt.Errorf("duckdb aggregate rows err: %w", err)
	}
	return aggResult{duration: time.Since(start)}, cs.result(), nil
}

func runDuckDBAggregateMultiple(n int, parquetPath string) (time.Duration, checksumResult, []time.Duration, error) {
	return runMultiple(n, func() (aggResult, checksumResult, error) {
		return runDuckDBAggregate(parquetPath)
	})
}
