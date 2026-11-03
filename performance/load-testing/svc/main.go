// Целевой сервис стенда performance/load-testing — эталон, с которым сверяются
// генераторы нагрузки.
//
// Роль svc (по умолчанию):
//
//	GET  /work                 обслуживание S под семафором C: ёмкость C/S известна заранее
//	GET  /fast                 без работы — для потолка генераторов
//	GET  /work?leak=1          то же и LEAK байт в кэш без вытеснения (soak)
//	GET  /order                запрос к зависимости DEP_URL (ось хаоса), вариант MODE
//	POST /admin/config?s=10ms&c=8
//	POST /admin/stall?p=1s&t=10s   паузы: сервис не начинает обслуживание P секунд из каждых T
//	POST /admin/reset          обнулить статистику (и паузы, и кэш утечки)
//	GET  /admin/stats          принятые запросы, по секундам, время на сервере, окна пауз
//	GET  /healthz
//
// Роль dep — та же программа с ROLE=dep: /price отвечает за S_DEP.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type stall struct {
	start time.Time
	p, t  time.Duration
}

// until — до какого момента длится пауза, в которую попадает now (zero — не пауза).
// Окна: [start + k·T + (T−P), start + (k+1)·T).
func (s stall) until(now time.Time) time.Time {
	if s.t == 0 || now.Before(s.start) {
		return time.Time{}
	}
	off := now.Sub(s.start) % s.t
	if off < s.t-s.p {
		return time.Time{}
	}
	return now.Add(s.t - off)
}

type server struct {
	mu      sync.Mutex
	s       time.Duration
	sem     chan struct{}
	stall   stall
	started time.Time
	// по запросу: секунда прихода от started, время на сервере, код
	perSec  map[int]int
	donePS  map[int]int           // завершённые по секундам — пропускная способность (ось types)
	maxBy   map[int]time.Duration // худшее время на сервере по окнам прихода 100 мс (разгрузка после всплеска)
	lat     []time.Duration
	busy    []time.Duration // занятость слота /work: от конца ожидания (семафор и пауза) до освобождения слота
	codes   map[int]int
	first   time.Time // первый приход после сброса — от него истина считает сдвиг генератора
	leak    [][]byte
	leakB   int
	role    string
	dep     *http.Client
	depURL  string
	mode    string
	depWait time.Duration
	// последняя цена от зависимости — её отдаёт запасной ответ устойчивого варианта
	lastPrice string
	priceAt   time.Time
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func dur(k, def string) time.Duration {
	d, err := time.ParseDuration(env(k, def))
	if err != nil {
		log.Fatalf("%s: %v", k, err)
	}
	return d
}

func newServer() *server {
	c, _ := strconv.Atoi(env("C", "8"))
	leak, _ := strconv.Atoi(env("LEAK", "65536"))
	sv := &server{
		s: dur("S", "10ms"), sem: make(chan struct{}, c), role: env("ROLE", "svc"),
		depURL: env("DEP_URL", ""), mode: env("MODE", "naive"), depWait: dur("DEP_TIMEOUT", "150ms"), leakB: leak,
	}
	// наивный клиент ждёт зависимость сколько угодно; устойчивый — таймаут на попытку
	sv.dep = &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 256}}
	sv.reset()
	return sv
}

func (sv *server) reset() {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	sv.started = time.Now()
	sv.perSec, sv.codes, sv.lat, sv.leak, sv.busy = map[int]int{}, map[int]int{}, nil, nil, nil
	sv.donePS, sv.maxBy = map[int]int{}, map[int]time.Duration{}
	sv.first = time.Time{}
	sv.stall = stall{}
	runtime.GC()
}

func (sv *server) record(arrived time.Time, code int) {
	d := time.Since(arrived)
	sv.mu.Lock()
	if sv.first.IsZero() || arrived.Before(sv.first) {
		sv.first = arrived
	}
	sv.perSec[int(arrived.Sub(sv.started)/time.Second)]++
	sv.donePS[int(time.Since(sv.started)/time.Second)]++
	if b := int(arrived.Sub(sv.started) / (100 * time.Millisecond)); d > sv.maxBy[b] {
		sv.maxBy[b] = d
	}
	sv.lat = append(sv.lat, d)
	sv.codes[code]++
	sv.mu.Unlock()
}

