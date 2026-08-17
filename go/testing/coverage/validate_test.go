package coverage

import (
	"os"
	"testing"
)

// TestDiscountedPrice покрывает «нормальные» входы: pct 0, 10, 50, 100.
// Функция DiscountedPrice линейна и не содержит ветвлений, поэтому уже
// эти четыре кейса дают 100% покрытия операторов (statements).
//
// Обратите внимание: pct=150 здесь намеренно НЕ проверяется. Граничный
// баг (уход цены в минус при pct>100) остаётся незамеченным несмотря на
// полное покрытие — покрытие операторов (statements) говорит лишь о том,
// что каждый оператор был исполнен хотя бы раз, а не о том, что проверены
// все интересные значения входа. Тест, который ловит этот баг, —
// TestDiscountedPrice_BugCaught ниже (по умолчанию пропускается, см. его
// комментарий).
func TestDiscountedPrice(t *testing.T) {
	cases := []struct {
		name string
		base int
		pct  int
		want int
	}{
		{"no discount", 1000, 0, 1000},
		{"ten percent", 1000, 10, 900},
		{"half price", 1000, 50, 500},
		{"full discount", 1000, 100, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DiscountedPrice(tc.base, tc.pct)
			if got != tc.want {
				t.Errorf("DiscountedPrice(%d, %d) = %d, want %d", tc.base, tc.pct, got, tc.want)
			}
		})
	}
}

// TestDiscountedPrice_BugCaught показывает, каким должен быть тест, чтобы
// поймать граничный баг: pct=150 (за пределами разумного диапазона 0..100)
// не должен давать отрицательную цену. DiscountedPrice такой валидации не
// делает, поэтому этот тест осознанно падает — он ловит именно то, что
// пропускает TestDiscountedPrice при 100% покрытии операторов (statements).
//
// По умолчанию тест пропускается (SKIP), чтобы основной прогон
// `go test ./coverage/ -cover` оставался чистым (все тесты PASS) и
// демонстрировал сам факт: 100% покрытия операторов (statements), но баг
// жив. Чтобы увидеть обнаружение бага вживую:
//
//	DEMO_BUG_CAUGHT=1 go test ./coverage/ -run BugCaught -v
func TestDiscountedPrice_BugCaught(t *testing.T) {
	if os.Getenv("DEMO_BUG_CAUGHT") != "1" {
		t.Skip("demo-тест выключен по умолчанию; запустить: DEMO_BUG_CAUGHT=1 go test -run BugCaught -v ./coverage/")
	}

	got := DiscountedPrice(1000, 150)
	if got < 0 {
		t.Errorf("DiscountedPrice(1000, 150) = %d — цена ушла в минус: pct>100 не валидируется (баг пойман)", got)
	}
}
