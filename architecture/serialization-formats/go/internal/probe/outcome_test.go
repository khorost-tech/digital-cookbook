package probe

import (
	"errors"
	"fmt"
	"testing"

	"tech.khorost/serialization-formats/internal/codec"
)

// Круг правок 4, вторая правка. «Формат отказался читать» и «проба не
// смогла запуститься» — разные вещи, а исход был один на двоих. Чем это
// опасно, показал баг с путями из круга 3: пока jsonschema не могла
// скомпилировать схему, ВСЯ колонка JSON Schema выглядела бы как
// «формат отказал» — правдоподобная таблица, целиком неверная. Задачи 8
// и 12 иначе прочитают нашу поломку как поведение формата.
func TestClassifySeparatesFormatRefusalFromProbeFailure(t *testing.T) {
	want := map[string]any{"id": int64(1)}

	formatRefusal := errors.New("схемы писателя и читателя несовместимы")
	if got := Classify(nil, want, formatRefusal); got != "refused" {
		t.Fatalf("отказ формата = %q, ждали refused", got)
	}

	probeFailure := fmt.Errorf("не смогли скомпилировать схему: %w", codec.ErrProbeFailure)
	if got := Classify(nil, want, probeFailure); got != "error" {
		t.Fatalf("сбой самой пробы = %q, ждали error", got)
	}
}

// Сбой пробы опознаётся по цепочке обёрток, а не по тексту сообщения:
// текст пишет каждый кодек по-своему, и сравнение строк развалилось бы
// на первой же правке формулировки.
func TestClassifyRecognizesWrappedProbeFailure(t *testing.T) {
	err := fmt.Errorf("avro: схема писателя x.avsc: %w",
		fmt.Errorf("прочитать файл: %w", codec.ErrProbeFailure))
	if got := Classify(nil, nil, err); got != "error" {
		t.Fatalf("обёрнутый сбой пробы = %q, ждали error", got)
	}
}
