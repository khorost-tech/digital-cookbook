// Package pricing — доменное ядро, одинаковое во всех трёх языках стенда:
// объёмная скидка по тирам и итоговая цена. Ровно этот код (построчно тот же
// смысл) есть в java/ и python/ — чтобы сравнивать не задачи, а экосистемы.
package pricing

// Тиры скидки: от 100.00 — 5%, от 200.00 — 10%. Всё в копейках.
const (
	Tier1Cents = 10_000
	Tier2Cents = 20_000
)

// Discount — размер скидки в копейках для суммы заказа.
func Discount(totalCents int64) int64 {
	switch {
	case totalCents >= Tier2Cents:
		return totalCents * 10 / 100
	case totalCents >= Tier1Cents:
		return totalCents * 5 / 100
	default:
		return 0
	}
}

// Price — итоговая цена: сумма минус скидка.
func Price(totalCents int64) int64 {
	return totalCents - Discount(totalCents)
}
