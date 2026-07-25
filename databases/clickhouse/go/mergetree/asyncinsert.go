package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// Async inserts (Step 3 брифа): async_insert=1 включает серверную буферизацию
// мелких вставок — сервер копит присланные INSERT'ы в памяти и сбрасывает их
// одним куском (одним part'ом) либо по достижении async_insert_max_data_size,
// либо по истечении async_insert_busy_timeout_ms. Это прямая альтернатива
// клиентскому батчингу (Step 2) для случая, когда сам клиент не может
// собрать батч (много независимых мелких писателей).
//
// Синтетические строки (не из CSV): эта секция — про механизм буферизации
// сервером, а не про содержимое общего датасета; конкурентная генерация из
// одного CSV-читателя усложнила бы код без пользы для демонстрации.
func syntheticInsertSQL(table string) string {
	return fmt.Sprintf("INSERT INTO demo.%s %s VALUES (now(), ?, 'click', '/async-demo', 42, 'RU', 0)", table, insertColumns)
}

// concurrentAsyncInsert шлёt total однострочных INSERT конкурентно
// (concurrency горутин) с settings async_insert=1/wait_for_async_insert/
// async_insert_busy_timeout_ms — модель реального использования async
// insert: много независимых клиентов шлют мелкие вставки одновременно,
// сервер коалесит их в один флаш вместо part-на-каждую-вставку.
func concurrentAsyncInsert(ctx context.Context, conn clickhouse.Conn, table string, total, concurrency int, waitForAsync bool, busyTimeoutMs int) (time.Duration, error) {
	wait := 0
	if waitForAsync {
		wait = 1
	}
	settings := clickhouse.Settings{
		"async_insert":                 1,
		"wait_for_async_insert":        wait,
		"async_insert_busy_timeout_ms": busyTimeoutMs,
	}
	sql := syntheticInsertSQL(table)

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	errCh := make(chan error, total)

	start := time.Now()
	for i := 0; i < total; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func(userID int) {
			defer wg.Done()
			defer func() { <-sem }()
			qctx := clickhouse.Context(ctx, clickhouse.WithSettings(settings))
			if err := conn.Exec(qctx, sql, uint64(userID)); err != nil {
				errCh <- fmt.Errorf("async insert row %d: %w", userID, err)
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(errCh)
	for err := range errCh {
		if err != nil {
			return elapsed, err
		}
	}
	return elapsed, nil
}

// sequentialAsyncInsert — то же, но последовательно (для сценария видимости:
// нужен предсказуемый момент "все INSERT-вызовы отправлены", без гонки
// конкурентных горутин).
func sequentialAsyncInsert(ctx context.Context, conn clickhouse.Conn, table string, total int, waitForAsync bool, busyTimeoutMs int) (time.Duration, error) {
	wait := 0
	if waitForAsync {
		wait = 1
	}
	settings := clickhouse.Settings{
		"async_insert":                 1,
		"wait_for_async_insert":        wait,
		"async_insert_busy_timeout_ms": busyTimeoutMs,
	}
	sql := syntheticInsertSQL(table)

	start := time.Now()
	for i := 0; i < total; i++ {
		qctx := clickhouse.Context(ctx, clickhouse.WithSettings(settings))
		if err := conn.Exec(qctx, sql, uint64(i)); err != nil {
			return time.Since(start), fmt.Errorf("async insert row %d: %w", i, err)
		}
	}
	return time.Since(start), nil
}
