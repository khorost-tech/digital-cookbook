// batch_test.go проверяет сброс батчей по размеру и по времени, отдачу остатка
// при закрытии входа и отсутствие утечек горутин при досрочной отмене.
package patterns

import (
	"context"
	"testing"
	"time"
)

// TestBatchBySize: при maxSize=2 и большом maxWait поток из 5 элементов должен
// дать батчи [1,2], [3,4] по размеру и остаток [5] на закрытии входа.
func TestBatchBySize(t *testing.T) {
	ctx := context.Background()

	src := Source(ctx, 1, 2, 3, 4, 5)
	out := Batch(ctx, src, 2, time.Hour) // maxWait заведомо не сработает.

	var batches [][]int
	for b := range out {
		batches = append(batches, b)
	}

	want := [][]int{{1, 2}, {3, 4}, {5}}
	assertBatches(t, batches, want)
}

// TestBatchByTime: элементы приходят по одному реже, чем maxSize; сброс должен
// происходить по таймеру. Проверяем, что все элементы прошли ровно один раз.
func TestBatchByTime(t *testing.T) {
	ctx := context.Background()

	// Источник с задержкой между элементами, чтобы сработал таймер.
	in := make(chan int)
	go func() {
		defer close(in)
		for _, v := range []int{1, 2, 3} {
			select {
			case <-ctx.Done():
				return
			case in <- v:
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	out := Batch(ctx, in, 100, 5*time.Millisecond) // maxSize не достижим.

	var total []int
	for b := range out {
		if len(b) == 0 {
			t.Fatalf("пустой батч не должен доставляться")
		}
		total = append(total, b...)
	}

	want := []int{1, 2, 3}
	if len(total) != len(want) {
		t.Fatalf("всего элементов: got %d, want %d", len(total), len(want))
	}
	for i := range want {
		if total[i] != want[i] {
			t.Fatalf("элемент %d: got %d, want %d", i, total[i], want[i])
		}
	}
}

// TestBatchCancel: отмена ctx до вычитывания всех батчей не должна оставлять
// висящих горутин.
func TestBatchCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	src := Source(ctx, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	out := Batch(ctx, src, 2, time.Hour)

	<-out // читаем один батч.
	cancel()
	// Дренируем остаток, чтобы горутина вышла по <-ctx.Done().
	for range out {
	}
}

func assertBatches(t *testing.T, got, want [][]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("число батчей: got %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("батч %d длина: got %d, want %d", i, len(got[i]), len(want[i]))
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("батч %d элемент %d: got %d, want %d", i, j, got[i][j], want[i][j])
			}
		}
	}
}
