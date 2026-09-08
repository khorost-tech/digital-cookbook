// loadshedding_test.go проверяет: под перегрузом доля отклонений растёт, все
// принятые запросы обрабатываются, а сглаживатель латентности EWMA ведёт себя
// как заявлено. Тесты детерминированы (без пауз) и не оставляют утечек горутин.
package loadshedding

import (
	"math"
	"sync"
	"testing"
)

// TestSubmitShedsWhenFull: без запущенных воркеров очередь заполняется до
// ёмкости, после чего Submit отклоняет — доля reject строго положительна.
func TestSubmitShedsWhenFull(t *testing.T) {
	const cap = 4
	const total = 10
	s := New(Config{Capacity: cap, Workers: 1})
	// Воркеров не запускаем: очередь никто не разбирает, значит заполнится ровно
	// до ёмкости, а остальное будет отклонено — поведение приёма детерминировано.

	noop := func() {}
	for i := 0; i < total; i++ {
		s.Submit(noop)
	}

	if got, want := s.Accepted(), int64(cap); got != want {
		t.Fatalf("accepted: got %d, want %d", got, want)
	}
	if got, want := s.Rejected(), int64(total-cap); got != want {
		t.Fatalf("rejected: got %d, want %d", got, want)
	}
	if !s.Saturated() {
		t.Fatal("полная очередь должна давать Saturated()==true")
	}

	// Разбираем очередь напрямую (воркеров нет) и проверяем, что сигнал
	// насыщения спадает после освобождения места.
drain:
	for {
		select {
		case <-s.queue:
		default:
			break drain
		}
	}
	if s.Saturated() {
		t.Fatal("после освобождения очереди Saturated() должно быть false")
	}
}

// TestAllAcceptedAreProcessed: при остановке Shedder дренирует очередь, поэтому
// число обработанных запросов равно числу принятых.
func TestAllAcceptedAreProcessed(t *testing.T) {
	const cap = 16
	const submit = 12 // <= cap, чтобы ни один не был отклонён
	s := New(Config{Capacity: cap, Workers: 3})
	s.Start()

	var mu sync.Mutex
	seen := make(map[int]bool)
	for i := 0; i < submit; i++ {
		id := i
		if !s.Submit(func() {
			mu.Lock()
			seen[id] = true
			mu.Unlock()
		}) {
			t.Fatalf("запрос %d не должен быть отклонён (очередь не полна)", id)
		}
	}

	s.Stop() // дренирует очередь и ждёт воркеров

	if got, want := s.Processed(), int64(submit); got != want {
		t.Fatalf("processed: got %d, want %d", got, want)
	}
	if s.Processed() != s.Accepted() {
		t.Fatalf("processed(%d) должно равняться accepted(%d)", s.Processed(), s.Accepted())
	}
	if len(seen) != submit {
		t.Fatalf("уникальных обработанных: got %d, want %d", len(seen), submit)
	}
}

// TestRejectShareGrowsUnderOverload: чем сильнее перегруз (входящих больше
// ёмкости при остановленном разборе), тем выше доля отклонений. Сравниваем два
// уровня нагрузки — доля reject монотонно растёт.
func TestRejectShareGrowsUnderOverload(t *testing.T) {
	share := func(load int) float64 {
		s := New(Config{Capacity: 4, Workers: 1})
		for i := 0; i < load; i++ {
			s.Submit(func() {})
		}
		return float64(s.Rejected()) / float64(load)
	}
	low := share(8)   // 4 приняты, 4 отклонены => 0.5
	high := share(40) // 4 приняты, 36 отклонены => 0.9
	if !(high > low) {
		t.Fatalf("доля reject должна расти с нагрузкой: low=%.3f high=%.3f", low, high)
	}
}

// TestEWMAConverges: при постоянном замере EWMA сходится к нему; при ступеньке
// значение сдвигается в сторону нового замера, но не перепрыгивает его.
func TestEWMAConverges(t *testing.T) {
	e := NewEWMA(0.3)
	for i := 0; i < 200; i++ {
		e.Update(100)
	}
	if math.Abs(e.Value()-100) > 1e-6 {
		t.Fatalf("EWMA не сошлось к 100: got %.6f", e.Value())
	}
	// Ступенька вверх: одно значение 200 должно поднять среднее, но не до 200.
	v := e.Update(200)
	if !(v > 100 && v < 200) {
		t.Fatalf("после ступеньки EWMA=%.3f, ожидалось (100,200)", v)
	}
}
