// interfaces_test.go — тесты корректности: неявное удовлетворение,
// «accept interfaces» на срезе разных типов, ветви type switch.
package interfaces

import "testing"

// TestImplicitSatisfaction: разные конкретные типы неявно удовлетворяют
// один интерфейс и работают через общий срез Describer.
func TestImplicitSatisfaction(t *testing.T) {
	items := []Describer{
		Point{X: 1, Y: 2},
		User{Name: "Alice"},
	}
	got := JoinDescriptions(items)
	want := "точка (1, 2); пользователь Alice"
	if got != want {
		t.Errorf("JoinDescriptions = %q, ожидали %q", got, want)
	}
}

// TestJoinEmpty: пустой вход даёт пустую строку без паник.
func TestJoinEmpty(t *testing.T) {
	if got := JoinDescriptions(nil); got != "" {
		t.Errorf("для nil ожидали пустую строку, получили %q", got)
	}
}

// TestClassifyKind: type switch выбирает правильную ветвь по динамическому типу.
func TestClassifyKind(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, "nil"},
		{"int", 42, "int(42)"},
		{"string", "hi", `string("hi")`},
		{"describer", Point{X: 3, Y: 4}, "Describer: точка (3, 4)"},
		{"other", 3.14, "неизвестный тип float64"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyKind(c.in); got != c.want {
				t.Errorf("ClassifyKind(%v) = %q, ожидали %q", c.in, got, c.want)
			}
		})
	}
}
