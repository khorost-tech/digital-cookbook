//go:build flaky

package order

import "testing"

// shared — ОДИН экземпляр на весь пакет. Корень зла: тесты видят изменения
// друг друга, и результат зависит от порядка их запуска.
var shared = NewRegistry()

// FLAKY: этот тест «наследил» — добавил элемент в общий Registry и не убрал.
func TestShared_LeavesState(t *testing.T) {
	shared.Add("leaked")
	if shared.Count() < 1 {
		t.Fatal("ожидали хотя бы 1 элемент")
	}
}

// FLAKY: этот тест ждёт ПУСТОЙ общий Registry. Проходит, только если запущен
// ДО TestShared_LeavesState. При go test -shuffle=on порядок меняется — мигает.
// Запуск: go test -tags=flaky -shuffle=on -count=20 ./order/
func TestShared_ExpectsClean(t *testing.T) {
	if shared.Count() != 0 {
		t.Fatalf("общий Registry не пуст (Count=%d): другой тест наследил — порядко-зависимо", shared.Count())
	}
}
