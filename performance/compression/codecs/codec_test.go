package main

import (
	"bytes"
	"testing"
)

// Round-trip на каждом кодеке — защита от самого дорогого класса ошибок:
// замер, где «сжатие» на деле портит данные, выглядит как отличный результат.
func TestCodecRoundTrip(t *testing.T) {
	src := bytes.Repeat([]byte("product_viewed sku=tools-000042 category=tools\n"), 2000)
	for _, c := range AllCodecs() {
		comp, err := c.Compress(src)
		if err != nil {
			t.Fatalf("%s уровень %d: сжатие: %v", c.Name(), c.Level(), err)
		}
		back, err := c.Decompress(comp)
		if err != nil {
			t.Fatalf("%s уровень %d: разжатие: %v", c.Name(), c.Level(), err)
		}
		if !bytes.Equal(src, back) {
			t.Fatalf("%s уровень %d: данные после round-trip не совпали", c.Name(), c.Level())
		}
	}
}

func TestAllCodecsNonEmpty(t *testing.T) {
	cs := AllCodecs()
	if len(cs) < 8 {
		t.Fatalf("кодеков %d — ожидалось не меньше 8 (четыре семейства с уровнями)", len(cs))
	}
	seen := map[string]bool{}
	for _, c := range cs {
		key := c.Name() + "/" + string(rune('0'+c.Level()))
		if seen[key] {
			t.Fatalf("дубль кодека: %s", key)
		}
		seen[key] = true
	}
}
