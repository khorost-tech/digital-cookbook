// Command bench — минимальный нагрузчик для стенда Keycloak (Task 7).
//
// Шлёт N параллельных воркеров × M запросов на защищённый эндпоинт resource
// server'а (по умолчанию http://localhost:8081/me) с фиксированным валидным
// Bearer-токеном и меряет latency (p50/p95/p99), throughput и распределение
// статус-кодов. Сам факт «сколько запросов дошло до Keycloak» меряется СНАРУЖИ
// (bench.sh снимает счётчик introspection из /metrics Keycloak до и после):
// нагрузчик намеренно не знает про Keycloak и работает одинаково в обоих режимах
// resource server'а (AUTH_MODE=jwks|introspect) — разница видна именно в дельте
// метрики IdP и в latency.
//
// Зависимостей нет (только stdlib): нагрузчик собирается и в оффлайне.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type config struct {
	url       string
	tokenFile string
	token     string
	workers   int
	perWorker int
	warmup    int
	timeout   time.Duration
	label     string
	jsonOut   string
}

func main() {
	cfg := parseFlags()
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.url, "url", "http://localhost:8081/me", "защищённый эндпоинт resource server'а")
	flag.StringVar(&cfg.tokenFile, "token-file", "", "файл с access-токеном (одна строка)")
	flag.StringVar(&cfg.token, "token", "", "access-токен строкой (альтернатива -token-file)")
	flag.IntVar(&cfg.workers, "workers", 50, "число параллельных воркеров (N)")
	flag.IntVar(&cfg.perWorker, "requests", 2000, "запросов на воркер (M); всего = N*M")
	flag.IntVar(&cfg.warmup, "warmup", 200, "прогревочных запросов до замера (артефакт Docker Desktop: первые коннекты к :8081 иногда 000)")
	flag.DurationVar(&cfg.timeout, "timeout", 10*time.Second, "таймаут одиночного HTTP-запроса")
	flag.StringVar(&cfg.label, "label", "run", "метка прогона (напр. jwks|introspect) — попадает в вывод")
	flag.StringVar(&cfg.jsonOut, "json-out", "", "куда писать машинный JSON результата: '-' = stdout, иначе путь файла (дозапись JSONL)")
	flag.Parse()
	return cfg
}

func run(cfg config) error {
	token, err := loadToken(cfg)
	if err != nil {
		return err
	}

	// Пул соединений под число воркеров: без keep-alive каждый запрос открывал бы
	// новый TCP+, латентность мерялась бы по установке соединений, а не по обработке.
	transport := &http.Transport{
		MaxIdleConns:        cfg.workers * 2,
		MaxIdleConnsPerHost: cfg.workers * 2,
		IdleConnTimeout:     30 * time.Second,
	}
	client := &http.Client{Timeout: cfg.timeout, Transport: transport}
	defer client.CloseIdleConnections()

	warmupPhase(client, cfg, token)

	total := cfg.workers * cfg.perWorker
	fmt.Fprintf(os.Stderr, "[%s] warmup done, running %d workers × %d = %d requests → %s\n",
		cfg.label, cfg.workers, cfg.perWorker, total, cfg.url)

	// Каждый воркер копит свои latency в отдельный слайс (без общей блокировки на
	// горячем пути), в конце сливаем.
	perWorkerLat := make([][]time.Duration, cfg.workers)
	var okCount, errCount int64
	statusCounts := &statusTally{}

	var wg sync.WaitGroup
	start := time.Now()
	for w := range cfg.workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			lat := make([]time.Duration, 0, cfg.perWorker)
			for range cfg.perWorker {
				code, d := doRequest(client, cfg.url, token, cfg.timeout)
				lat = append(lat, d)
				statusCounts.add(code)
				if code == http.StatusOK {
					atomic.AddInt64(&okCount, 1)
				} else {
					atomic.AddInt64(&errCount, 1)
				}
			}
			perWorkerLat[w] = lat
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)

	all := make([]time.Duration, 0, total)
	for _, l := range perWorkerLat {
		all = append(all, l...)
	}
	res := summarize(cfg.label, all, elapsed, okCount, errCount, statusCounts.snapshot())

	// Человекочитаемая таблица — всегда в stderr (диагностика/прогресс). Машинный
	// JSON — в stdout при -json-out=- (bench.sh парсит его) или дозаписью в файл.
	if _, err := io.WriteString(os.Stderr, res.render()); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	switch cfg.jsonOut {
	case "":
		// no machine output requested
	case "-":
		if err := json.NewEncoder(os.Stdout).Encode(res); err != nil {
			return fmt.Errorf("encode json: %w", err)
		}
	default:
		if err := res.appendJSON(cfg.jsonOut); err != nil {
			return fmt.Errorf("write json-out: %w", err)
		}
	}
	return nil
}

