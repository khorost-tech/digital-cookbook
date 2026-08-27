package maporder

import (
	"reflect"
	"testing"
)

// ФИКС: сравниваем отсортированные ключи — детерминированно при любом прогоне.
func TestSortedKeys_Deterministic(t *testing.T) {
	m := map[string]int{"banana": 1, "apple": 2, "cherry": 3}
	got := SortedKeys(m)
	want := []string{"apple", "banana", "cherry"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SortedKeys = %v, want %v", got, want)
	}
}
