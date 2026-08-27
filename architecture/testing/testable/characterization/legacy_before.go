package characterization

import (
	"fmt"
	"strings"
)

// formatReceiptLegacy — ИСХОДНАЯ реализация, до рефакторинга. Сохранена в
// стенде намеренно и дословно: golden-файл сам по себе доказывает лишь то,
// что текущий код совпадает с текущим файлом. Чтобы «рефакторинг ничего не
// сломал» стало проверяемым утверждением, нужна старая версия рядом — тогда
// эквивалентность old vs new проверяется исполняемым тестом на одном корпусе
// (см. TestRefactor_ЭквивалентностьСтаройРеализации).
//
// В проде так, конечно, не живут: там старая версия остаётся в истории Git, а
// эквивалентность проверяют один раз при рефакторинге. Здесь она нужна как
// доказательство для читателя.
//
// ВАЖНО про независимость оракула: legacy-версия обязана быть самодостаточной.
// Если бы она звала текущую money(), регрессия ВНУТРИ money одинаково изменила
// бы обе стороны — и тест эквивалентности остался бы зелёным, ничего не заметив.
// Поэтому здесь свой moneyLegacy: старая реализация целиком, без общих деталей
// с новой. Оракул сравнивает две независимые ветки, а не две обёртки над одним
// кодом.
//
// Ниже — код как он был: один длинный метод, всё внутри, включая три
// исторических дефекта (обрезка по байтам, усечение скидки, отрицательная
// сдача при недоплате).
func formatReceiptLegacy(items []Item, paidCents int64) string {
	var b strings.Builder
	b.WriteString("=== ЧЕК ===\n")

	var total int64
	for i, it := range items {
		name := it.Name
		if len(name) > 12 {
			name = name[:12] // обрезка по байтам, не по рунам
		}
		sum := it.PriceCents * int64(it.Qty)
		total += sum
		b.WriteString(fmt.Sprintf("%d) %-12s x%d  %s\n", i+1, name, it.Qty, moneyLegacy(sum)))
	}

	var disc int64
	if total >= 10_000 {
		disc = total * 5 / 100 // усечение
	}
	total -= disc
	if disc > 0 {
		b.WriteString(fmt.Sprintf("СКИДКА: -%s\n", moneyLegacy(disc)))
	}

	b.WriteString(fmt.Sprintf("ИТОГО:  %s\n", moneyLegacy(total)))
	b.WriteString(fmt.Sprintf("СДАЧА:  %s\n", moneyLegacy(paidCents-total)))
	return b.String()
}

// moneyLegacy — копия форматирования денег на момент «до». Дублирование здесь
// намеренное: см. комментарий про независимость оракула выше.
func moneyLegacy(cents int64) string {
	neg := ""
	if cents < 0 {
		neg = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", neg, cents/100, cents%100)
}
