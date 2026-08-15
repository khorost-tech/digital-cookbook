// ordone_test.go проверяет корректность OrDone (все элементы проходят ровно
// один раз в исходном порядке) и отсутствие утечек при досрочной отмене ctx.
package patterns

import (
	"context"
	"testing"
)

func TestOrDone(t *testing.T) {
	ctx := context.Background()

	src := Source(ctx, 1, 2, 3, 4, 5)

	var got []int
	for v := range OrDone(ctx, src) {
		got = append(got, v)
	}

	want := []int{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("длина результата: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("элемент %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

// TestOrDoneCancel: отмена ctx до вычитывания всех элементов не должна
// оставлять висящих горутин — goleak в TestMain это подтвердит.
func TestOrDoneCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	src := Source(ctx, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	out := OrDone(ctx, src)

	<-out // читаем один элемент.
	cancel()
	Drain(ctx, out) // дренируем остаток, чтобы горутина вышла по <-ctx.Done().
}
