package order

import "testing"

// ФИКС: каждый тест заводит СВОЙ Registry. Общего состояния нет — порядок
// выполнения не важен, -shuffle=on ничего не ломает.
func TestRegistry_IsolatedA(t *testing.T) {
	r := NewRegistry()
	r.Add("a")
	if r.Count() != 1 {
		t.Fatalf("Count = %d, want 1", r.Count())
	}
}

func TestRegistry_IsolatedB(t *testing.T) {
	r := NewRegistry()
	if r.Count() != 0 {
		t.Fatalf("свежий Registry не пуст: Count = %d", r.Count())
	}
	r.Add("b")
	r.Add("c")
	if r.Count() != 2 {
		t.Fatalf("Count = %d, want 2", r.Count())
	}
}

// Если состояние всё же общее (пул, коннект, temp-каталог) — чистим через
// t.Cleanup, чтобы каждый тест стартовал с известного состояния.
func TestRegistry_SharedButCleaned(t *testing.T) {
	r := NewRegistry()
	t.Cleanup(r.Reset) // гарантированная очистка после теста
	r.Add("x")
	if r.Count() != 1 {
		t.Fatalf("Count = %d, want 1", r.Count())
	}
}
