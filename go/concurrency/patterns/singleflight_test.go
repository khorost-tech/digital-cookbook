// singleflight_test.go проверяет схлопывание конкурентных одинаковых вызовов:
// N параллельных вызовов с одним ключом → fn выполнилась ровно один раз
// (счётчик через atomic). Отдельно проверяются DoChan и Forget.
package patterns

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"
)

func TestCoalesceSingleExecution(t *testing.T) {
	var g singleflight.Group
	var calls int32

	release := make(chan struct{})
	fn := func() (int, error) {
		atomic.AddInt32(&calls, 1)
		<-release // держим вызов in-flight, пока все N не присоединятся.
		return 42, nil
	}

	const n = 20
	var wg sync.WaitGroup
	var sharedCount int32
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			v, shared, err := Coalesce(&g, "key", fn)
			if err != nil {
				t.Errorf("неожиданная ошибка: %v", err)
				return
			}
			if v != 42 {
				t.Errorf("значение: got %d, want 42", v)
			}
			if shared {
				atomic.AddInt32(&sharedCount, 1)
			}
		}()
	}

	// Даём всем горутинам заблокироваться в Do на одном ключе, затем отпускаем.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("fn выполнилась %d раз, want 1", got)
	}
	if atomic.LoadInt32(&sharedCount) == 0 {
		t.Fatal("ожидались разделяемые (shared) результаты")
	}
}

func TestCoalesceChan(t *testing.T) {
	var g singleflight.Group
	ctx := context.Background()

	v, _, err := CoalesceChan(ctx, &g, "k", func() (string, error) {
		return "value", nil
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if v != "value" {
		t.Fatalf("значение: got %q, want %q", v, "value")
	}
}

// TestForget: после Forget новый вызов исполняет fn заново, а не присоединяется
// к запомненному in-flight вызову.
func TestForget(t *testing.T) {
	var g singleflight.Group
	var calls int32

	fn := func() (int, error) {
		atomic.AddInt32(&calls, 1)
		return 1, nil
	}

	if _, _, err := Coalesce(&g, "k", fn); err != nil {
		t.Fatalf("первый вызов: %v", err)
	}
	Forget(&g, "k")
	if _, _, err := Coalesce(&g, "k", fn); err != nil {
		t.Fatalf("второй вызов: %v", err)
	}

	// Оба вызова последовательны и вне окна схлопывания — fn исполнилась дважды.
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("fn выполнилась %d раз, want 2", got)
	}
}
