//go:build flaky

package async

import (
	"testing"
	"time"
)

// FLAKY: «синхронизация» через Sleep. Горутина кладёт результат после ~2 мс
// работы, а тест ждёт фиксированные 1 мс и читает — часто видит ещё не
// записанный ноль. Sleep — не синхронизация, а ставка на скорость машины: чуть
// изменится нагрузка (или margin окажется «безопасным», как в timedep) — и
// частота мигания поедет. (Заодно это гонка данных: чтение result без
// happens-before.)
// Запуск: go test -tags=flaky -count=200 ./async/
func TestCompute_FlakySleep(t *testing.T) {
	var result int
	go func() {
		time.Sleep(2 * time.Millisecond) // «медленная работа»
		result = 81
	}()

	time.Sleep(1 * time.Millisecond) // «наверное, уже готово» — нет гарантии
	if result != 81 {
		t.Fatalf("result = %d, want 81 — горутина не успела, Sleep не дождался", result)
	}
}
