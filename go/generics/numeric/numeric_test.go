// Тесты корректности и бенчмарки для сюжета «дженерики выигрывают».
//
// Корректность: дженерик, рукописная и interface{}-версии дают один и тот же
// результат. Бенчи сравнивают три реализации на int и на float64.
// Ожидаемая картина (числа снимайте прогоном у себя):
//   - _Generic ≈ _Handwritten (форма специализируется, нулевые аллокации);
//   - _Iface заметно медленнее и аллоцирует (боксинг каждого элемента).
//
// В бенчах _Iface боксинг делается ВНУТРИ измеряемого цикла намеренно: это и
// есть реальная цена интерфейсного подхода — чтобы сложить []int через
// interface{}, значения приходится упаковывать, а упаковка чисел > 255 идёт
// с аллокацией на куче. Значения < 256 рантайм кэширует (staticuint64s), но
// при benchN элементов бо́льшая часть аллоцирует, и это видно в allocs/op.
package numeric

import "testing"

// makeInts — детерминированный срез int для тестов и бенчей.
func makeInts(n int) []int {
	xs := make([]int, n)
	for i := range xs {
		xs[i] = i
	}
	return xs
}

// makeFloats — детерминированный срез float64.
func makeFloats(n int) []float64 {
	xs := make([]float64, n)
	for i := range xs {
		xs[i] = float64(i)
	}
	return xs
}

// boxInts — упаковывает []int в []any (боксинг заранее, вне цикла бенча).
func boxInts(xs []int) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// boxFloats — упаковывает []float64 в []any.
func boxFloats(xs []float64) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func TestSumIntConsistency(t *testing.T) {
	xs := makeInts(1000)
	// Ожидаемая сумма 0..999 = 999*1000/2.
	want := 999 * 1000 / 2

	if got := SumInt(xs); got != want {
		t.Fatalf("SumInt = %d, want %d", got, want)
	}
	if got := SumGeneric(xs); got != want {
		t.Fatalf("SumGeneric = %d, want %d", got, want)
	}
	if got := SumIface(boxInts(xs)); got != float64(want) {
		t.Fatalf("SumIface = %v, want %d", got, want)
	}
}

func TestSumFloatConsistency(t *testing.T) {
	xs := makeFloats(1000)
	want := SumFloat64(xs)

	if got := SumGeneric(xs); got != want {
		t.Fatalf("SumGeneric = %v, want %v", got, want)
	}
	if got := SumIface(boxFloats(xs)); got != want {
		t.Fatalf("SumIface = %v, want %v", got, want)
	}
}

// sink — пакетная переменная, чтобы компилятор не выкинул результат бенча.
var (
	sinkInt   int
	sinkFloat float64
)

const benchN = 4096

// --- int ---

func BenchmarkSumInt_Generic(b *testing.B) {
	xs := makeInts(benchN)
	b.ReportAllocs()
	b.ResetTimer()
	var acc int
	for i := 0; i < b.N; i++ {
		acc += SumGeneric(xs)
	}
	sinkInt = acc
}

func BenchmarkSumInt_Handwritten(b *testing.B) {
	xs := makeInts(benchN)
	b.ReportAllocs()
	b.ResetTimer()
	var acc int
	for i := 0; i < b.N; i++ {
		acc += SumInt(xs)
	}
	sinkInt = acc
}

func BenchmarkSumInt_Iface(b *testing.B) {
	xs := makeInts(benchN)
	b.ReportAllocs()
	b.ResetTimer()
	var acc float64
	for i := 0; i < b.N; i++ {
		// Боксинг внутри цикла — реальная цена интерфейсного подхода.
		acc += SumIface(boxInts(xs))
	}
	sinkFloat = acc
}

// --- float64 ---

func BenchmarkSumFloat64_Generic(b *testing.B) {
	xs := makeFloats(benchN)
	b.ReportAllocs()
	b.ResetTimer()
	var acc float64
	for i := 0; i < b.N; i++ {
		acc += SumGeneric(xs)
	}
	sinkFloat = acc
}

func BenchmarkSumFloat64_Handwritten(b *testing.B) {
	xs := makeFloats(benchN)
	b.ReportAllocs()
	b.ResetTimer()
	var acc float64
	for i := 0; i < b.N; i++ {
		acc += SumFloat64(xs)
	}
	sinkFloat = acc
}

func BenchmarkSumFloat64_Iface(b *testing.B) {
	xs := makeFloats(benchN)
	b.ReportAllocs()
	b.ResetTimer()
	var acc float64
	for i := 0; i < b.N; i++ {
		// Боксинг внутри цикла — реальная цена интерфейсного подхода.
		acc += SumIface(boxFloats(xs))
	}
	sinkFloat = acc
}
