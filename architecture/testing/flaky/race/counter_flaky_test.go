//go:build flaky

package race

import (
	"sync"
	"testing"
)

// FLAKY: небезопасный счётчик. 100 конкурентных Inc — гонка данных: часть
// обновлений теряется, итог < 100 и меняется от прогона к прогону.
// Запуск (значение): go test -tags=flaky -count=100 ./race/
// Запуск (детектор): go test -tags=flaky -race ./race/  (нужен C-компилятор)
func TestUnsafeCounter_FlakyLostUpdates(t *testing.T) {
	var c UnsafeCounter
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Inc() }()
	}
	wg.Wait()
	if c.Value() != 100 {
		t.Fatalf("UnsafeCounter = %d, want 100 — потеряны обновления (гонка)", c.Value())
	}
}
