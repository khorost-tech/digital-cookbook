package probe

import (
	"errors"
	"testing"
)

// errBoom — единственный потребитель; живёт в тесте, а не в probe.go
// (минорная находка ревью: непроизводственный код не должен жить
// рядом с реализацией только потому, что раньше так было проще
// писать).
var errBoom = errors.New("проба провалилась")

// Классификация — сердце стенда. Три исхода вместо двух: отказ и
// «прочиталось, но не то» — разные вещи, и путать их нельзя.
func TestClassify(t *testing.T) {
	want := map[string]any{"id": int64(1), "name": "Анна"}

	if got := Classify(want, want, nil); got != "ok" {
		t.Fatalf("совпадение должно быть ok, получено %q", got)
	}
	if got := Classify(nil, want, errBoom); got != "refused" {
		t.Fatalf("ошибка должна быть refused, получено %q", got)
	}
	other := map[string]any{"id": int64(1), "name": "Борис"}
	if got := Classify(other, want, nil); got != "wrong" {
		t.Fatalf("расхождение без ошибки должно быть wrong, получено %q", got)
	}
}
