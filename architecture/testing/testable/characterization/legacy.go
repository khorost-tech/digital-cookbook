// Package characterization показывает заход в легаси через
// characterization-тест («золотой мастер»): код нетестируем и страшен, но
// рефакторить надо. Сначала фиксируем ТЕКУЩЕЕ поведение как есть — вместе с
// его странностями и багами, — потом рефакторим под этой защитой.
//
// Здесь код УЖЕ отрефакторен: разложен на маленькие функции и получил шов
// (правило скидки). Golden в testdata снят с ИСХОДНОЙ версии и не менялся —
// он и доказывает, что рефакторинг ничего не сломал.
package characterization

import (
	"fmt"
	"strings"
)

// Item — позиция чека.
type Item struct {
	Name       string
	PriceCents int64
	Qty        int
}

// nameLimit — исторический лимит длины имени.
const nameLimit = 12

// FormatReceipt — прежняя точка входа: сигнатура не тронута, вызывающие не
// заметили рефакторинга. Внутри теперь Printer с дефолтным правилом скидки.
func FormatReceipt(items []Item, paidCents int64) string {
	return Printer{Discount: DefaultDiscount}.Format(items, paidCents)
}

// Printer — то, ради чего затевался рефакторинг: ШОВ. Правило скидки стало
// подменяемым, а печать разложена на маленькие функции. Теперь правило можно
// менять и тестировать, не трогая форматирование.
type Printer struct {
	Discount func(totalCents int64) int64
}

// Format печатает чек. Поведение — байт в байт как у легаси (см. golden).
func (p Printer) Format(items []Item, paidCents int64) string {
	var b strings.Builder
	b.WriteString("=== ЧЕК ===\n")

	var total int64
	for i, it := range items {
		sum := it.PriceCents * int64(it.Qty)
		total += sum
		b.WriteString(formatLine(i+1, it, sum))
	}

	disc := p.Discount(total)
	total -= disc
	if disc > 0 {
		b.WriteString(fmt.Sprintf("СКИДКА: -%s\n", money(disc)))
	}

	b.WriteString(fmt.Sprintf("ИТОГО:  %s\n", money(total)))
	// Отрицательная сдача при недоплате — легаси-поведение, сохранено намеренно:
	// его фиксирует golden. Чинить — отдельным осознанным шагом.
	b.WriteString(fmt.Sprintf("СДАЧА:  %s\n", money(paidCents-total)))
	return b.String()
}

// DefaultDiscount — историческое правило: 5% от 100.00, целочисленно
// (усечение в пользу магазина: 999.95 коп. → 999 коп.).
func DefaultDiscount(totalCents int64) int64 {
	if totalCents >= 10_000 {
		return totalCents * 5 / 100
	}
	return 0
}

// formatLine печатает строку позиции.
func formatLine(n int, it Item, sumCents int64) string {
	return fmt.Sprintf("%d) %-12s x%d  %s\n", n, truncName(it.Name), it.Qty, money(sumCents))
}

// truncName обрезает имя. ВНИМАНИЕ: обрезка по БАЙТАМ — исторический баг
// (на кириллице «12 символов» превращаются в 6). Сохранён намеренно: golden
// зафиксировал текущее поведение, а починка байты→руны — отдельное изменение
// поведения, которое обязано осознанно обновить golden.
func truncName(s string) string {
	if len(s) > nameLimit {
		return s[:nameLimit]
	}
	return s
}

func money(cents int64) string {
	neg := ""
	if cents < 0 {
		neg = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", neg, cents/100, cents%100)
}
