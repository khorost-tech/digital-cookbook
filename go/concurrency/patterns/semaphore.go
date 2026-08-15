// semaphore.go демонстрирует ограничение параллелизма через взвешенный семафор
// golang.org/x/sync/semaphore.Weighted.
//
// Каждая задача занимает вес 1 через Acquire (блокируется, если свободного веса
// нет) и освобождает его через defer Release. Acquire уважает ctx: при отмене он
// вернёт ошибку, и мы прекращаем запуск новых задач. Так одновременно активны не
// более limit задач.
package patterns

import (
	"context"
	"sync"

	"golang.org/x/sync/semaphore"
)

// RunWithSemaphore обрабатывает inputs с ограничением limit одновременных задач
// через semaphore.Weighted. Возвращает результаты в порядке inputs и первую
// ошибку Acquire (например, при отмене ctx).
func RunWithSemaphore[T, R any](ctx context.Context, limit int64, inputs []T, fn func(T) R) ([]R, error) {
	out := make([]R, len(inputs))
	sem := semaphore.NewWeighted(limit)
	var wg sync.WaitGroup

	for i, in := range inputs {
		// Acquire блокируется до появления свободного веса либо отмены ctx.
		if err := sem.Acquire(ctx, 1); err != nil {
			wg.Wait() // дожидаемся уже запущенных, чтобы не оставить горутин.
			return out, err
		}
		wg.Add(1)
		go func(i int, in T) {
			defer wg.Done()
			defer sem.Release(1) // освобождаем вес в любом случае.
			out[i] = fn(in)      // своя ячейка — гонки по записи нет.
		}(i, in)
	}
	wg.Wait()
	return out, nil
}
