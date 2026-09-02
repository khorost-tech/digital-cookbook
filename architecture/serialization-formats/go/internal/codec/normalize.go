package codec

import "math"

// Normalize приводит декодированное значение к виду, устойчивому к
// разнице числовых типов между библиотеками (json.Unmarshal отдаёт
// float64, hamba/avro — int64/int32 в зависимости от типа поля,
// dynamicpb — int32/int64 через protoreflect.Value). Стенд сравнивает
// декодированную запись с эталоном через reflect.DeepEqual, а он не
// прощает int32 vs int64 — без единой точки нормализации сравнение
// находило бы различия там, где их нет.
//
// Правило одно на всю пробу: и каноническая запись стенда, и результат
// чтения проходят через него. Две копии правила неизбежно разъедутся
// незаметно, а сравнение начнёт находить различия там, где их нет.
//
// Дробное с нулевой дробной частью становится целым, потому что числа
// канонических записей приходят из JSON и там разделения на целые и
// дробные нет вовсе. Дробное с НЕнулевой дробной частью целым не
// становится: усечение здесь было бы той самой тихой порчей, ради
// поиска которой стенд существует, — только нашей, а предъявлена она
// была бы формату.
func Normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = Normalize(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = Normalize(vv)
		}
		return out
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint:
		// uint на всех платформах, где собирается стенд (amd64/arm64),
		// 64-битный — теоретически может не влезть в int64, поэтому
		// проверка та же, что для uint64 ниже.
		if uint64(x) > math.MaxInt64 {
			return x
		}
		return int64(x)
	case uint64:
		// Переполнение не глотаем (находка I2): значение, которое не
		// влезает в int64, остаётся uint64 как есть — молчаливое
		// "int64(x)" завернуло бы его в отрицательное число, а это и
		// есть тот самый тихий баг, ради поиска которого стенд
		// существует.
		if x > math.MaxInt64 {
			return x
		}
		return int64(x)
	case float32:
		return truncateIfIntegral(float64(x))
	case float64:
		return truncateIfIntegral(x)
	default:
		return v
	}
}

// truncateIfIntegral сворачивает float в int64 только если это не
// меняет значение (число целое и помещается в int64). Для всего
// остального — дробной части или выхода за диапазон — исходный float64
// возвращается без изменений: молчаливое округление здесь было бы
// именно той подменой находки, которую ловит стенд (I2).
func truncateIfIntegral(x float64) any {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	if x != math.Trunc(x) {
		return x
	}
	if x < math.MinInt64 || x > math.MaxInt64 {
		return x
	}
	return int64(x)
}
