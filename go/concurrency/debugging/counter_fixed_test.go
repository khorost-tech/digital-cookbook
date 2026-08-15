// counter_fixed_test.go: атомарный счётчик даёт детерминированный итог и чист
// под детектором гонок.
//
// Прогон обычный:      go test ./debugging/...
// Прогон под гонками:  go test -race ./debugging/...   (в контейнере с CGO)
// Под -race предупреждений DATA RACE быть не должно, итог = ожидаемому.
package debugging

import "testing"

func TestConcurrentCountIsDeterministic(t *testing.T) {
	const goroutines = 100
	const perGoroutine = 100

	got := ConcurrentCount(goroutines, perGoroutine)
	want := int64(goroutines * perGoroutine)

	if got != want {
		t.Fatalf("итог счётчика = %d, ожидалось %d", got, want)
	}
}
