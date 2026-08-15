// Пакет synccookbook — примеры к статье #2 «Пакет sync и атомики»
// серии «Конкурентность в Go» (khorost.tech).
//
// mutex_vs_atomic.go — два способа безопасного инкремента счётчика:
// через sync.Mutex и через типизированный atomic.Int64 (Go 1.19+).
package synccookbook

import (
	"sync"
	"sync/atomic"
)

// MutexCounter — счётчик, защищённый мьютексом.
// Мьютекс сериализует любую работу в критической секции; удобен,
// когда под замком не одна операция, а несколько связанных.
type MutexCounter struct {
	mu    sync.Mutex
	value int64
}

// Inc увеличивает счётчик на единицу под замком.
func (c *MutexCounter) Inc() {
	c.mu.Lock()
	c.value++
	c.mu.Unlock()
}

// Value возвращает текущее значение (тоже под замком — чтение int64
// без синхронизации на некоторых архитектурах не атомарно).
func (c *MutexCounter) Value() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

// AtomicCounter — счётчик на atomic.Int64. Типизированные атомики
// (Go 1.19+) не дают случайно обратиться к полю мимо atomic-операций.
type AtomicCounter struct {
	value atomic.Int64
}

// Inc атомарно увеличивает счётчик; блокировок нет.
func (c *AtomicCounter) Inc() {
	c.value.Add(1)
}

// Value атомарно читает текущее значение.
func (c *AtomicCounter) Value() int64 {
	return c.value.Load()
}

// runConcurrent запускает workers горутин, каждая делает perWorker
// вызовов inc. Общая утилита для тестов и наглядности.
func runConcurrent(workers, perWorker int, inc func()) {
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				inc()
			}
		}()
	}
	wg.Wait()
}
