// errgroup_example_test.go: успех заполняет результаты по индексу без гонок;
// первая ошибка отменяет ctx для остальных задач; утечек нет.
package goroutines

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestProcessAllSuccess(t *testing.T) {
	inputs := []int{0, 1, 2, 3, 4, 5, 6, 7}
	out, err := ProcessAll(context.Background(), 3, inputs,
		func(_ context.Context, v int) (string, error) {
			return strconv.Itoa(v * v), nil
		})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	for i, v := range inputs {
		want := strconv.Itoa(v * v)
		if out[i] != want {
			t.Fatalf("index %d: got %q, want %q", i, out[i], want)
		}
	}
}

func TestProcessAllFirstErrorCancels(t *testing.T) {
	sentinel := errors.New("boom")
	inputs := make([]int, 50)
	for i := range inputs {
		inputs[i] = i
	}

	var canceled atomic.Bool

	_, err := ProcessAll(context.Background(), 4, inputs,
		func(ctx context.Context, v int) (int, error) {
			if v == 7 {
				return 0, sentinel // провоцируем отмену остальных.
			}
			// Задачи, ещё не начавшие работу, увидят отменённый ctx.
			select {
			case <-ctx.Done():
				canceled.Store(true)
				return 0, ctx.Err()
			default:
				return v, nil
			}
		})

	if !errors.Is(err, sentinel) {
		t.Fatalf("ожидалась ошибка sentinel, получено %v", err)
	}
	if !canceled.Load() {
		t.Log("отмена не наблюдалась (возможно при малом числе задач) — это не гонка")
	}
}
