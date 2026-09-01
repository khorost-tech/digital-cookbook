// Тег сборки здесь не украшение. Под GOEXPERIMENT=nojsonv2 пакет
// encoding/json/jsontext исключается build-ограничениями точно так же, как
// encoding/json/v2 (см. bench_v2_test.go) — это тот же самый механизм отката
// на прежний движок, просто затрагивает соседний пакет. Файл с импортом
// jsontext в режиме отката не собрался бы вовсе и утащил бы за собой весь
// тестовый пакет, включая уже работающие бенчи v1. Поэтому потоковый замер
// живёт в отдельном файле с тегом, а не в bench_v1_test.go.

//go:build goexperiment.jsonv2

package jsonv2bench_test

import (
	"bytes"
	stdjson "encoding/json"
	"encoding/json/jsontext"
	"runtime"
	"testing"

	"tech.khorost/json-v2-cookbook/payload"
)

// allocDelta — сколько байт всего выделено за время работы f, и что именно f
// вернула. Результат возвращается вместе с дельтой намеренно: если бы проверка
// полноты чтения и замер расхода шли через два разных вызова f, формально
// проверялось бы не то, что меряется — совпадение чисел, а не гарантия того,
// что вход дочитан до конца именно в замеряемом вызове.
func allocDelta[T any](f func() T) (uint64, T) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	result := f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc, result
}

func decodeAllV1(data []byte) int {
	dec := stdjson.NewDecoder(bytes.NewReader(data))
	n := 0
	for {
		var v map[string]any
		if err := dec.Decode(&v); err != nil {
			return n
		}
		n++
	}
}

func decodeAllJSONText(data []byte) int {
	dec := jsontext.NewDecoder(bytes.NewReader(data))
	n := 0
	for {
		val, err := dec.ReadValue()
		if err != nil {
			return n
		}
		if len(val) > 0 {
			n++
		}
	}
}

// Проверяем не абсолютные числа, а МАСШТАБИРОВАНИЕ: во сколько раз растёт расход
// при десятикратном входе. Абсолютные значения зависят от машины, отношение — от
// устройства кодека.
func TestStreamMemoryScaling(t *testing.T) {
	small := payload.NDJSON(1000)
	large := payload.NDJSON(10000)

	// Проверяем полноту чтения РОВНО на том вызове, чей расход учитывается в
	// замере — не на соседнем прогоне "для разогрева". Если вход оборвётся на
	// сотой строке, расход закономерно не вырастет, и красивое x1.00 получится
	// из неполного чтения. Отличить это от честного результата можно только
	// сверив число прочитанных значений с ожидаемым на каждом измеряемом вызове.
	v1small, nV1small := allocDelta(func() int { return decodeAllV1(small) })
	if nV1small != 1000 {
		t.Fatalf("v1 прочитал %d строк из 1000 (small) — вход или парсер не тот", nV1small)
	}

	v1large, nV1large := allocDelta(func() int { return decodeAllV1(large) })
	if nV1large != 10000 {
		t.Fatalf("v1 прочитал %d строк из 10000 (large) — вход или парсер не тот", nV1large)
	}

	jtSmall, nJTSmall := allocDelta(func() int { return decodeAllJSONText(small) })
	if nJTSmall != 1000 {
		t.Fatalf("jsontext прочитал %d значений из 1000 (small) — вход или парсер не тот", nJTSmall)
	}

	jtLarge, nJTLarge := allocDelta(func() int { return decodeAllJSONText(large) })
	if nJTLarge != 10000 {
		t.Fatalf("jsontext прочитал %d значений из 10000 (large) — вход или парсер не тот", nJTLarge)
	}

	if v1small == 0 || jtSmall == 0 {
		t.Fatal("нулевой расход — замер не состоялся")
	}

	t.Logf("v1 Decoder: %d -> %d байт (x%.2f)", v1small, v1large, float64(v1large)/float64(v1small))
	t.Logf("jsontext:   %d -> %d байт (x%.2f)", jtSmall, jtLarge, float64(jtLarge)/float64(jtSmall))
}

func BenchmarkStreamV1Decoder(b *testing.B) {
	data := payload.NDJSON(10000)
	b.ReportAllocs()
	for b.Loop() {
		if n := decodeAllV1(data); n != 10000 {
			b.Fatalf("прочитано %d строк из 10000", n)
		}
	}
}

func BenchmarkStreamJSONText(b *testing.B) {
	data := payload.NDJSON(10000)
	b.ReportAllocs()
	for b.Loop() {
		if n := decodeAllJSONText(data); n != 10000 {
			b.Fatalf("прочитано %d значений из 10000", n)
		}
	}
}
