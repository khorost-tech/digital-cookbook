package probe

import "testing"

// Круг правок 5. Равенство значений было определено только примером
// («целое 1 и строка "1" не равны») и держалось на сравнении, которое
// умеет вторая реализация вовсе не так же: в другом языке два целых
// разной ширины — разные объекты с неравным сравнением, и колонка
// развалилась бы молча.
//
// Равенство определено операционно: сравниваются КАТЕГОРИЯ (целое,
// строка, логическое, пусто, отсутствует) и значение, а не тип времени
// выполнения.
func TestRecordsEqualComparesCategoryAndValue(t *testing.T) {
	cases := []struct {
		name  string
		a, b  map[string]any
		equal bool
	}{
		{"одинаковые целые разной ширины",
			map[string]any{"id": int64(1)}, map[string]any{"id": uint64(1)}, true},
		{"разные целые",
			map[string]any{"id": int64(1)}, map[string]any{"id": int64(2)}, false},
		{"целое и строка — разные категории",
			map[string]any{"id": int64(1)}, map[string]any{"id": "1"}, false},
		{"пусто и ноль — разные категории",
			map[string]any{"age": nil}, map[string]any{"age": int64(0)}, false},
		{"пусто и пусто",
			map[string]any{"age": nil}, map[string]any{"age": nil}, true},
		{"отсутствует и пусто — не одно и то же",
			map[string]any{}, map[string]any{"age": nil}, false},
		{"отсутствует и пустая строка — не одно и то же",
			map[string]any{}, map[string]any{"email": ""}, false},
		{"лишнее поле",
			map[string]any{"id": int64(1)}, map[string]any{"id": int64(1), "x": int64(2)}, false},
		{"логические",
			map[string]any{"ok": true}, map[string]any{"ok": true}, true},
		{"логическое и целое",
			map[string]any{"ok": true}, map[string]any{"ok": int64(1)}, false},
		{"строки",
			map[string]any{"name": "Анна"}, map[string]any{"name": "Анна"}, true},
		{"обе пустые",
			map[string]any{}, map[string]any{}, true},
	}
	for _, c := range cases {
		if got := RecordsEqual(c.a, c.b); got != c.equal {
			t.Fatalf("%s: получили %v, ждали %v", c.name, got, c.equal)
		}
		if got := RecordsEqual(c.b, c.a); got != c.equal {
			t.Fatalf("%s (в обратную сторону): получили %v, ждали %v", c.name, got, c.equal)
		}
	}
}

// Целое, не помещающееся в знаковое 64-битное, остаётся целым и
// сравнивается по значению — это и означает «остаётся как есть».
func TestRecordsEqualHandlesVeryLargeIntegers(t *testing.T) {
	huge := uint64(1) << 63
	if !RecordsEqual(map[string]any{"id": huge}, map[string]any{"id": huge}) {
		t.Fatal("два одинаковых очень больших целых признаны разными")
	}
	if RecordsEqual(map[string]any{"id": huge}, map[string]any{"id": int64(-1)}) {
		t.Fatal("очень большое целое признано равным отрицательному")
	}
}
