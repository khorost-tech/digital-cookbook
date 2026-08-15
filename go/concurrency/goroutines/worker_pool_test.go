// worker_pool_test.go: корректность WorkerPool (все задачи обработаны ровно один
// раз) и отсутствие утечек при досрочной отмене контекста.
package goroutines

import (
	"context"
	"testing"
)

func TestWorkerPoolProcessesAllJobs(t *testing.T) {
	const n = 100
	jobs := make(chan Job[int])
	go func() {
		defer close(jobs)
		for i := range n {
			jobs <- Job[int]{Index: i, Value: i}
		}
	}()

	results := WorkerPool(context.Background(), 4, jobs,
		func(_ context.Context, v int) (int, error) {
			return v * 2, nil
		})

	seen := make(map[int]int) // index -> value
	for r := range results {
		if r.Err != nil {
			t.Fatalf("неожиданная ошибка: %v", r.Err)
		}
		if _, dup := seen[r.Index]; dup {
			t.Fatalf("индекс %d обработан повторно", r.Index)
		}
		seen[r.Index] = r.Value
	}

	if len(seen) != n {
		t.Fatalf("обработано %d задач, ожидалось %d", len(seen), n)
	}
	for i := range n {
		if seen[i] != i*2 {
			t.Fatalf("index %d: got %d, want %d", i, seen[i], i*2)
		}
	}
}

func TestWorkerPoolCancelNoLeak(t *testing.T) {
	// Бесконечный поток задач + отмена: воркеры не должны зависнуть ни на
	// приёме, ни на отправке. goleak (TestMain) проверит отсутствие утечек.
	jobs := make(chan Job[int])
	ctx, cancel := context.WithCancel(context.Background())

	producer := make(chan struct{})
	go func() {
		defer close(producer)
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return
			case jobs <- Job[int]{Index: i, Value: i}:
			}
		}
	}()

	results := WorkerPool(ctx, 3, jobs,
		func(_ context.Context, v int) (int, error) { return v, nil })

	// Прочитаем несколько результатов, затем отменим и дренируем до закрытия.
	for range 10 {
		<-results
	}
	cancel()
	for range results {
	}
	<-producer
}
