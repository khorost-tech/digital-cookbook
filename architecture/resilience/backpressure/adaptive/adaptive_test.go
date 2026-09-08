// adaptive_test.go проверяет поведение AIMD-лимитера детерминированно: без
// реального времени и пауз мы подаём синтетические замеры латентности через
// Release и наблюдаем, как лимит снижается на «медленной» зависимости и
// восстанавливается на «быстрой». Отдельно проверяем блокировку под лимитом,
// отмену ctx и отсутствие утечек горутин.
package adaptive

import (
	"context"
	"testing"
	"time"
)

func cfg() Config {
	return Config{
		Min:       1,
		Max:       20,
		Initial:   10,
		Threshold: 100 * time.Millisecond,
		Increase:  1,
		Decrease:  0.5,
	}
}

// feed выполняет n пар Acquire/Release с фиксированным замером sample. Слот
// каждый раз освобождается сразу, поэтому Acquire не блокирует, а лимит
// эволюционирует детерминированно по одним и тем же замерам.
func feed(t *testing.T, l *Limiter, n int, sample time.Duration) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := l.Acquire(ctx); err != nil {
			t.Fatalf("Acquire вернул ошибку: %v", err)
		}
		l.Release(sample)
	}
}

// TestSlowDependencyShrinksLimit: замеры выше порога загоняют лимит к минимуму.
func TestSlowDependencyShrinksLimit(t *testing.T) {
	l := New(cfg())
	start := l.Limit()
	feed(t, l, 20, 500*time.Millisecond) // выше порога 100ms
	got := l.Limit()
	if got >= start {
		t.Fatalf("лимит должен снизиться: было %d, стало %d", start, got)
	}
	if got != cfg().Min {
		t.Fatalf("на устойчиво медленной зависимости лимит должен дойти до Min=%d, got %d", cfg().Min, got)
	}
}

// TestFastDependencyRestoresLimit: после снижения быстрые замеры возвращают
// лимит к максимуму.
func TestFastDependencyRestoresLimit(t *testing.T) {
	l := New(cfg())
	feed(t, l, 20, 500*time.Millisecond) // сначала загнали вниз
	if l.Limit() != cfg().Min {
		t.Fatalf("подготовка: лимит должен быть Min, got %d", l.Limit())
	}
	feed(t, l, 100, 1*time.Millisecond) // норма — аддитивный рост
	if got := l.Limit(); got != cfg().Max {
		t.Fatalf("на быстрой зависимости лимит должен вырасти до Max=%d, got %d", cfg().Max, got)
	}
}

// TestClampBounds: лимит не выходит за [Min, Max] при любых замерах.
func TestClampBounds(t *testing.T) {
	l := New(cfg())
	feed(t, l, 1000, 1*time.Millisecond) // давим вверх
	if l.Limit() != cfg().Max {
		t.Fatalf("лимит должен упереться в Max, got %d", l.Limit())
	}
	feed(t, l, 1000, time.Second) // давим вниз
	if l.Limit() != cfg().Min {
		t.Fatalf("лимит должен упереться в Min, got %d", l.Limit())
	}
}

// TestAcquireBlocksAtLimitAndCancel: при исчерпанном лимите Acquire блокируется,
// а отмена ctx возвращает ошибку и не оставляет зависших горутин.
func TestAcquireBlocksAtLimitAndCancel(t *testing.T) {
	// Фиксируем лимит на 1: Min=Max=1, порог большой, чтобы норма не растила.
	l := New(Config{Min: 1, Max: 1, Initial: 1, Threshold: time.Hour, Increase: 1, Decrease: 0.5})
	bg := context.Background()

	if err := l.Acquire(bg); err != nil { // занимаем единственный слот
		t.Fatalf("первый Acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() { done <- l.Acquire(ctx) }() // должен заблокироваться

	cancel() // разблокирует ждущего через ctx.Done()
	if err := <-done; err == nil {
		t.Fatal("Acquire под исчерпанным лимитом при отмене ctx должен вернуть ошибку")
	}

	// Слот всё ещё занят первым Acquire — освобождаем, чтобы состояние было чистым.
	l.Release(1 * time.Millisecond)
	if l.InFlight() != 0 {
		t.Fatalf("после Release inFlight должно быть 0, got %d", l.InFlight())
	}
}

// TestAcquireUnblocksOnRelease: ждущий Acquire получает слот, когда его
// освобождает Release (проверка пути пробуждения из очереди).
func TestAcquireUnblocksOnRelease(t *testing.T) {
	l := New(Config{Min: 1, Max: 1, Initial: 1, Threshold: time.Hour, Increase: 1, Decrease: 0.5})
	bg := context.Background()

	if err := l.Acquire(bg); err != nil {
		t.Fatalf("первый Acquire: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- l.Acquire(bg) }() // ждёт освобождения слота

	l.Release(1 * time.Millisecond) // будит ждущего
	if err := <-done; err != nil {
		t.Fatalf("второй Acquire должен пройти после Release, got %v", err)
	}
	l.Release(1 * time.Millisecond)
}
