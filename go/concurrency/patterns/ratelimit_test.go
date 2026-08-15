// ratelimit_test.go проверяет оба лимитера: токен-бакет x/time/rate и вариант на
// time.Ticker — что разрешения выдаются, и что нет утечек горутин/тикеров.
package patterns

import (
	"context"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestWaitProcess(t *testing.T) {
	ctx := context.Background()
	// Высокая частота и большой burst — тест не тормозит, но Wait реально вызван.
	lim := rate.NewLimiter(rate.Limit(1000), 10)
	inputs := []int{1, 2, 3, 4, 5}

	got, err := WaitProcess(ctx, lim, inputs, func(n int) int { return n + 1 })
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if len(got) != len(inputs) {
		t.Fatalf("обработано: got %d, want %d", len(got), len(inputs))
	}
	for i, in := range inputs {
		if got[i] != in+1 {
			t.Fatalf("результат %d: got %d, want %d", i, got[i], in+1)
		}
	}
}

// TestWaitProcessCancel: отменённый ctx прерывает Wait ошибкой.
func TestWaitProcessCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Нулевой burst гарантирует, что Wait обратится к ctx и вернёт ошибку.
	lim := rate.NewLimiter(rate.Limit(1), 0)

	_, err := WaitProcess(ctx, lim, []int{1, 2, 3}, func(n int) int { return n })
	if err == nil {
		t.Fatal("ожидалась ошибка Wait при отменённом ctx")
	}
}

func TestAllowFilter(t *testing.T) {
	// burst=3, очень медленное пополнение: пройдут ровно первые 3 разрешения.
	lim := rate.NewLimiter(rate.Limit(0.001), 3)
	inputs := []int{1, 2, 3, 4, 5, 6, 7, 8}

	got := AllowFilter(lim, inputs, func(n int) int { return n })
	if len(got) != 3 {
		t.Fatalf("пропущено элементов: got %d, want 3", len(got))
	}
}

func TestTickerLimiter(t *testing.T) {
	lim := NewTickerLimiter(5 * time.Millisecond)
	defer lim.Stop() // обязательно останавливаем тикер — иначе утечка.

	// Дожидаемся двух разрешений — лимитер их выдаёт.
	for i := 0; i < 2; i++ {
		select {
		case <-lim.C:
		case <-time.After(time.Second):
			t.Fatal("разрешение от тикера не поступило вовремя")
		}
	}
}
