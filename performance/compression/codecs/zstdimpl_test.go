package main

import (
	"bytes"
	"testing"
)

// Три реализации обязаны быть взаимно совместимы по формату: сжатое одной
// разжимается другой. Если это не так — сравнивать их «уровни» бессмысленно,
// и статья должна говорить о другом.
func TestZstdImplementationsInterop(t *testing.T) {
	src := bytes.Repeat([]byte("sku=tools-000042 category=tools color=red\n"), 5000)
	cs := ZstdCodecs()
	if len(cs) < 6 {
		t.Fatalf("кодеков %d — ожидалось не меньше 6 (три реализации × уровни)", len(cs))
	}
	for _, a := range cs {
		comp, err := a.Compress(src)
		if err != nil {
			t.Fatalf("%s ур.%d: сжатие: %v", a.Name(), a.Level(), err)
		}
		for _, b := range cs {
			back, err := b.Decompress(comp)
			if err != nil {
				t.Fatalf("%s разжал вывод %s ур.%d с ошибкой: %v", b.Name(), a.Name(), a.Level(), err)
			}
			if !bytes.Equal(src, back) {
				t.Fatalf("%s не воспроизвёл данные, сжатые %s ур.%d", b.Name(), a.Name(), a.Level())
			}
		}
	}
}
