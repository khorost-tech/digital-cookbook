// worker_pool.go демонстрирует два способа ограничить параллелизм обработки.
//
// 1. WorkerPool — фиксированный пул из size долгоживущих воркеров, читающих из
//    общего канала задач. Результаты собираются в отдельный канал, который
//    закрывается один раз через sync.WaitGroup после завершения всех воркеров.
// 2. RunWithSlots — вариант «канал-семафор»: буферизованный канал ёмкости limit
//    выступает набором слотов. Занять слот — отправить в канал, освободить —
//    прочитать (через defer). Горутина на задачу, но одновременно активны не
//    более limit из них.
//
// Оба варианта закрывают владеемые каналы через defer и защищают отправки
// select с <-ctx.Done(), поэтому отмена не оставляет горутин.
package patterns

import (
	"context"
	"sync"
)

// WorkerPool обрабатывает jobs фиксированным пулом из size воркеров и возвращает
// канал результатов. Владелец (пул) закрывает канал результатов, когда все
// воркеры завершились. Отправитель jobs закрывает jobs сам — это сигнал к
// завершению воркеров.
func WorkerPool[T, R any](ctx context.Context, size int, jobs <-chan T, fn func(T) R) <-chan R {
	results := make(chan R)
	var wg sync.WaitGroup
	wg.Add(size)
	for i := 0; i < size; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return // канал задач закрыт — воркер завершается.
					}
					select {
					case <-ctx.Done():
						return
					case results <- fn(job):
					}
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results) // закрываем результаты ровно один раз.
	}()
	return results
}

// RunWithSlots запускает по горутине на каждый элемент inputs, но ограничивает
// число одновременно активных значением limit через буферизованный канал-семафор.
// Возвращает результаты в порядке inputs (каждая горутина пишет в свою ячейку).
func RunWithSlots[T, R any](ctx context.Context, limit int, inputs []T, fn func(T) R) []R {
	out := make([]R, len(inputs))
	slots := make(chan struct{}, limit) // ёмкость = максимум параллелизма.
	var wg sync.WaitGroup

	for i, in := range inputs {
		// Занимаем слот; при отмене прекращаем запуск новых горутин.
		select {
		case <-ctx.Done():
			wg.Wait()
			return out
		case slots <- struct{}{}:
		}
		wg.Add(1)
		go func(i int, in T) {
			defer wg.Done()
			defer func() { <-slots }() // освобождаем слот в любом случае.
			out[i] = fn(in)            // своя ячейка — гонки по записи нет.
		}(i, in)
	}
	wg.Wait()
	return out
}
