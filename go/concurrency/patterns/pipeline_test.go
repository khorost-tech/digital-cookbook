// pipeline_test.go проверяет корректность конвейера и отсутствие утечек горутин
// при раннем выходе (отмена ctx до исчерпания источника).
package patterns

import (
	"context"
	"testing"
)

func TestPipeline(t *testing.T) {
	ctx := context.Background()

	src := Source(ctx, 1, 2, 3, 4, 5)
	squared := PipeStage(ctx, src, func(n int) int { return n * n })
	got := Drain(ctx, squared)

	want := []int{1, 4, 9, 16, 25}
	if len(got) != len(want) {
		t.Fatalf("длина результата: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("элемент %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

// TestPipelineCancel: отмена ctx до чтения всех элементов не должна оставлять
// висящих горутин — goleak в TestMain это подтвердит.
func TestPipelineCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	src := Source(ctx, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	doubled := PipeStage(ctx, src, func(n int) int { return n * 2 })

	// Читаем лишь первый элемент, затем отменяем — стадии должны завершиться.
	select {
	case <-doubled:
	case <-ctx.Done():
	}
	cancel()
	// Дренируем остаток, чтобы стадии гарантированно вышли по <-ctx.Done().
	Drain(ctx, doubled)
}
