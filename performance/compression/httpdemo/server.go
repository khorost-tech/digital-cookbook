package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

// serve отдаёт один и тот же корпус в трёх кодировках. Тело сжимается на
// каждый запрос, а не кэшируется: измеряется путь, на котором сжатие входит в
// стоимость ответа, — именно он описывается формулой режима «горячий
// транспорт».
func serve(addr, profilePath string) error {
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return err
	}

	http.HandleFunc("/products", func(w http.ResponseWriter, r *http.Request) {
		enc := r.URL.Query().Get("encoding")

		// bwmbit — необязательная полоса в Мбит/с для sweep-сценария
		// (ops/netem-sweep.sh). Ограничивает темп записи ТЕЛА ответа на
		// уровне приложения; см. комментарий в throttle.go про то, почему
		// не tc netem/tbf. Заголовки полосой не ограничиваются — измеряется
		// передача тела, как и было бы при ограничении на интерфейсе.
		var out io.Writer = w
		if bwStr := r.URL.Query().Get("bwmbit"); bwStr != "" {
			bw, err := strconv.ParseFloat(bwStr, 64)
			if err != nil || bw <= 0 {
				http.Error(w, "некорректный bwmbit "+bwStr, http.StatusBadRequest)
				return
			}
			out = newThrottledWriter(w, bw)
		}

		switch enc {
		case "", "identity":
			w.Header().Set("Content-Encoding", "identity")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			out.Write(data)
		case "gzip":
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
			zw.Write(data)
			zw.Close()
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
			out.Write(buf.Bytes())
		case "zstd":
			var buf bytes.Buffer
			zw, _ := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.SpeedDefault))
			zw.Write(data)
			zw.Close()
			w.Header().Set("Content-Encoding", "zstd")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
			out.Write(buf.Bytes())
		default:
			http.Error(w, "неизвестная кодировка "+enc, http.StatusBadRequest)
		}
	})

	fmt.Fprintf(os.Stderr, "сервер на %s, корпус %d байт\n", addr, len(data))
	return http.ListenAndServe(addr, nil)
}
