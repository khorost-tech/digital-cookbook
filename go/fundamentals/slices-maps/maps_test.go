// maps_test.go — безопасные тесты поведения карт: чтение из nil-карты даёт
// нулевое значение, а запись в nil-карту паникует. Панику проверяем в
// изоляции через recover, поэтому тест ПРОХОДИТ.
package slicesmaps

import "testing"

// didPanic выполняет f и сообщает, была ли паника. recover глушит её, чтобы
// тест не падал: мы именно проверяем ФАКТ паники, а не даём ей завершить процесс.
func didPanic(f func()) (panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	f()
	return false
}

// TestReadFromNilMap: чтение из nil-карты легально и возвращает нулевое
// значение типа (для int — 0), а comma-ok сообщает об отсутствии ключа.
func TestReadFromNilMap(t *testing.T) {
	var m map[string]int // nil-карта

	if got := m["absent"]; got != 0 {
		t.Errorf("чтение из nil-карты дало %d, ожидали 0", got)
	}
	if v, ok := m["absent"]; ok || v != 0 {
		t.Errorf("comma-ok из nil-карты: v=%d ok=%v, ожидали 0, false", v, ok)
	}
	if len(m) != 0 {
		t.Errorf("len(nil-карты) = %d, ожидали 0", len(m))
	}
}

// TestWriteToNilMapPanics: запись в nil-карту паникует. Проверяем безопасно —
// панику ловит didPanic, тест проходит.
func TestWriteToNilMapPanics(t *testing.T) {
	var m map[string]int // nil-карта

	panicked := didPanic(func() {
		m["key"] = 1 // паника: assignment to entry in nil map
	})
	if !panicked {
		t.Error("ожидали панику при записи в nil-карту, её не было")
	}
}

// TestWriteToInitializedMap: после make запись работает без паники.
func TestWriteToInitializedMap(t *testing.T) {
	m := make(map[string]int) // инициализированная карта
	panicked := didPanic(func() {
		m["key"] = 1
	})
	if panicked {
		t.Error("запись в инициализированную карту не должна паниковать")
	}
	if m["key"] != 1 {
		t.Errorf("m[\"key\"] = %d, ожидали 1", m["key"])
	}
}
