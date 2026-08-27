package pricing

import "testing"

// ПРИЁМ 1: table-driven юнит — идиома Go. Примеры выбирает человек, поэтому
// проверено ровно то, о чём человек подумал: границы тиров и усечение.
func TestDiscount(t *testing.T) {
	cases := []struct {
		name  string
		total int64
		want  int64
	}{
		{"ниже порога", 9_999, 0},
		{"ровно 100.00 — 5%", 10_000, 500},
		{"на копейку ниже 200 — 5% с усечением", 19_999, 999},
		{"ровно 200.00 — 10%", 20_000, 2_000},
		{"250.00 — 10%", 25_000, 2_500},
		{"ноль", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Discount(c.total); got != c.want {
				t.Errorf("Discount(%d) = %d, want %d", c.total, got, c.want)
			}
		})
	}
}
