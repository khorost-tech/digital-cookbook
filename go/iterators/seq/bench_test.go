package seq

import "testing"

const benchN = 1000

// Бенчи сравнивают суммирование 0..benchN-1 тремя способами: обычным циклом,
// через range по слайсу и через range по iter.Seq (range-over-func).
// Вопрос: сколько стоит абстракция итератора.

// BenchmarkPlainLoop — эталон: голый for без промежуточных структур.
func BenchmarkPlainLoop(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sum := 0
		for i := 0; i < benchN; i++ {
			sum += i
		}
		_ = sum
	}
}

// BenchmarkSliceRange — range по заранее заполненному слайсу.
func BenchmarkSliceRange(b *testing.B) {
	data := make([]int, benchN)
	for i := range data {
		data[i] = i
	}
	b.ReportAllocs()
	for b.Loop() {
		sum := 0
		for _, v := range data {
			sum += v
		}
		_ = sum
	}
}

// BenchmarkSeqRange — range по iter.Seq (range-over-func). Каждый элемент —
// это вызов yield, и на тривиальном теле (сумма) абстракция заметна: в разы
// дороже голого цикла. Относительная накладная тает, когда тело делает
// реальную работу, но «итератор бесплатен» — миф; цена per-element. Числа
// в README стенда.
func BenchmarkSeqRange(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sum := 0
		for v := range Count(benchN) {
			sum += v
		}
		_ = sum
	}
}

// BenchmarkSeqFilterMap — цепочка Filter+Map через итераторы, без
// промежуточных слайсов: конвейер обходится за один проход.
func BenchmarkSeqFilterMap(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sum := 0
		even := Filter(Count(benchN), func(v int) bool { return v%2 == 0 })
		for v := range Map(even, func(v int) int { return v * 2 }) {
			sum += v
		}
		_ = sum
	}
}
