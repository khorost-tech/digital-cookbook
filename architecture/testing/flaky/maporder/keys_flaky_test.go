//go:build flaky

package maporder

import (
	"reflect"
	"testing"
)

// FLAKY: тест ждёт конкретный порядок ключей из RawKeys, но обход map в Go
// рандомизирован. Для двух ключей порядок [a b] или [b a] выпадает примерно
// 50/50 — тест мигает через раз.
// Запуск: go test -tags=flaky -count=50 ./maporder/
func TestRawKeys_FlakyOrder(t *testing.T) {
	m := map[string]int{"apple": 1, "banana": 2}
	got := RawKeys(m)
	want := []string{"apple", "banana"} // наивное ожидание «в порядке вставки/алфавита»
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RawKeys = %v, want %v — порядок обхода map недетерминирован", got, want)
	}
}
