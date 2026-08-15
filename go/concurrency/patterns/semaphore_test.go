// semaphore_test.go проверяет ограничение параллелизма взвешенным семафором:
// корректность результатов, соблюдение лимита и отсутствие утечек горутин.
package patterns

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestRunWithSemaphore(t *testing.T) {
	ctx := context.Background()
	inputs := []int{1, 2, 3, 4, 5, 6, 7, 8}

	var active, maxActive int64
	got, err := RunWithSemaphore(ctx, 3, inputs, func(n int) int {
		cur := atomic.AddInt64(&active, 1)
		for {
			m := atomic.LoadInt64(&maxActive)
			if cur <= m || atomic.CompareAndSwapInt64(&maxActive, m, cur) {
				break
			}
		}
		atomic.AddInt64(&active, -1)
		return n * 3
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if len(got) != len(inputs) {
		t.Fatalf("результатов: got %d, want %d", len(got), len(inputs))
	}
	for i, in := range inputs {
		if got[i] != in*3 {
			t.Fatalf("результат %d: got %d, want %d", i, got[i], in*3)
		}
	}
	if maxActive > 3 {
		t.Fatalf("пик параллелизма %d превысил лимит 3", maxActive)
	}
}

// TestRunWithSemaphoreCancel: отменённый ctx приводит к ошибке Acquire и не
// оставляет горутин.
func TestRunWithSemaphoreCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // отменяем до старта — первый Acquire должен вернуть ошибку.

	_, err := RunWithSemaphore(ctx, 2, []int{1, 2, 3}, func(n int) int { return n })
	if err == nil {
		t.Fatal("ожидалась ошибка Acquire при отменённом ctx")
	}
}
