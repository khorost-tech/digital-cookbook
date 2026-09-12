// Package binsize — сюжет про размер бинаря.
//
// Go НЕ мономорфизирует дженерики как C++ (отдельная копия под КАЖДЫЙ тип).
// Вместо этого работает GC-shape stenciling: копия кода генерируется на каждую
// «форму» (shape), а типы одной формы (например, все указатели) делят одну
// копию плюс словарь. Поэтому инстанцирование дженерика для многих разных
// типов раздувает бинарь СКРОМНО. Для сравнения cmd/iface делает то же самое
// через единственную interface{}-функцию (одна копия кода на все типы).
//
// Смысл стенда: собрать оба бинаря и сравнить их размер (снимает оркестратор).
package binsize

// Zero — дженерик-функция, инстанцируемая для многих числовых типов.
// Тело намеренно тривиально: интересен факт инстанцирования по форме, а не
// вычисление. Возвращает нулевое значение своего типа.
func Zero[T any]() T {
	var z T
	return z
}

// SumTwoGeneric — ещё одна дженерик-функция с арифметикой по форме,
// чтобы у каждой инстанциации было нетривиальное тело.
func SumTwoGeneric[T Addable](a, b T) T {
	return a + b
}

// Addable — constraint для SumTwoGeneric.
type Addable interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64 | ~string
}

// SumTwoIface — эквивалент через interface{}: ОДНА копия кода на все типы.
// Используется в cmd/iface для контраста с инстанцированием дженерика.
func SumTwoIface(a, b any) any {
	switch av := a.(type) {
	case int:
		return av + b.(int)
	case int8:
		return av + b.(int8)
	case int16:
		return av + b.(int16)
	case int32:
		return av + b.(int32)
	case int64:
		return av + b.(int64)
	case uint:
		return av + b.(uint)
	case uint8:
		return av + b.(uint8)
	case uint16:
		return av + b.(uint16)
	case uint32:
		return av + b.(uint32)
	case uint64:
		return av + b.(uint64)
	case float32:
		return av + b.(float32)
	case float64:
		return av + b.(float64)
	case string:
		return av + b.(string)
	default:
		return nil
	}
}
