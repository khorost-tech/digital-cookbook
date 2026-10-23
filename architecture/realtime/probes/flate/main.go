// Проба к оси 1: сколько байт даёт compress/flate стандартной библиотеки Go на
// сообщение размером с событие стенда, если сбрасывать поток после каждого
// сообщения (так делают и gzip потока SSE, и permessage-deflate). Три уровня.
// Итог — строка JSON на уровень.
package main

import (
	"bytes"
	"compress/flate"
	"fmt"
	"strings"
)

func msg(i int) []byte {
	p := fmt.Sprintf("p%d-", i)
	p += strings.Repeat("x", 64-len(p))
	return []byte(fmt.Sprintf(`{"id":%d,"topic":"wire-123456789","payload":"%s"}`, i, p))
}

func main() {
	for _, lvl := range []int{flate.BestSpeed, flate.DefaultCompression, flate.BestCompression} {
		var buf bytes.Buffer
		w, _ := flate.NewWriter(&buf, lvl)
		var at20 int
		for i := 1; i <= 120; i++ {
			w.Write(msg(i))
			w.Flush()
			if i == 20 {
				at20 = buf.Len()
			}
		}
		fmt.Printf("{\"kind\":\"flate\",\"level\":%d,\"bytes_per_message\":%.1f,\"raw\":%d}\n",
			lvl, float64(buf.Len()-at20)/100, len(msg(50)))
	}
}