// serve — обслуживание S под семафором; пауза задерживает начало обслуживания
// (уже начатое доходит до конца) — так же считает scripts/truth.py. Возвращает
// занятость слота: от конца ожидания (семафор и пауза) до освобождения слота —
// ровно тот интервал, который в формуле ёмкости C/S стоит на месте S.
func (sv *server) serve() time.Duration {
	sv.mu.Lock()
	sem := sv.sem // конфигурацию меняют только между прогонами
	sv.mu.Unlock()
	sem <- struct{}{}
	defer func() { <-sem }()
	for {
		sv.mu.Lock()
		u, s := sv.stall.until(time.Now()), sv.s
		sv.mu.Unlock()
		if u.IsZero() {
			began := time.Now()
			time.Sleep(s)
			return time.Since(began) // до defer — слот ещё занят
		}
		time.Sleep(time.Until(u))
	}
}

func (sv *server) work(w http.ResponseWriter, r *http.Request) {
	arrived := time.Now()
	busy := sv.serve()
	if r.URL.Query().Get("leak") == "1" {
		b := make([]byte, sv.leakB)
		for i := range b {
			b[i] = byte(i)
		}
		sv.mu.Lock()
		sv.leak = append(sv.leak, b)
		sv.mu.Unlock()
	}
	io.WriteString(w, "ok\n")
	sv.mu.Lock()
	sv.busy = append(sv.busy, busy)
	sv.mu.Unlock()
	sv.record(arrived, 200)
}

func (sv *server) fast(w http.ResponseWriter, r *http.Request) {
	arrived := time.Now()
	io.WriteString(w, "ok\n")
	sv.record(arrived, 200)
}

func (sv *server) callDep(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", sv.depURL+"/price", nil)
	resp, err := sv.dep.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("dep: %d", resp.StatusCode)
	}
	price := strings.TrimSpace(string(body))
	sv.mu.Lock()
	sv.lastPrice, sv.priceAt = price, time.Now()
	sv.mu.Unlock()
	return price, nil
}

// order — запрос к зависимости. naive: без таймаута, ошибка зависимости = 502.
// resilient: таймаут на попытку, одна повторная попытка, затем запасной ответ —
// последняя полученная цена; её возраст — в заголовке X-Fallback-Age-Ms.
func (sv *server) order(w http.ResponseWriter, r *http.Request) {
	arrived := time.Now()
	if sv.mode == "naive" {
		price, err := sv.callDep(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			sv.record(arrived, 502)
			return
		}
		io.WriteString(w, price+"\n")
		sv.record(arrived, 200)
		return
	}
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(r.Context(), sv.depWait)
		price, err := sv.callDep(ctx)
		cancel()
		if err == nil {
			io.WriteString(w, price+"\n")
			sv.record(arrived, 200)
			return
		}
	}
	sv.mu.Lock()
	price, at := sv.lastPrice, sv.priceAt
	sv.mu.Unlock()
	if at.IsZero() {
		http.Error(w, "no price", http.StatusServiceUnavailable)
		sv.record(arrived, 503)
		return
	}
	w.Header().Set("X-Fallback", "1")
	w.Header().Set("X-Fallback-Age-Ms", strconv.FormatInt(time.Since(at).Milliseconds(), 10))
	io.WriteString(w, price+"\n")
	sv.record(arrived, 299) // 299 — внутренний код «запасной ответ»; клиент видит 200
}

func (sv *server) price(w http.ResponseWriter, r *http.Request) {
	arrived := time.Now()
	sv.mu.Lock()
	s := sv.s
	sv.mu.Unlock()
	time.Sleep(s)
	io.WriteString(w, "42\n")
	sv.record(arrived, 200)
}

func pct(sorted []time.Duration, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q*float64(len(sorted))+0.999999) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return float64(sorted[i]) / 1e6
}

