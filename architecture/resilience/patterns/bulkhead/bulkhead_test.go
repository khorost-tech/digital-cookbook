package bulkhead

import (
	"errors"
	"testing"
)

// TestIsolation — заполненный отсек не отбирает места у соседнего.
func TestIsolation(t *testing.T) {
	b := New(map[string]int{"heavy": 2, "light": 2})

	// Забиваем отсек heavy до отказа.
	r1, err := b.TryAcquire("heavy")
	mustOK(t, err)
	r2, err := b.TryAcquire("heavy")
	mustOK(t, err)
	if _, err := b.TryAcquire("heavy"); !errors.Is(err, ErrFull) {
		t.Fatalf("третий слот heavy: ждали ErrFull, получили %v", err)
	}

	// Отсек light не затронут — изоляция.
	r3, err := b.TryAcquire("light")
	if err != nil {
		t.Fatalf("light должен быть свободен несмотря на полный heavy, получили %v", err)
	}

	// Освобождение возвращает слот.
	r1()
	if _, err := b.TryAcquire("heavy"); err != nil {
		t.Fatalf("после release слот heavy должен освободиться, получили %v", err)
	}
	r2()
	r3()
}

// TestReleaseIdempotent — повторный release не крадёт чужой слот.
func TestReleaseIdempotent(t *testing.T) {
	b := New(map[string]int{"a": 1})
	rel, err := b.TryAcquire("a")
	mustOK(t, err)
	rel()
	rel() // повтор — не должен убрать чужой токен
	// Отсек свободен ровно на один слот.
	if _, err := b.TryAcquire("a"); err != nil {
		t.Fatalf("слот должен быть свободен, получили %v", err)
	}
	if b.InUse("a") != 1 {
		t.Fatalf("занято %d, ждали 1", b.InUse("a"))
	}
}

func TestUnknownPartition(t *testing.T) {
	b := New(map[string]int{"a": 1})
	if _, err := b.TryAcquire("nope"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("ждали ErrUnknown, получили %v", err)
	}
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
}
