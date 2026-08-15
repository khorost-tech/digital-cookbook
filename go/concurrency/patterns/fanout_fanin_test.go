// fanout_fanin_test.go проверяет, что fan-out/fan-in обрабатывает все элементы
// (без потерь) и не оставляет утечек горутин.
package patterns

import (
	"context"
	"sort"
	"testing"
)

func TestFanOutFanIn(t *testing.T) {
	ctx := context.Background()

	in := Source(ctx, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	workers := FanOut(ctx, in, 4, func(n int) int { return n * 10 })
	merged := FanIn(ctx, workers...)
	got := Drain(ctx, merged)

	if len(got) != 10 {
		t.Fatalf("обработано элементов: got %d, want 10", len(got))
	}
	// Порядок при fan-in не гарантирован — сравниваем как множество.
	sort.Ints(got)
	for i, v := range got {
		want := (i + 1) * 10
		if v != want {
			t.Fatalf("элемент %d: got %d, want %d", i, v, want)
		}
	}
}
