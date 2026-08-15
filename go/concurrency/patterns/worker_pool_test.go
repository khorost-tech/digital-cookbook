// worker_pool_test.go проверяет оба варианта пула: фиксированный WorkerPool и
// «канал-семафор» RunWithSlots — корректность и отсутствие утечек горутин.
package patterns

import (
	"context"
	"sort"
	"sync/atomic"
	"testing"
)

func TestWorkerPool(t *testing.T) {
	ctx := context.Background()

	jobs := Source(ctx, 1, 2, 3, 4, 5, 6, 7, 8)
	results := WorkerPool(ctx, 3, jobs, func(n int) int { return n + 100 })
	got := Drain(ctx, results)

	if len(got) != 8 {
		t.Fatalf("обработано задач: got %d, want 8", len(got))
	}
	sort.Ints(got)
	for i, v := range got {
		want := (i + 1) + 100
		if v != want {
			t.Fatalf("результат %d: got %d, want %d", i, v, want)
		}
	}
}

func TestRunWithSlots(t *testing.T) {
	ctx := context.Background()
	inputs := []int{1, 2, 3, 4, 5, 6, 7}

	var active, maxActive int64
	got := RunWithSlots(ctx, 2, inputs, func(n int) int {
		cur := atomic.AddInt64(&active, 1)
		// Отслеживаем пик параллелизма без гонок (CAS-цикл).
		for {
			m := atomic.LoadInt64(&maxActive)
			if cur <= m || atomic.CompareAndSwapInt64(&maxActive, m, cur) {
				break
			}
		}
		atomic.AddInt64(&active, -1)
		return n * n
	})

	if len(got) != len(inputs) {
		t.Fatalf("результатов: got %d, want %d", len(got), len(inputs))
	}
	for i, in := range inputs {
		if got[i] != in*in {
			t.Fatalf("результат %d: got %d, want %d", i, got[i], in*in)
		}
	}
	if maxActive > 2 {
		t.Fatalf("пик параллелизма %d превысил лимит 2", maxActive)
	}
}
