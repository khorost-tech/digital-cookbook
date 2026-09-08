// boundedqueue_test.go проверяет, что каждая политика переполнения ведёт себя
// как заявлено, счётчики accepted/dropped точны, а конкурентная нагрузка не
// оставляет утечек горутин (goleak в main_test.go).
package boundedqueue

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestReject: при полной очереди Reject мгновенно отклоняет и не блокирует.
func TestReject(t *testing.T) {
	ctx := context.Background()
	q := New[int](2, Reject)

	if !q.Offer(ctx, 1) || !q.Offer(ctx, 2) {
		t.Fatal("первые два Offer должны быть приняты")
	}
	// Очередь полна — третий отклоняется немедленно.
	if q.Offer(ctx, 3) {
		t.Fatal("Offer в полную очередь должен вернуть false")
	}
	if got, want := q.Accepted(), int64(2); got != want {
		t.Fatalf("accepted: got %d, want %d", got, want)
	}
	if got, want := q.Dropped(), int64(1); got != want {
		t.Fatalf("dropped: got %d, want %d", got, want)
	}
	if q.Len() != 2 {
		t.Fatalf("len: got %d, want 2", q.Len())
	}
}

// TestDropNewest: при переполнении отбрасывается именно новый элемент, а старые
// остаются в очереди в исходном порядке.
func TestDropNewest(t *testing.T) {
	ctx := context.Background()
	q := New[int](2, DropNewest)

	q.Offer(ctx, 1)
	q.Offer(ctx, 2)
	if q.Offer(ctx, 3) { // должен быть отброшен
		t.Fatal("DropNewest должен отбросить новый элемент")
	}
	// В очереди остаются 1 и 2.
	if v, _ := q.TryTake(); v != 1 {
		t.Fatalf("первый элемент: got %d, want 1", v)
	}
	if v, _ := q.TryTake(); v != 2 {
		t.Fatalf("второй элемент: got %d, want 2", v)
	}
	if q.Dropped() != 1 {
		t.Fatalf("dropped: got %d, want 1", q.Dropped())
	}
}

// TestDropOldest: при переполнении вытесняется самый старый элемент, новый
// принимается; в очереди остаётся «хвост» из последних cap элементов.
func TestDropOldest(t *testing.T) {
	ctx := context.Background()
	q := New[int](2, DropOldest)

	q.Offer(ctx, 1)
	q.Offer(ctx, 2)
	if !q.Offer(ctx, 3) { // вытесняет 1, принимает 3
		t.Fatal("DropOldest должен принять новый элемент")
	}
	// В очереди должны остаться 2 и 3.
	if v, _ := q.TryTake(); v != 2 {
		t.Fatalf("первый элемент: got %d, want 2", v)
	}
	if v, _ := q.TryTake(); v != 3 {
		t.Fatalf("второй элемент: got %d, want 3", v)
	}
	// accepted: 1,2,3 вошли = 3; dropped: вытеснена 1 = 1.
	if q.Accepted() != 3 {
		t.Fatalf("accepted: got %d, want 3", q.Accepted())
	}
	if q.Dropped() != 1 {
		t.Fatalf("dropped: got %d, want 1", q.Dropped())
	}
}

// TestBlockWaitsThenAccepts: политика Block заставляет отправителя ждать, пока
// получатель не освободит место. Проверяем без фиксированных пауз: отправитель
// стартует на полной очереди, разблокируется только после Take.
func TestBlockWaitsThenAccepts(t *testing.T) {
	ctx := context.Background()
	q := New[int](1, Block)

	if !q.Offer(ctx, 1) { // заполнили единственную ячейку
		t.Fatal("первый Offer должен пройти")
	}

	blocked := make(chan bool, 1)
	done := make(chan bool, 1)
	go func() {
		// Этот Offer обязан заблокироваться: место освободится только Take ниже.
		blocked <- true
		done <- q.Offer(ctx, 2)
	}()

	<-blocked
	// Убеждаемся, что отправитель действительно ждёт: пока не забрали элемент,
	// done не должен прийти. Небольшое окно на попытку проскочить.
	select {
	case <-done:
		t.Fatal("Offer не должен завершиться, пока очередь полна")
	case <-time.After(20 * time.Millisecond):
		// ожидаемо: отправитель заблокирован
	}

	// Освобождаем место — отправитель должен разблокироваться и принять элемент.
	if v, ok := q.Take(ctx); !ok || v != 1 {
		t.Fatalf("Take: got (%d,%v), want (1,true)", v, ok)
	}
	if accepted := <-done; !accepted {
		t.Fatal("после освобождения места Offer должен принять элемент")
	}
	if v, _ := q.TryTake(); v != 2 {
		t.Fatalf("в очереди должен быть элемент 2, got %d", v)
	}
}

// TestBlockCancel: отмена ctx разблокирует ждущий Offer и возвращает false, не
// оставляя зависших горутин.
func TestBlockCancel(t *testing.T) {
	q := New[int](1, Block)
	bg := context.Background()
	q.Offer(bg, 1) // заполнили очередь

	ctx, cancel := context.WithCancel(bg)
	done := make(chan bool, 1)
	go func() { done <- q.Offer(ctx, 2) }()

	cancel() // разблокирует ждущий Offer через ctx.Done()
	if accepted := <-done; accepted {
		t.Fatal("при отменённом ctx Offer должен вернуть false")
	}
	if q.Dropped() != 1 {
		t.Fatalf("dropped: got %d, want 1", q.Dropped())
	}
}

// TestTakeCancel: Take на пустой очереди уважает отмену ctx.
func TestTakeCancel(t *testing.T) {
	q := New[int](1, Block)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		_, ok := q.Take(ctx)
		done <- ok
	}()
	cancel()
	if ok := <-done; ok {
		t.Fatal("при отменённом ctx Take должен вернуть ok=false")
	}
}

// TestConcurrentCounters: под конкурентной нагрузкой сумма accepted+dropped
// равна числу вызовов Offer, а accepted не превышает суммарно принятого
// (проверка на гонки по счётчикам; запускать с -race в контейнере).
func TestConcurrentCounters(t *testing.T) {
	ctx := context.Background()
	q := New[int](8, Reject)

	const producers = 8
	const perProducer = 1000
	total := int64(producers * perProducer)

	// Потребитель забирает элементы, пока идёт нагрузка.
	stop := make(chan struct{})
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for {
			select {
			case <-stop:
				return
			default:
				q.TryTake()
			}
		}
	}()

	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				q.Offer(ctx, i)
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-consumerDone

	if got := q.Accepted() + q.Dropped(); got != total {
		t.Fatalf("accepted+dropped: got %d, want %d", got, total)
	}
}
