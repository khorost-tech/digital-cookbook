// Package tdd — доменная логика тарификации корзины, выстроенная test-first.
// Вся арифметика в копейках (int64), чтобы не ловить ошибки округления float.
package tdd

// Item — позиция корзины: цена в копейках и количество.
type Item struct {
	PriceCents int64
	Qty        int64
}

// tier — порог объёмной скидки: при subtotal >= MinCents применяется Percent.
type tier struct {
	MinCents int64
	Percent  int64
}

// tiers отсортированы по убыванию порога: берём первый подходящий.
var tiers = []tier{
	{MinCents: 20000, Percent: 10}, // >= 200.00 → 10%
	{MinCents: 10000, Percent: 5},  // >= 100.00 → 5%
}

// Total — итоговая сумма корзины с учётом объёмной скидки.
func Total(items []Item) int64 {
	var subtotal int64
	for _, it := range items {
		subtotal += it.PriceCents * it.Qty
	}
	return subtotal - discount(subtotal)
}

// discount возвращает величину скидки в копейках для данного subtotal.
func discount(subtotal int64) int64 {
	for _, t := range tiers {
		if subtotal >= t.MinCents {
			return subtotal * t.Percent / 100
		}
	}
	return 0
}
