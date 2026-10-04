package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

// Latency — перцентили, а не среднее: среднее по латентности скрывает ровно то,
// что интересно (хвост), и сравнивать по нему накладные расходы бессмысленно.
type Latency struct {
	P50 float64 `json:"p50_ms"`
	P90 float64 `json:"p90_ms"`
	P99 float64 `json:"p99_ms"`
	Max float64 `json:"max_ms"`
}

type Result struct {
	Run        string         `json:"run"`
	Seed       int64          `json:"seed"`
	Requests   int            `json:"requests"`
	Concurrent int            `json:"concurrent"`
	Duration   string         `json:"duration"`
	Expected   map[string]int `json:"expected_by_status"`
	Actual     map[string]int `json:"actual_by_status"`
	Mismatch   int            `json:"mismatch"`
	Transport  int            `json:"transport_errors"`
	Latency    Latency        `json:"latency"`
	// Латентность считается ТОЛЬКО по успешным быстрым запросам: медленный SKU
	// с pg_sleep(0.15) иначе перетягивает перцентили на себя, и разница между
	// режимами телеметрии тонет в задержке базы.
	LatencyNote string `json:"latency_note"`
}

func main() {
	target := flag.String("target", "http://go-frontend:8080", "адрес go-frontend")
	requests := flag.Int("requests", 200, "сколько запросов выполнить")
	concurrency := flag.Int("concurrency", 4, "сколько параллельных отправителей")
	seed := flag.Int64("seed", 42, "seed сценария: одинаковый seed — одинаковая последовательность")
	// Итог печатается в stdout, а не пишется в файл внутри контейнера: путь,
	// переданный аргументом из Git Bash, MSYS переписывает в путь Windows
	// (/out/x.json превращался в C:/Program Files/Git/out/x.json), и генератор
	// рапортовал «итог не записан» при формально успешном прогоне.
	asJSON := flag.Bool("json", false, "печатать только итог в JSON (для скриптов)")
	// Метка прогона едет в заголовке X-Load-Run и попадает атрибутом в спан.
	// Без неё точно посчитать трейсы одного прогона нельзя: поиск Tempo
	// округляет окно времени до границ блоков и захватывает соседние прогоны.
	run := flag.String("run", "", "метка прогона: попадает в атрибут спана load.run")
	// Режим для алертов статьи 5: только отказы сервера. На обычном плане доля
	// ошибок около 10%, и burn rate до порога 14.4 не доходит.
	errorsOnly := flag.Bool("errors-only", false, "нагрузка из одних 502: для проверки SLO-алертов")
	// Режим для замера wide events (статья 8): только успешные заказы. Событие
	// «заказ создан» на 404 и 502 не пишется, поэтому смешанная нагрузка сделала
	// бы сравнение объёма событий с числом запросов бессмысленным.
	successOnly := flag.Bool("success-only", false, "нагрузка из одних успешных заказов: для замера объёма событий")
	flag.Parse()

	if *errorsOnly && *successOnly {
		fmt.Fprintln(os.Stderr, "-errors-only и -success-only взаимно исключают друг друга")
		os.Exit(2)
	}

	plan := BuildPlan(*requests, *seed)
	if *errorsOnly {
		plan = BuildErrorPlan(*requests, *seed)
	}
	if *successOnly {
		plan = BuildSuccessPlan(*requests, *seed)
	}
	expected := Expect(plan)
	if !*asJSON {
		fmt.Printf("план: %s\n", expected)
	}

	started := time.Now()
	actual, transport, latencies := execute(*target, *run, plan, *concurrency)
	elapsed := time.Since(started)

	res := Result{
		Run:        *run,
		Seed:       *seed,
		Requests:   len(plan),
		Concurrent: *concurrency,
		Duration:   elapsed.Round(time.Millisecond).String(),
		Expected:   keysAsStrings(expected.ByStatus),
		Actual:     keysAsStrings(actual),
		Transport:  transport,
		Latency:    percentiles(latencies),
		LatencyNote: fmt.Sprintf("по %d успешным запросам без медленного SKU-0004",
			len(latencies)),
	}

	// Расхождение плана и факта — это отказ прогона, а не примечание: дальше на
	// эти числа опираются проверки сигналов.
	for code, want := range expected.ByStatus {
		if actual[code] != want {
			res.Mismatch += abs(want - actual[code])
		}
	}

	if *asJSON {
		body, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(body))
	} else {
		printSummary(res)
	}
	if res.Mismatch > 0 || res.Transport > 0 {
		os.Exit(1)
	}
}

func execute(target, runID string, plan []Step, concurrency int) (map[int]int, int, []float64) {
	if concurrency < 1 {
		concurrency = 1
	}
	client := &http.Client{Timeout: 15 * time.Second}

	var mu sync.Mutex
	actual := map[int]int{}
	transport := 0
	var fastLatencies []float64

	steps := make(chan Step)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for step := range steps {
				started := time.Now()
				code, err := send(client, target, runID, step)
				elapsed := time.Since(started)
				mu.Lock()
				if err != nil {
					transport++
				} else {
					actual[code]++
					if code == 201 && step.SKU != "SKU-0004" {
						fastLatencies = append(fastLatencies, float64(elapsed.Microseconds())/1000.0)
					}
				}
				mu.Unlock()
			}
		}()
	}
	for _, step := range plan {
		steps <- step
	}
	close(steps)
	wg.Wait()

	return actual, transport, fastLatencies
}

func percentiles(values []float64) Latency {
	if len(values) == 0 {
		return Latency{}
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	at := func(p float64) float64 {
		idx := int(p * float64(len(sorted)-1))
		return sorted[idx]
	}
	return Latency{
		P50: at(0.50),
		P90: at(0.90),
		P99: at(0.99),
		Max: sorted[len(sorted)-1],
	}
}

func send(client *http.Client, target, runID string, step Step) (int, error) {
	body, err := json.Marshal(map[string]any{"sku": step.SKU, "quantity": step.Quantity})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodPost, target+"/order", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if runID != "" {
		req.Header.Set("X-Load-Run", runID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

func printSummary(res Result) {
	fmt.Printf("прогон: %d запросов за %s, параллельно %d\n", res.Requests, res.Duration, res.Concurrent)
	codes := make([]string, 0, len(res.Actual))
	for c := range res.Actual {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		fmt.Printf("  %s: ожидалось %d, получено %d\n", c, res.Expected[c], res.Actual[c])
	}
	if res.Transport > 0 {
		fmt.Printf("  транспортных ошибок: %d\n", res.Transport)
	}
	if res.Mismatch == 0 && res.Transport == 0 {
		fmt.Println("итог: факт совпал с планом")
	} else {
		fmt.Printf("итог: РАСХОЖДЕНИЕ на %d ответов\n", res.Mismatch)
	}
}

func keysAsStrings(m map[int]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[fmt.Sprintf("%d", k)] = v
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
