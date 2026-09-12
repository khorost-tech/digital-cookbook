// Package numeric — сюжет, где дженерики Go выигрывают.
//
// Value-типы (int, float64 и т. п.) специализируются компилятором по «форме»
// (GC-shape stenciling): для каждой формы генерируется отдельная копия кода,
// поэтому дженерик-версия по производительности близка к рукописной.
// Версия через interface{} вынуждена боксить каждый элемент (упаковка value в
// интерфейсное значение), что МОЖЕТ приводить к аллокации на куче — в этих
// бенчмарках это видно для float64 и для int вне small-int cache (рантайм
// кэширует малые целые, поэтому аллокаций не ровно N). Отсюда лишние аллокации
// и заметное замедление. Числа снимаются бенчами в numeric_test.go.
package numeric

// Number — constraint: все встроенные числовые типы и их производные (~).
type Number interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64
}

// SumGeneric — дженерик-сумма по любому числовому типу.
// Компилятор специализирует её по форме T (int, float64, ...).
func SumGeneric[T Number](xs []T) T {
	var acc T
	for _, x := range xs {
		acc += x
	}
	return acc
}

// SumInt — рукописная сумма по []int (эталон для сравнения с дженериком).
func SumInt(xs []int) int {
	var acc int
	for _, x := range xs {
		acc += x
	}
	return acc
}

// SumFloat64 — рукописная сумма по []float64 (эталон для float-формы).
func SumFloat64(xs []float64) float64 {
	var acc float64
	for _, x := range xs {
		acc += x
	}
	return acc
}

// SumIface — сумма через interface{}: элементы приходят боксированными,
// внутри — type switch по конкретному типу. Показывает цену боксинга.
// Возвращаем float64, чтобы единообразно складывать разные числовые типы.
func SumIface(xs []any) float64 {
	var acc float64
	for _, x := range xs {
		switch v := x.(type) {
		case int:
			acc += float64(v)
		case int64:
			acc += float64(v)
		case float64:
			acc += v
		case float32:
			acc += float64(v)
		default:
			// Неизвестный числовой тип игнорируем: в бенчах его нет.
		}
	}
	return acc
}
