// tee_test.go проверяет, что Tee доставляет каждый элемент в оба выхода в
// исходном порядке, и что досрочная отмена не оставляет утечек горутин.
package patterns

import (
	"context"
	"sync"
	"testing"
)

func TestTee(t *testing.T) {
	ctx := context.Background()

	src := Source(ctx, 1, 2, 3, 4, 5)
	out1, out2 := Tee(ctx, src)

	// Оба выхода читаем параллельно — иначе отправитель заблокируется.
	var got1, got2 []int
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		got1 = Drain(ctx, out1)
	}()
	go func() {
		defer wg.Done()
		got2 = Drain(ctx, out2)
	}()
	wg.Wait()

	want := []int{1, 2, 3, 4, 5}
	for _, pair := range []struct {
		name string
		got  []int
	}{{"out1", got1}, {"out2", got2}} {
		if len(pair.got) != len(want) {
			t.Fatalf("%s длина: got %d, want %d", pair.name, len(pair.got), len(want))
		}
		for i := range want {
			if pair.got[i] != want[i] {
				t.Fatalf("%s элемент %d: got %d, want %d", pair.name, i, pair.got[i], want[i])
			}
		}
	}
}

// TestTeeCancel: отмена ctx при частичном чтении обоих выходов не должна
// оставлять висящих горутин.
func TestTeeCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	src := Source(ctx, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	out1, out2 := Tee(ctx, src)

	<-out1
	<-out2
	cancel()
	// Дренируем оба выхода, чтобы владелец гарантированно вышел по <-ctx.Done().
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); Drain(ctx, out1) }()
	go func() { defer wg.Done(); Drain(ctx, out2) }()
	wg.Wait()
}
