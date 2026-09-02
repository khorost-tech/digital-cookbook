package codec

import (
	"math"
	"reflect"
	"testing"
)

// Проверяем именно то расхождение, которое стенд обязан не путать с
// реальной несовместимостью схем: одно и то же число, разные Go-типы.
func TestNormalizeUnifiesIntegerTypes(t *testing.T) {
	in := map[string]any{
		"a": int(1),
		"b": int32(2),
		"c": int64(3),
		"d": uint32(4),
		"e": float64(5), // так decoding/json отдаёт числа
		"nested": map[string]any{
			"f": int16(6),
		},
		"list": []any{int8(7), int64(8)},
		"s":    "строка не трогается",
	}
	want := map[string]any{
		"a": int64(1),
		"b": int64(2),
		"c": int64(3),
		"d": int64(4),
		"e": int64(5),
		"nested": map[string]any{
			"f": int64(6),
		},
		"list": []any{int64(7), int64(8)},
		"s":    "строка не трогается",
	}
	got := Normalize(in)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize() = %#v, want %#v", got, want)
	}
}

// I2 ревью: усечение float обязано быть условным. Если бы Normalize
// безусловно резал дробную часть, смена типа поля long -> double
// превращала бы 1.5 в 1 незаметно для стенда — то есть заметала бы
// ровно тот класс тихой порчи данных, ради обнаружения которого стенд
// написан.
func TestNormalizeDoesNotTruncateFractionalFloats(t *testing.T) {
	got := Normalize(1.5)
	if got == int64(1) {
		t.Fatalf("Normalize(1.5) = %#v, дробная часть потеряна", got)
	}
	if got != 1.5 {
		t.Fatalf("Normalize(1.5) = %#v, want 1.5 без изменений", got)
	}
}

// Целый float — по-прежнему int64: разница типов между hamba/avro,
// dynamicpb и encoding/json остаётся тем единственным случаем, который
// Normalize обязана стирать.
func TestNormalizeStillCollapsesIntegralFloats(t *testing.T) {
	if got := Normalize(float64(5)); got != int64(5) {
		t.Fatalf("Normalize(5.0) = %#v, want int64(5)", got)
	}
}

// uint64, не влезающий в int64, не должен молча завернуться в
// отрицательное число — это тоже тихая порча, а не нормализация.
func TestNormalizeDoesNotWrapOverflowingUint64(t *testing.T) {
	huge := uint64(math.MaxInt64) + 1
	got := Normalize(huge)
	if got != huge {
		t.Fatalf("Normalize(%d) = %#v, значение обязано остаться неизменным", huge, got)
	}
}
