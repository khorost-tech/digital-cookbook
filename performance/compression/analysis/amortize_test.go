package analysis

import (
	"math"
	"testing"
)

// Режим «холодное хранение»: платим за сжатие один раз, за разжатие — N раз.
func TestTotalCostGrowsWithReads(t *testing.T) {
	one := TotalCostMs(500, 10, 1)
	if math.Abs(one-510) > 1e-9 {
		t.Fatalf("одно чтение: %v, ожидалось 510", one)
	}
	hundred := TotalCostMs(500, 10, 100)
	if math.Abs(hundred-1500) > 1e-9 {
		t.Fatalf("сто чтений: %v, ожидалось 1500", hundred)
	}
	// При многих чтениях вклад сжатия становится второстепенным — это и есть
	// причина, по которой архив терпит медленный плотный кодек.
	if hundred/one < 2 {
		t.Fatal("стоимость обязана существенно вырасти при 100 чтениях")
	}
}

func TestTotalCostZeroReads(t *testing.T) {
	// Ноль чтений — данные записаны и не прочитаны ни разу: платим только сжатие.
	if got := TotalCostMs(500, 10, 0); math.Abs(got-500) > 1e-9 {
		t.Fatalf("ноль чтений: %v, ожидалось 500", got)
	}
}

func TestTotalCostNegativeReadsClampedToZero(t *testing.T) {
	// Отрицательное число чтений не имеет смысла в предметной области —
	// функция не должна выдавать отрицательную (тем более неверную) стоимость.
	// Ожидаемое поведение: как ноль чтений, платим только сжатие.
	if got := TotalCostMs(500, 10, -3); math.Abs(got-500) > 1e-9 {
		t.Fatalf("отрицательные чтения: %v, ожидалось 500 (как для 0)", got)
	}
}
