package bench

import (
	"context"
	"testing"
)

// Перцентиль — единственная чистая функция в стенде, и именно на ней проще
// всего ошибиться на единицу. Числа статьи держатся на ней, поэтому тест есть.
func TestPercentile(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := []struct {
		q    float64
		want float64
	}{
		{0.50, 5},
		{0.95, 10},
		{0.99, 10},
	}
	for _, c := range cases {
		if got := Percentile(xs, c.q); got != c.want {
			t.Errorf("Percentile(q=%v) = %v, хотели %v", c.q, got, c.want)
		}
	}
}

func TestPercentileEmpty(t *testing.T) {
	if got := Percentile(nil, 0.5); got != 0 {
		t.Errorf("на пустой выборке хотели 0, получили %v", got)
	}
}

// n=0 раньше делил на нулевую elapsed-время и давал OpsPerSec = +Inf молча.
// Run вызывается по-настоящему только с Task 4, поэтому дефект не проявлялся.
func TestRunRejectsZeroN(t *testing.T) {
	if _, err := Run(context.Background(), nil, OpRead, 0, 1, "sqlite"); err == nil {
		t.Fatal("Run(n=0) должен вернуть ошибку, а не пытаться делить на elapsed=0")
	}
}
