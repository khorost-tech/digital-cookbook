// counter_fixed.go: ИСПРАВЛЕННАЯ версия счётчика из cmd/race-counter.
//
// Незащищённый counter++ заменён на atomic.Int64.Add(1). Атомарная операция
// устанавливает happens-before между инкрементами из разных горутин: чтение,
// прибавление и запись выполняются как единое неделимое действие, потерянных
// инкрементов нет. Детектор гонок (-race) на этой версии молчит, а итог всегда
// равен ожидаемому — что и проверяет counter_fixed_test.go.
package debugging

import (
	"sync"
	"sync/atomic"
)

// ConcurrentCount запускает goroutines горутин, каждая из которых делает
// perGoroutine атомарных инкрементов, и возвращает итоговое значение счётчика.
// Результат детерминирован: goroutines*perGoroutine.
func ConcurrentCount(goroutines, perGoroutine int) int64 {
	var counter atomic.Int64
	var wg sync.WaitGroup

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perGoroutine {
				counter.Add(1) // атомарный инкремент — без гонки
			}
		}()
	}
	wg.Wait()

	return counter.Load()
}
