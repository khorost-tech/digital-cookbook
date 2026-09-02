// Package probe классифицирует исход одной пробы чтения.
package probe

import (
	"errors"

	"tech.khorost/serialization-formats/internal/codec"
)

// Classify различает исходы одной пробы.
//
// Отдельный «wrong» существует потому, что самое дорогое поведение
// форматов — не отказ, а молчаливое изменение данных: проверка
// совместимости пройдена, ошибки нет, значение другое.
//
// Отдельный «error» появился в круге правок 4 и отвечает на другой
// вопрос: ЧЬЯ это ошибка. «refused» — отказ ФОРМАТА, находка ради
// которой стенд существует; «error» — сбой САМОЙ ПРОБЫ (схему не
// удалось прочитать, разобрать, скомпилировать), наша поломка. Пока
// исход был один на двоих, сломанный стенд выглядел в таблице как
// поведение формата — правдоподобно и целиком неверно.
func Classify(got, want map[string]any, err error) string {
	switch {
	case errors.Is(err, codec.ErrProbeFailure):
		return "error"
	case err != nil:
		return "refused"
	case RecordsEqual(got, want):
		return "ok"
	default:
		return "wrong"
	}
}
