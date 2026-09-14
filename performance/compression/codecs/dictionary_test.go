package main

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// RunDictionary сам по себе меряет только размеры (Compress), но никогда не
// вызывает Decompress — тихо сломанное декодирование осталось бы незамеченным
// в сценарии, при этом числа размеров выглядели бы совершенно нормально.
// zstdWithDict.Decompress использует WithDecoderDicts([]byte{z.dict}) —
// декодер обязан восстановить исходные байты именно через словарь, а не
// потому что сообщение и без словаря помещается в блок.
func TestDictionaryCodecRoundTrip(t *testing.T) {
	// Выборка генерируется, а не выписывается руками: BuildDict в
	// klauspost/compress v1.19.0 ПАНИКУЕТ (integer divide by zero,
	// zstd/dict.go:431) на вырожденно малом входе — три сообщения его
	// роняют. Это ловушка библиотеки, а не свойство словарного сжатия:
	// рабочий сценарий обучает словарь на 20 000 сообщениях и отрабатывает
	// штатно. Здесь берём выборку, заведомо достаточную для обучения.
	cats := []string{"books", "garden", "auto", "sport", "kitchen", "tools"}
	samples := make([][]byte, 0, 512)
	for i := 1; i <= 512; i++ {
		cat := cats[i%len(cats)]
		samples = append(samples, []byte(fmt.Sprintf(
			`{"category":%q,"event":"product_viewed","product_id":%d,"sku":"%s-%06d","ts":%d}`,
			cat, i, cat, i, 1771484400+int64(i)*3600)))
	}

	history := buildHistory(samples, dictHistoryCap)
	dict, err := zstd.BuildDict(zstd.BuildDictOptions{
		ID:       1,
		Contents: samples,
		History:  history,
		Offsets:  [3]int{1, 4, 8},
	})
	if err != nil {
		t.Fatalf("BuildDict: %v", err)
	}
	if len(dict) == 0 {
		t.Fatal("словарь пуст")
	}

	c := zstdWithDict{level: 3, dict: dict}
	// Проверяем и обучающие сообщения, и то, что не входило в обучающую
	// выборку (RunDictionary замеряет именно вторую группу) — словарь не
	// должен восстанавливать только то, чем сам обучен.
	unseen := []byte(`{"category":"sport","event":"product_viewed","product_id":999,"sku":"sport-000999","ts":1780000000}`)
	for _, src := range append(append([][]byte{}, samples...), unseen) {
		comp, err := c.Compress(src)
		if err != nil {
			t.Fatalf("Compress(%q): %v", src, err)
		}
		back, err := c.Decompress(comp)
		if err != nil {
			t.Fatalf("Decompress(%q): %v", src, err)
		}
		if !bytes.Equal(src, back) {
			t.Fatalf("round-trip не совпал: src=%q back=%q", src, back)
		}
	}
}

// buildHistory обязана обрезать по cap, а не по числу сообщений: без этого
// dictHistoryCap ничего бы не гарантировал при иначе устроенном входе.
func TestBuildHistoryRespectsCap(t *testing.T) {
	samples := make([][]byte, 0, 100)
	for i := 0; i < 100; i++ {
		samples = append(samples, bytes.Repeat([]byte("x"), 500))
	}
	h := buildHistory(samples, 1000)
	if len(h) > 1000 {
		t.Fatalf("history длиной %d байт превышает cap 1000", len(h))
	}
	if len(h) == 0 {
		t.Fatal("history пуста")
	}
}
