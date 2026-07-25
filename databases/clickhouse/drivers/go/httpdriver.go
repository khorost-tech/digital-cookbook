package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// httpInsert — "нулевой" драйвер: сырой POST на HTTP-интерфейс ClickHouse
// (net/http, без единой ClickHouse-библиотеки) — CSV-файл датасета
// стримится ПРЯМО в тело запроса (INSERT ... FORMAT CSVWithNames), сервер
// сам парсит CSV. Показывает, что для INSERT/SELECT не обязателен ни один
// специализированный клиент — обычный HTTP POST/GET работает "из коробки"
// (проксирование, балансировщик, curl — Step 3 брифа), ценой (см. README)
// отсутствия батч-протокола/бинарного кодирования нативного клиента.
func httpInsert(ctx context.Context, baseURL, table, csvPath string, expectRows uint64) (rows uint64, elapsed time.Duration, err error) {
	f, err := os.Open(csvPath)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	q := fmt.Sprintf("INSERT INTO demo.%s FORMAT CSVWithNames", table)
	u := strings.TrimRight(baseURL, "/") + "/?query=" + url.QueryEscape(q)

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, f)
	if err != nil {
		return 0, 0, fmt.Errorf("http insert: build request: %w", err)
	}
	req.Header.Set("Content-Type", "text/csv")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, time.Since(start), fmt.Errorf("http insert: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	elapsed = time.Since(start)
	if resp.StatusCode != http.StatusOK {
		return 0, elapsed, fmt.Errorf("http insert: status %d: %s", resp.StatusCode, string(body))
	}
	// HTTP-INSERT либо целиком успешен (весь CSV принят сервером), либо
	// вернул ошибку выше — фактическое число строк перепроверяется
	// авторитетно через system.parts (main.go, tableRowsAdmin), не здесь.
	return expectRows, elapsed, nil
}

// httpSelect — тот же аналитический SELECT (aggregate.go), POST SQL-текста
// как тела запроса (стандартная ClickHouse HTTP-конвенция), разбор ответа
// FORMAT TabSeparated построчным split по табу — минимальный клиент без
// зависимостей.
func httpSelect(ctx context.Context, baseURL, table string) (aggResult, time.Duration, error) {
	sqlQuery := fmt.Sprintf(aggregateSQLTemplate, table) + "\nFORMAT TabSeparated"
	u := strings.TrimRight(baseURL, "/") + "/"

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(sqlQuery))
	if err != nil {
		return aggResult{}, 0, fmt.Errorf("http select: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return aggResult{}, time.Since(start), fmt.Errorf("http select: %w", err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	dur := time.Since(start)
	if readErr != nil {
		return aggResult{}, dur, fmt.Errorf("http select: read body: %w", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		return aggResult{}, dur, fmt.Errorf("http select: status %d: %s", resp.StatusCode, string(body))
	}

	var triples []triple
	for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			return aggResult{}, dur, fmt.Errorf("http select: unexpected TSV line %q", line)
		}
		c, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return aggResult{}, dur, fmt.Errorf("http select: parse c: %w", err)
		}
		cents, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return aggResult{}, dur, fmt.Errorf("http select: parse cents: %w", err)
		}
		triples = append(triples, triple{country: parts[0], c: c, cents: cents})
	}
	return computeAgg(triples), dur, nil
}
