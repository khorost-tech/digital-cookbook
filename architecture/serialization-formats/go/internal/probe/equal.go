package probe

import "math"

// RecordsEqual — определение равенства двух записей, на котором стоит
// граница между исходами «прочиталось то» и «прочиталось не то».
//
// Круг правок 5: раньше сравнение делалось средством языка, и в
// описании стенда осталось только примером («целое 1 и строка "1" не
// равны»). Для второй реализации этого мало: там два целых разной
// ширины — разные объекты с неравным сравнением, и колонка развалилась
// бы молча, без единого признака поломки.
//
// Правило: записи равны, если у них СОВПАДАЕТ НАБОР ИМЁН полей и для
// каждого имени совпадают КАТЕГОРИЯ значения и само значение.
// Категории: целое, строка, логическое, дробное, пусто. Отсутствие
// поля — не категория, а отсутствие: поле, которого нет, не равно полю
// со значением «пусто» и не равно полю с нулевым значением типа.
// Ширина целого — свойство представления, а не значения, и на равенство
// не влияет.
func RecordsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok {
			return false
		}
		if !valuesEqual(av, bv) {
			return false
		}
	}
	return true
}

// категории значений
type category int

const (
	catEmpty  category = iota // пусто
	catInt                    // целое
	catString                 // строка
	catBool                   // логическое
	catFloat                  // дробное — полей такого рода в стенде нет
	// catRecord — вложенная запись (задача 6bis, retype_message): поле
	// сменило тип со строки на вложенное сообщение, и значением такого
	// поля стенд несёт map[string]any — той же формы, что и запись
	// верхнего уровня.
	catRecord
	catOther // всё прочее: равенство определяется только тождеством
)

func valuesEqual(a, b any) bool {
	ca, cb := categoryOf(a), categoryOf(b)
	if ca != cb {
		return false
	}
	switch ca {
	case catEmpty:
		return true
	case catInt:
		return intsEqual(a, b)
	case catString:
		return a.(string) == b.(string)
	case catBool:
		return a.(bool) == b.(bool)
	case catFloat:
		return floatOf(a) == floatOf(b)
	case catRecord:
		// РЕКУРСИВНО тем же правилом («совпадает набор имён, и для
		// каждого имени — категория и значение»), а не оператором `==`:
		// map[string]any им не сравним вовсе — до этой правки сравнение
		// двух вложенных записей ПАНИКОВАЛО бы в рантайме ("comparing
		// uncomparable type"), стоило decode хоть раз вернуть вложенное
		// сообщение. На клетке retype_message это не гипотетика: одна
		// из пяти записей — измеренный случай, когда мусорные байты
		// успешно разбираются как валидное вложенное сообщение.
		am, aok := a.(map[string]any)
		bm, bok := b.(map[string]any)
		return aok && bok && RecordsEqual(am, bm)
	default:
		return a == b
	}
}

func categoryOf(v any) category {
	switch v.(type) {
	case nil:
		return catEmpty
	case int64, uint64:
		return catInt
	case string:
		return catString
	case bool:
		return catBool
	case float64, float32:
		return catFloat
	case map[string]any:
		return catRecord
	default:
		return catOther
	}
}

// intsEqual сравнивает целые по ЗНАЧЕНИЮ, а не по представлению.
// Нормализация приводит все целые к знаковому 64-битному, кроме тех,
// что в него не помещаются, — те остаются беззнаковыми, и сравнивать их
// со знаковыми приходится аккуратно: отрицательное не равно ничему из
// того, что не поместилось.
func intsEqual(a, b any) bool {
	ai, aSigned := a.(int64)
	au, _ := a.(uint64)
	bi, bSigned := b.(int64)
	bu, _ := b.(uint64)
	switch {
	case aSigned && bSigned:
		return ai == bi
	case !aSigned && !bSigned:
		return au == bu
	case aSigned:
		return ai >= 0 && uint64(ai) == bu
	default:
		return bi >= 0 && uint64(bi) == au
	}
}

func floatOf(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	default:
		return math.NaN()
	}
}
