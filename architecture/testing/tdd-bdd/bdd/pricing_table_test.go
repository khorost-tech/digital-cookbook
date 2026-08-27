package bdd

import (
	"testing"

	"tech.khorost/tdd-bdd-cookbook/tdd"
)

// TestBoundariesTableDriven кодирует ТЕ ЖЕ пять граничных примеров, что и
// discount.feature, но обычным table-driven тестом Go — без .feature-файла,
// без регэкспов-шагов, без парсинга «100.00» из текста. Сравните объём и
// количество слоёв с BDD-вариантом (pricing_bdd_test.go + features/*.feature):
// покрытие идентичное, а «клея» на порядок меньше.
func TestBoundariesTableDriven(t *testing.T) {
	cases := []struct {
		name       string
		priceCents int64
		wantCents  int64
	}{
		{"99.99 — без скидки", 9999, 9999},
		{"100.00 — 5%", 10000, 9500},
		{"199.99 — 5% с усечением", 19999, 19000},
		{"200.00 — 10%", 20000, 18000},
		{"250.00 — 10%", 25000, 22500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := tdd.Total([]tdd.Item{{PriceCents: c.priceCents, Qty: 1}})
			if got != c.wantCents {
				t.Errorf("Total(%d) = %d, want %d", c.priceCents, got, c.wantCents)
			}
		})
	}
}
