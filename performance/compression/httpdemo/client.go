package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"
)

type wireResult struct {
	Encoding  string
	WireBytes int64
	TotalMs   []float64
}

// fetch меряет полное время ответа и число байт, реально прошедших по
// проводу. Тело читается до конца: без этого замер закончился бы на
// заголовках и измерял бы не передачу, а установление соединения.
func fetch(url, encoding string, reps int, bwmbit float64) (wireResult, error) {
	res := wireResult{Encoding: encoding}
	// DisableCompression обязателен: по умолчанию Transport сам добавляет
	// Accept-Encoding: gzip к каждому запросу (если заголовок не задан явно)
	// и прозрачно разжимает ответ, если сервер вернул Content-Encoding:
	// gzip, — независимо от того, что кодировка выбрана параметром запроса.
	// Без этой опции resp.Uncompressed становится true, заголовки
	// Content-Encoding/Content-Length вырезаются транспортом, а
	// io.Copy читает уже разжатые байты: для кодировки "gzip" WireBytes
	// совпал бы с размером identity, и по проводу измерялось бы не то, что
	// реально передано.
	client := &http.Client{
		Timeout:   10 * time.Minute,
		Transport: &http.Transport{DisableCompression: true},
	}
	q := "?encoding=" + encoding
	if bwmbit > 0 {
		// bwmbit пробрасывается на сервер: он ограничивает темп записи тела
		// ответа (app-layer throttle, см. httpdemo/throttle.go — почему не
		// tc netem/tbf в этом окружении).
		q += fmt.Sprintf("&bwmbit=%g", bwmbit)
	}
	for i := 0; i < reps; i++ {
		t0 := time.Now()
		// Заголовок Accept-Encoding не используется: кодировка задаётся
		// параметром запроса, иначе Go-клиент прозрачно разожмёт gzip сам и
		// байты по проводу окажутся неизмеримы.
		resp, err := client.Get(url + q)
		if err != nil {
			return res, err
		}
		n, err := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil {
			return res, err
		}
		res.TotalMs = append(res.TotalMs, float64(time.Since(t0).Microseconds())/1000)
		if res.WireBytes != 0 && res.WireBytes != n {
			return res, fmt.Errorf("кодировка %s: размер тела менялся между повторами: %d и %d",
				encoding, res.WireBytes, n)
		}
		res.WireBytes = n
	}
	return res, nil
}

func medianOf(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s) == 0 {
		return 0
	}
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func runClient(url string, reps int, bwmbit float64) error {
	for _, enc := range []string{"identity", "gzip", "zstd"} {
		r, err := fetch(url, enc, reps, bwmbit)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%-10s по проводу=%d байт  медиана=%.1f мс  повторы=%v\n",
			r.Encoding, r.WireBytes, medianOf(r.TotalMs), r.TotalMs)
		fmt.Printf("%s,%d,%.3f,%d\n", r.Encoding, r.WireBytes, medianOf(r.TotalMs), reps)
	}
	return nil
}
