// Package mutation — копия доменной логики tdd для демонстрации
// mutation-тестирования на СЛАБОМ наборе тестов (без граничных примеров).
package mutation

// Item — позиция корзины: цена в копейках и количество.
type Item struct {
	PriceCents int64
	Qty        int64
}

type tier struct {
	MinCents int64
	Percent  int64
}

var tiers = []tier{
	{MinCents: 20000, Percent: 10},
	{MinCents: 10000, Percent: 5},
}

// Total — та же логика, что и в пакете tdd.
func Total(items []Item) int64 {
	var subtotal int64
	for _, it := range items {
		subtotal += it.PriceCents * it.Qty
	}
	return subtotal - discount(subtotal)
}

func discount(subtotal int64) int64 {
	for _, t := range tiers {
		if subtotal >= t.MinCents {
			return subtotal * t.Percent / 100
		}
	}
	return 0
}
