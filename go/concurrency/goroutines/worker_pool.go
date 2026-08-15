// Package goroutines собирает канонически корректные, свободные от утечек
// базовые паттерны конкурентности из статьи #1 «Горутины и каналы».
//
// worker_pool.go демонстрирует worker pool с фиксированным числом воркеров,
// где select защищает и приём задачи, и отправку результата (case <-ctx.Done()
// с обеих сторон) — это «полностью защищённый» вариант. При отмене контекста
// ни один воркер не зависает ни на чтении из jobs, ни на записи в results.
package goroutines

import (
	"context"
	"sync"
)

// Job — единица работы для пула.
type Job[T any] struct {
	Index int
	Value T
}

// Result — результат обработки одной задачи.
type Result[R any] struct {
	Index int
	Value R
	Err   error
}

// WorkerPool запускает workers горутин, читающих задачи из jobs и пишущих
// результаты в возвращаемый канал. Функция fn обрабатывает одну задачу.
//
// Гарантии:
//   - Единственный владелец канала результатов (эта функция через отдельную
//     горутину-закрывателя) закрывает его ровно один раз после того, как все
//     воркеры завершились (WaitGroup).
//   - Каждый select защищён <-ctx.Done() и на приёме, и на отправке, поэтому
//     при отмене контекста воркеры не блокируются и корректно выходят.
//   - Вызывающий обязан либо дочитать канал результатов до закрытия, либо
//     отменить ctx; при отмене отправка результата не заблокирует воркер.
func WorkerPool[T, R any](
	ctx context.Context,
	workers int,
	jobs <-chan Job[T],
	fn func(context.Context, T) (R, error),
) <-chan Result[R] {
	if workers < 1 {
		workers = 1
	}
	results := make(chan Result[R])

	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for {
				// Защищённый приём задачи: отмена контекста прерывает ожидание.
				var job Job[T]
				select {
				case <-ctx.Done():
					return
				case j, ok := <-jobs:
					if !ok {
						return // входной канал закрыт — штатное завершение.
					}
					job = j
				}

				value, err := fn(ctx, job.Value)

				// Защищённая отправка результата: если потребитель ушёл и ctx
				// отменён, воркер не зависнет на записи в results.
				select {
				case <-ctx.Done():
					return
				case results <- Result[R]{Index: job.Index, Value: value, Err: err}:
				}
			}
		}()
	}

	// Единственный владелец закрывает канал результатов после всех воркеров.
	go func() {
		wg.Wait()
		close(results)
	}()

	return results
}
