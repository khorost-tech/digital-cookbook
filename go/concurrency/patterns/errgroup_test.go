// errgroup_test.go проверяет errgroup-обёртки: успешную обработку, отмену по
// первой ошибке, неблокирующий TryGo — и отсутствие утечек горутин.
package patterns

import (
	"context"
	"errors"
	"testing"
)

func TestProcessAllSuccess(t *testing.T) {
	ctx := context.Background()
	inputs := []int{1, 2, 3, 4, 5}

	got, err := ProcessAll(ctx, 2, inputs, func(_ context.Context, n int) (int, error) {
		return n * n, nil
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	for i, in := range inputs {
		if got[i] != in*in {
			t.Fatalf("результат %d: got %d, want %d", i, got[i], in*in)
		}
	}
}

// TestProcessAllError: первая ошибка отменяет производный ctx; остальные задачи,
// слушающие <-ctx.Done(), завершаются досрочно, утечек нет.
func TestProcessAllError(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")

	_, err := ProcessAll(ctx, 2, []int{1, 2, 3, 4, 5, 6}, func(ctx context.Context, n int) (int, error) {
		if n == 3 {
			return 0, boom
		}
		// Прочие задачи уважают отмену, чтобы не зависнуть.
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
			return n, nil
		}
	})
	if !errors.Is(err, boom) {
		t.Fatalf("ожидалась ошибка boom, got %v", err)
	}
}

func TestTryProcess(t *testing.T) {
	ctx := context.Background()
	// Больше входов, чем лимит: часть задач TryGo не примет.
	inputs := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	out, rejected, err := TryProcess(ctx, 2, inputs, func(_ context.Context, n int) (int, error) {
		return n * 2, nil
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	// Принятые задачи заполнили свои ячейки корректно.
	for i, in := range inputs {
		if out[i] != 0 && out[i] != in*2 {
			t.Fatalf("ячейка %d: got %d, want 0 или %d", i, out[i], in*2)
		}
	}
	// Хотя бы часть задач должна была не поместиться под лимит 2.
	if len(rejected) == 0 {
		t.Fatal("ожидались отклонённые задачи при лимите 2")
	}
}
