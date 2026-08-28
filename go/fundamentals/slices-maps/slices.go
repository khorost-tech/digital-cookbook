// slices.go — как устроены слайсы: рост append (len/cap), nil против пустого
// слайса, корректное копирование backing-массива.
//
// Пакет назван slicesmaps, чтобы не конфликтовать со стандартным пакетом
// slices, который здесь импортируется.
package slicesmaps

import "slices"

// GrowthStep — снимок состояния слайса после одного append: длина и ёмкость.
type GrowthStep struct {
	Len int
	Cap int
}

// AppendGrowth добавляет по одному элементу n раз, начиная с nil-слайса, и
// возвращает историю (len, cap) после каждого шага. Ёмкость растёт скачками:
// когда места не хватает, рантайм выделяет НОВЫЙ массив (обычно кратно больше)
// и копирует данные. Конкретный множитель роста — деталь реализации, поэтому
// тест проверяет инварианты (len растёт на 1, cap >= len, cap не убывает),
// а не жёсткие числа.
func AppendGrowth(n int) []GrowthStep {
	steps := make([]GrowthStep, 0, n)
	var s []int // nil-слайс: len==0, cap==0
	for i := 0; i < n; i++ {
		s = append(s, i)
		steps = append(steps, GrowthStep{Len: len(s), Cap: cap(s)})
	}
	return steps
}

// AppendToNil показывает, что append к nil-слайсу корректен: nil здесь ведёт
// себя как пустой слайс, append сам выделит массив.
func AppendToNil() []int {
	var s []int // nil
	s = append(s, 1, 2, 3)
	return s
}

// IsNil сообщает, является ли слайс nil. Важно: nil-слайс и пустой
// (не-nil, len==0) слайс НЕ равны по == nil, но неотличимы по len и по
// поведению в range/append. Сравнивать состояние «пусто ли» надо через len,
// а не через == nil.
func IsNil(s []int) bool {
	return s == nil
}

// CloneViaAppend делает независимую копию через append([]T(nil), src...):
// классический приём до появления slices.Clone. Возвращаемый слайс НЕ делит
// backing-массив с src.
func CloneViaAppend(src []int) []int {
	return append([]int(nil), src...)
}

// CloneViaStdlib — то же через slices.Clone (Go 1.21+): короче и понятнее.
func CloneViaStdlib(src []int) []int {
	return slices.Clone(src)
}