func loadToken(cfg config) (string, error) {
	if cfg.token != "" {
		return strings.TrimSpace(cfg.token), nil
	}
	// Env-fallback: удобно для контейнерного запуска (docker compose run -e BENCH_TOKEN=…),
	// чтобы токен не светился в argv.
	if env := strings.TrimSpace(os.Getenv("BENCH_TOKEN")); env != "" {
		return env, nil
	}
	if cfg.tokenFile == "" {
		return "", fmt.Errorf("нужен -token, -token-file или env BENCH_TOKEN")
	}
	b, err := os.ReadFile(cfg.tokenFile)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", fmt.Errorf("token file %q пуст", cfg.tokenFile)
	}
	return t, nil
}

// warmupPhase гоняет прогревочные запросы, игнорируя результат: на Docker Desktop
// (Windows) первые коннекты к опубликованному порту :8081 изредка возвращают 000.
func warmupPhase(client *http.Client, cfg config, token string) {
	for range cfg.warmup {
		_, _ = doRequest(client, cfg.url, token, cfg.timeout)
	}
}

// doRequest выполняет один GET с Bearer-токеном, возвращает статус-код (0 при
// сетевой ошибке) и полное время round-trip (включая чтение тела).
func doRequest(client *http.Client, url, token string, timeout time.Duration) (int, time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, time.Since(start)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return 0, time.Since(start)
	}
	// Дочитываем и закрываем тело, иначе соединение не вернётся в пул keep-alive.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, time.Since(start)
}

type statusTally struct {
	mu sync.Mutex
	m  map[int]int64
}

func (s *statusTally) add(code int) {
	s.mu.Lock()
	if s.m == nil {
		s.m = make(map[int]int64)
	}
	s.m[code]++
	s.mu.Unlock()
}

func (s *statusTally) snapshot() map[int]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int]int64, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out
}

type result struct {
	Label      string        `json:"label"`
	Total      int           `json:"total"`
	OK         int64         `json:"ok"`
	Errors     int64         `json:"errors"`
	Status     map[int]int64 `json:"status"`
	ElapsedMS  float64       `json:"elapsed_ms"`
	Throughput float64       `json:"throughput_rps"`
	P50MS      float64       `json:"p50_ms"`
	P95MS      float64       `json:"p95_ms"`
	P99MS      float64       `json:"p99_ms"`
	MinMS      float64       `json:"min_ms"`
	MaxMS      float64       `json:"max_ms"`
	MeanMS     float64       `json:"mean_ms"`
}

func summarize(label string, lat []time.Duration, elapsed time.Duration, ok, errs int64, status map[int]int64) result {
	r := result{
		Label:     label,
		Total:     len(lat),
		OK:        ok,
		Errors:    errs,
		Status:    status,
		ElapsedMS: ms(elapsed),
	}
	if elapsed > 0 {
		r.Throughput = float64(len(lat)) / elapsed.Seconds()
	}
	if len(lat) == 0 {
		return r
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	r.P50MS = ms(percentile(lat, 50))
	r.P95MS = ms(percentile(lat, 95))
	r.P99MS = ms(percentile(lat, 99))
	r.MinMS = ms(lat[0])
	r.MaxMS = ms(lat[len(lat)-1])
	var sum time.Duration
	for _, d := range lat {
		sum += d
	}
	r.MeanMS = ms(sum / time.Duration(len(lat)))
	return r
}

// percentile — nearest-rank по отсортированному слайсу.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p*len(sorted) + 99) / 100 // ceil(p/100 * n)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

func (r result) render() string {
	return fmt.Sprintf(
		"\n=== bench: %s ===\n"+
			"requests total : %d (ok=%d errors=%d)\n"+
			"status codes   : %s\n"+
			"elapsed        : %.1f ms\n"+
			"throughput     : %.0f req/s\n"+
			"latency p50    : %.2f ms\n"+
			"latency p95    : %.2f ms\n"+
			"latency p99    : %.2f ms\n"+
			"latency min/max: %.2f / %.2f ms\n"+
			"latency mean   : %.2f ms\n",
		r.Label, r.Total, r.OK, r.Errors, formatStatus(r.Status),
		r.ElapsedMS, r.Throughput, r.P50MS, r.P95MS, r.P99MS,
		r.MinMS, r.MaxMS, r.MeanMS)
}

func formatStatus(m map[int]int64) string {
	codes := make([]int, 0, len(m))
	for c := range m {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	parts := make([]string, 0, len(codes))
	for _, c := range codes {
		parts = append(parts, fmt.Sprintf("%d=%d", c, m[c]))
	}
	return strings.Join(parts, " ")
}

func (r result) appendJSON(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	return enc.Encode(r)
}
