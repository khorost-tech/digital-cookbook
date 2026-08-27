package race

import (
	"sync"
	"testing"
)

// ФИКС: atomic-счётчик. 100 горутин по Inc — всегда ровно 100, при любом
// числе прогонов и под -race. Синхронизация встроена в тип.
func TestSafeCounter(t *testing.T) {
	var c SafeCounter
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Inc() }()
	}
	wg.Wait()
	if c.Value() != 100 {
		t.Fatalf("SafeCounter = %d, want 100", c.Value())
	}
}
