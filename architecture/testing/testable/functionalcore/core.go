// Package functionalcore показывает разделение «функциональное ядро —
// императивная оболочка»: вся доменная логика вынесена в ЧИСТЫЕ функции
// (детерминированные, без побочных эффектов и без I/O), а всё общение с миром
// (БД, сеть, часы) собрано в тонкой оболочке. Ядро тестируется таблицей без
// единого дублёра; оболочке остаётся пара тестов на проводку.
package functionalcore

import "time"

// --- ЯДРО: чистые типы и функции ---

// Order — заказ на входе решения.
type Order struct {
	UserID     string
	TotalCents int64
	PlacedAt   time.Time
	IsFirst    bool // первый заказ пользователя
}

// Decision — что делать с заказом. Чистый результат: никаких побочек.
type Decision struct {
	DiscountCents int64
	Notify        bool
	Reason        string
}

// Тиры скидки и лимит по времени вынесены в константы ядра.
const (
	tier1Cents = 10_000 // от 100.00 → 5%
	tier2Cents = 20_000 // от 200.00 → 10%
	firstBonus = 500    // первый заказ: +5.00 сверху
	staleAfter = 30 * 24 * time.Hour
)

// Decide — ЧИСТАЯ функция: одни и те же входы всегда дают один и тот же выход.
// Время — параметр, а не time.Now() внутри; никаких запросов и записей.
// Именно поэтому её можно покрыть таблицей исчерпывающе и мгновенно.
func Decide(o Order, now time.Time) Decision {
	if now.Sub(o.PlacedAt) > staleAfter {
		return Decision{Reason: "заказ просрочен для скидки"}
	}

	var pct int64
	switch {
	case o.TotalCents >= tier2Cents:
		pct = 10
	case o.TotalCents >= tier1Cents:
		pct = 5
	default:
		return Decision{Reason: "сумма ниже порога скидки"}
	}

	d := o.TotalCents * pct / 100
	reason := "объёмная скидка"
	if o.IsFirst {
		d += firstBonus
		reason = "объёмная скидка + бонус за первый заказ"
	}
	return Decision{DiscountCents: d, Notify: true, Reason: reason}
}
