// Probe — клиент для опытов стенда изнутри сети compose: одновременный залп запросов.
//
//	probe -url http://service:8080/abc -n 200 -c 200
//	probe -url http://service:8080/abc,http://service:8080/def -d 60s -c 8 -keepalive=false
//
// Все c горутин ждут общий старт (закрытие канала), затем делят n запросов — или, с -d,
// шлют запросы подряд, пока не истечёт время (так считают ошибки за окно выкатки).
// Несколько адресов через запятую обходятся по кругу; -keepalive=false — новое соединение на
// каждый запрос, как у множества разных клиентов.
// Печатает JSON: коды ответов (0 — сетевая ошибка), виды сетевых ошибок и медиану длительности.
package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

func main() {
	url := flag.String("url", "", "адрес")
	n := flag.Int("n", 200, "запросов")
	c := flag.Int("c", 200, "одновременно")
	d := flag.Duration("d", 0, "длительность вместо числа запросов")
	keep := flag.Bool("keepalive", true, "переиспользовать соединения")
	flag.Parse()
	urls := strings.Split(*url, ",")
	client := &http.Client{Timeout: 10 * time.Second,
		Transport:     &http.Transport{MaxIdleConnsPerHost: *c, MaxConnsPerHost: *c, DisableKeepAlives: !*keep},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	start := make(chan struct{})
	var jobs chan int
	if *d == 0 {
		// залп: все запросы в очереди до общего старта — иначе он растянется во времени
		jobs = make(chan int, *n)
		for i := 0; i < *n; i++ {
			jobs <- i
		}
		close(jobs)
	} else {
		jobs = make(chan int, 1024)
		go func() {
			<-start
			deadline := time.Now().Add(*d)
			for i := 0; time.Now().Before(deadline); i++ {
				jobs <- i
			}
			close(jobs)
		}()
	}
	var mu sync.Mutex
	codes := map[int]int{}
	errs := map[string]int{}
	var lat []float64
	var wg sync.WaitGroup
	for w := 0; w < *c; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range jobs {
				t := time.Now()
				code, kind := 0, ""
				if resp, err := client.Get(urls[i%len(urls)]); err == nil {
					code = resp.StatusCode
					resp.Body.Close()
				} else {
					kind = errKind(err)
				}
				mu.Lock()
				codes[code]++
				if kind != "" {
					errs[kind]++
				}
				lat = append(lat, time.Since(t).Seconds())
				mu.Unlock()
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(start)
	wg.Wait()
	sort.Float64s(lat)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"codes": codes, "errors": errs, "median_s": lat[len(lat)/2]})
}

// errKind сводит сетевую ошибку к виду: по нему видно, на каком шаге остановки пода её получили.
func errKind(err error) string {
	s := strings.ToLower(err.Error())
	for _, k := range []string{"connection refused", "connection reset", "eof", "timeout", "no such host"} {
		if strings.Contains(s, k) {
			return k
		}
	}
	return "other"
}