func mean(xs []time.Duration) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum time.Duration
	for _, x := range xs {
		sum += x
	}
	return float64(sum) / float64(len(xs)) / 1e6
}

// msSince — мс от started до t; nil, если момента не было.
func msSince(t, started time.Time) any {
	if t.IsZero() {
		return nil
	}
	return float64(t.Sub(started)) / 1e6
}

func (sv *server) stats(w http.ResponseWriter, r *http.Request) {
	sv.mu.Lock()
	lat := append([]time.Duration(nil), sv.lat...)
	busy := append([]time.Duration(nil), sv.busy...)
	per := map[string]int{}
	for k, v := range sv.perSec {
		per[strconv.Itoa(k)] = v
	}
	done := map[string]int{}
	for k, v := range sv.donePS {
		done[strconv.Itoa(k)] = v
	}
	maxBy := map[string]float64{}
	for k, v := range sv.maxBy {
		maxBy[strconv.Itoa(k)] = float64(v) / 1e6
	}
	codes := map[string]int{}
	for k, v := range sv.codes {
		codes[strconv.Itoa(k)] = v
	}
	st, started, leakN, s, c, first := sv.stall, sv.started, len(sv.leak), sv.s, cap(sv.sem), sv.first
	sv.mu.Unlock()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	sort.Slice(busy, func(i, j int) bool { return busy[i] < busy[j] })
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out := map[string]any{
		"role": sv.role, "s_ms": float64(s) / 1e6, "c": c, "mode": sv.mode,
		"requests": len(lat), "per_sec": per, "done_per_sec": done, "max_ms_by_100ms": maxBy, "codes": codes,
		"p50_ms": pct(lat, 0.50), "p90_ms": pct(lat, 0.90), "p99_ms": pct(lat, 0.99), "max_ms": pct(lat, 1),
		"busy_p50_ms": pct(busy, 0.50), "busy_mean_ms": mean(busy),
		"stall_p_ms": float64(st.p) / 1e6, "stall_t_ms": float64(st.t) / 1e6,
		"stall_offset_ms":  msSince(st.start, started),
		"first_arrival_ms": msSince(first, started),
		"heap_inuse_mb":    float64(ms.HeapInuse) / (1 << 20), "leak_items": leakN,
		"uptime_ms": float64(time.Since(started)) / 1e6,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (sv *server) admin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	switch r.URL.Path {
	case "/admin/reset":
		sv.reset()
	case "/admin/config":
		if v := q.Get("s"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			sv.mu.Lock()
			sv.s = d
			sv.mu.Unlock()
		}
		if v := q.Get("c"); v != "" {
			c, err := strconv.Atoi(v)
			if err != nil || c < 1 {
				http.Error(w, "c", 400)
				return
			}
			sv.mu.Lock()
			sv.sem = make(chan struct{}, c)
			sv.mu.Unlock()
		}
	case "/admin/stall":
		p, err1 := time.ParseDuration(q.Get("p"))
		t, err2 := time.ParseDuration(q.Get("t"))
		if err := errors.Join(err1, err2); err != nil || p >= t {
			http.Error(w, "p < t", 400)
			return
		}
		sv.mu.Lock()
		sv.stall = stall{start: time.Now(), p: p, t: t}
		sv.mu.Unlock()
	default:
		http.NotFound(w, r)
		return
	}
	io.WriteString(w, "ok\n")
}

func main() {
	sv := newServer()
	mux := http.NewServeMux()
	if sv.role == "dep" {
		mux.HandleFunc("GET /price", sv.price)
	} else {
		mux.HandleFunc("GET /work", sv.work)
		mux.HandleFunc("GET /fast", sv.fast)
		mux.HandleFunc("GET /order", sv.order)
	}
	mux.HandleFunc("/admin/", sv.admin)
	mux.HandleFunc("GET /admin/stats", sv.stats)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	addr := env("ADDR", ":8080")
	log.Printf("%s: %s S=%s C=%d mode=%s", sv.role, addr, sv.s, cap(sv.sem), sv.mode)
	log.Fatal((&http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
}
