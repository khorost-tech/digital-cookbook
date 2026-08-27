package pricing

import (
	"testing"

	"pgregory.net/rapid"
)

// Замер, объясняющий, ПОЧЕМУ наивное свойство монотонности не находит нарушение
// (см. property_test.go). Разумный движок не сыплет равномерным шумом: он
// смещает выдачу к малым числам и краям диапазона. Значит, в интересную полосу
// (пара «чуть ниже порога» / «сразу за порогом») черновики почти не попадают.
//
// Это не тест, а измерение: утверждений нет, только вывод чисел.
//
//	go test -tags=property_demo -run Распределение ./pricing/ -rapid.checks=200000 -v
func TestРаспределениеГенератора(t *testing.T) {
	if !propertyDemo {
		t.Skip("замер: go test -tags=property_demo -run Распределение ./pricing/ -rapid.checks=200000 -v")
	}

	var draws, inBand, aboveTier1 int
	rapid.Check(t, func(t *rapid.T) {
		v := rapid.Int64Range(0, 30_000).Draw(t, "v")
		draws++
		if v >= 9_500 && v <= 10_500 {
			inBand++
		}
		if v >= Tier1Cents {
			aboveTier1++
		}
	})

	pct := func(n int) float64 { return 100 * float64(n) / float64(draws) }
	t.Logf("черновиков=%d", draws)
	t.Logf("в полосе [9500..10500]: %d (%.2f%%) — при равномерном было бы ~3.3%%", inBand, pct(inBand))
	t.Logf(">= %d (порог 1-го тира): %d (%.1f%%) — при равномерном было бы ~66.7%%",
		Tier1Cents, aboveTier1, pct(aboveTier1))
}
