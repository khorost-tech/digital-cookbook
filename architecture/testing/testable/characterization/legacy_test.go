package characterization

import (
	"flag"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -update перегенерирует «золотой мастер». Запускать ОСОЗНАННО: только когда
// изменение поведения намеренное. При рефакторинге golden трогать нельзя —
// в этом весь смысл защиты.
var update = flag.Bool("update", false, "перезаписать testdata/receipt.golden")

// сценарии подобраны так, чтобы задеть странности легаси: длинное кириллическое
// имя (обрезка по байтам), сумма на границе скидки, усечение при делении,
// недоплата (отрицательная сдача).
var cases = []struct {
	name  string
	items []Item
	paid  int64
}{
	{"пусто", nil, 0},
	{"одна позиция без скидки", []Item{{"Кофе", 5_000, 1}}, 5_000},
	{"длинное кириллическое имя", []Item{{"Шоколад молочный с фундуком", 3_000, 2}}, 10_000},
	{"ровно на пороге скидки", []Item{{"Чай", 10_000, 1}}, 10_000},
	{"усечение скидки", []Item{{"Набор", 19_999, 1}}, 20_000},
	{"недоплата — отрицательная сдача", []Item{{"Торт", 30_000, 1}}, 1_000},
}

// TestFormatReceipt_GoldenMaster — «золотой мастер»: прогоняет легаси на наборе
// входов и сверяет ВЕСЬ вывод с зафиксированным файлом. Он не утверждает, что
// поведение верное, — он утверждает, что оно НЕ ИЗМЕНИЛОСЬ.
func TestFormatReceipt_GoldenMaster(t *testing.T) {
	var b strings.Builder
	for _, c := range cases {
		b.WriteString("### " + c.name + "\n")
		b.WriteString(FormatReceipt(c.items, c.paid))
		b.WriteString("\n")
	}
	got := b.String()

	golden := filepath.Join("testdata", "receipt.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("запись golden: %v", err)
		}
		t.Log("golden перезаписан:", golden)
		return
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("чтение golden (сгенерируйте: go test ./characterization/ -run GoldenMaster -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("вывод разошёлся с золотым мастером.\n--- получено ---\n%s\n--- ожидалось ---\n%s", got, want)
	}
}

// ДОКАЗАТЕЛЬСТВО, что рефакторинг ничего не сломал.
//
// Golden-файл сам по себе доказывает меньше, чем кажется: он сверяет текущий
// код с текущим файлом. Если бы поведение поехало ВМЕСТЕ с golden (например,
// кто-то машинально прогнал -update), тест остался бы зелёным. Настоящая
// проверка — сравнить СТАРУЮ и НОВУЮ реализации на одном корпусе.
//
// formatReceiptLegacy — исходная версия, сохранённая дословно (legacy_before.go).
func TestRefactor_ЭквивалентностьСтаройРеализации(t *testing.T) {
	// 1) тот же корпус, что и у golden
	for _, c := range cases {
		old := formatReceiptLegacy(c.items, c.paid)
		got := FormatReceipt(c.items, c.paid)
		if old != got {
			t.Errorf("%s: рефакторинг изменил поведение\n--- было ---\n%s\n--- стало ---\n%s", c.name, old, got)
		}
	}

	// 2) рандомизированный свип с ФИКСИРОВАННЫМ seed: 2000 случайных чеков.
	// Детерминированно (seed зафиксирован), но покрывает куда больше входов,
	// чем шесть ручных случаев, — включая суммы вокруг порога скидки и
	// многобайтовые имена произвольной длины.
	rnd := rand.New(rand.NewPCG(42, 1024))
	names := []string{"Кофе", "Шоколад молочный с фундуком", "Tea", "Набор конфет ассорти", "Т", ""}
	for i := 0; i < 2000; i++ {
		n := rnd.IntN(4)
		items := make([]Item, 0, n)
		for j := 0; j < n; j++ {
			items = append(items, Item{
				Name:       names[rnd.IntN(len(names))],
				PriceCents: int64(rnd.IntN(30_000)),
				Qty:        1 + rnd.IntN(3),
			})
		}
		paid := int64(rnd.IntN(60_000))

		if old, got := formatReceiptLegacy(items, paid), FormatReceipt(items, paid); old != got {
			t.Fatalf("расхождение на случайном входе #%d (items=%+v paid=%d)\n--- было ---\n%s\n--- стало ---\n%s",
				i, items, paid, old, got)
		}
	}
}

// Вот ради чего был рефакторинг: появился ШОВ. Правило скидки теперь можно
// подменить и проверить отдельно — не трогая форматирование и не ломая
// прежних вызывающих (FormatReceipt по-прежнему печатает по историческому
// правилу, что и стережёт golden выше).
func TestPrinter_ШовПравилаСкидки(t *testing.T) {
	items := []Item{{"Чай", 10_000, 1}}

	// своё правило: плоские 10.00 вместо исторических 5%
	flat := Printer{Discount: func(int64) int64 { return 1_000 }}
	got := flat.Format(items, 10_000)

	if !strings.Contains(got, "СКИДКА: -10.00") {
		t.Errorf("шов не сработал, скидка не подменилась:\n%s", got)
	}
	if !strings.Contains(got, "ИТОГО:  90.00") {
		t.Errorf("итог не пересчитался по новому правилу:\n%s", got)
	}

	// а историческое правило осталось на месте у прежней точки входа
	if legacy := FormatReceipt(items, 10_000); !strings.Contains(legacy, "СКИДКА: -5.00") {
		t.Errorf("FormatReceipt изменил поведение:\n%s", legacy)
	}
}

// Правило скидки теперь тестируется в изоляции — без печати чека вообще.
func TestDefaultDiscount(t *testing.T) {
	cases := []struct{ total, want int64 }{
		{9_999, 0},      // ниже порога
		{10_000, 500},   // ровно порог: 5%
		{19_999, 999},   // усечение: 999.95 → 999
		{20_000, 1_000}, // 5% (историческое правило — без второго тира)
	}
	for _, c := range cases {
		if got := DefaultDiscount(c.total); got != c.want {
			t.Errorf("DefaultDiscount(%d) = %d, want %d", c.total, got, c.want)
		}
	}
}
