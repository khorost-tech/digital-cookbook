package functionalcore

import (
	"testing"
	"time"
)

// Ядро тестируется таблицей: НИ ОДНОГО дублёра, ни фейка, ни мока — потому что
// зависимостей нет вовсе. Время подаём аргументом. Все ветки правил закрыты
// здесь, быстро и точно.
func TestDecide(t *testing.T) {
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		o    Order
		now  time.Time
		want Decision
	}{
		{
			"ниже порога — без скидки",
			Order{TotalCents: 9_999, PlacedAt: base},
			base,
			Decision{Reason: "сумма ниже порога скидки"},
		},
		{
			"ровно 100.00 — 5%",
			Order{TotalCents: 10_000, PlacedAt: base},
			base,
			Decision{DiscountCents: 500, Notify: true, Reason: "объёмная скидка"},
		},
		{
			"на копейку ниже 200 — всё ещё 5% (усечение)",
			Order{TotalCents: 19_999, PlacedAt: base},
			base,
			Decision{DiscountCents: 999, Notify: true, Reason: "объёмная скидка"},
		},
		{
			"ровно 200.00 — 10%",
			Order{TotalCents: 20_000, PlacedAt: base},
			base,
			Decision{DiscountCents: 2_000, Notify: true, Reason: "объёмная скидка"},
		},
		{
			"первый заказ — плюс бонус",
			Order{TotalCents: 20_000, PlacedAt: base, IsFirst: true},
			base,
			Decision{DiscountCents: 2_500, Notify: true, Reason: "объёмная скидка + бонус за первый заказ"},
		},
		{
			"просрочен — скидки нет, даже если сумма большая",
			Order{TotalCents: 50_000, PlacedAt: base},
			base.Add(31 * 24 * time.Hour),
			Decision{Reason: "заказ просрочен для скидки"},
		},
		{
			"ровно на границе просрочки — скидка ещё есть",
			Order{TotalCents: 50_000, PlacedAt: base},
			base.Add(30 * 24 * time.Hour),
			Decision{DiscountCents: 5_000, Notify: true, Reason: "объёмная скидка"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Decide(c.o, c.now); got != c.want {
				t.Errorf("Decide() = %+v, want %+v", got, c.want)
			}
		})
	}
}

// Чистота ядра проверяема буквально: повторный вызов с теми же входами даёт
// тот же выход, и вход не меняется.
func TestDecide_ЧистаяФункция(t *testing.T) {
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	o := Order{UserID: "u1", TotalCents: 20_000, PlacedAt: base, IsFirst: true}
	before := o

	first := Decide(o, base)
	second := Decide(o, base)

	if first != second {
		t.Errorf("два вызова дали разное: %+v и %+v", first, second)
	}
	if o != before {
		t.Errorf("Decide изменила входной Order: было %+v, стало %+v", before, o)
	}
}
