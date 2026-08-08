// Command architecture — стенд #2 серии "ScyllaDB: глубокое погружение":
// архитектура shard-per-core. Читает уже загруженную (Task 1) таблицу
// telemetry.readings (PRIMARY KEY ((device_id, day), event_time), 672000
// строк, Generate(42,500,14,96) из ../dataset) точечными чтениями по
// случайным полным ключам и показывает две вещи живьём:
//
//  1. -scenario shard-distribution: нагрузка (точечные чтения) распределяется
//     по шардам (per-core reactor'ам) каждого узла, а не бьёт в один —
//     снимает Prometheus-счётчик scylla_database_total_reads (с label
//     shard=N) на каждом узле ДО и ПОСЛЕ прогона, печатает дельту.
//  2. -scenario latency: собирает КЛИЕНТСКИЕ латентности того же точечного
//     чтения (time.Since на каждый round-trip), печатает p50/p99/max и
//     ассертит честный инвариант "хвост не разрывается на порядки" —
//     контраст с JVM-базированной Cassandra, где stop-the-world GC даёт
//     редкие, но кратные сотням мс выбросы p99/p999.
//
// Метрики читаются ПРЯМО из этого контейнера по сети scylla-cookbook-net —
// в отличие от `nodetool` (Task 1/2, требует docker-сокет хоста), Prometheus
// endpoint ScyllaDB (:9180/metrics) слушает на СЕТЕВОМ адресе контейнера
// (не 127.0.0.1 — см. README «Честные ограничения»), поэтому доступен по
// внутреннему DNS-имени узла (`scylla1:9180` и т.п.) с любого контейнера той
// же compose-сети без обхода через хост.
package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

// refDate — та же опорная точка, что и в dataset/main.go: датасет
// детерминирован не от time.Now(), а от этой зашитой в код даты. Полный
// перекрёстный набор ключей (500 устройств × 14 суток × 96 замеров) заведомо
// существует независимо от -seed генератора (seed там влияет только на
// value/status, не на то, какие ключи есть) — поэтому random-семплинг ключей
// здесь безопасен при ЛЮБОМ seed.
var refDate = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

const (
	devices = 500
	days    = 14
	perDay  = 96
)

func main() {
	scenario := flag.String("scenario", "shard-distribution", "shard-distribution|latency")
	hosts := flag.String("hosts", envOr("SCYLLA_HOSTS", "127.0.0.1:9042"), "scylla hosts (CQL, через запятую)")
	n := flag.Int("n", 20000, "число точечных чтений")
	seed := flag.Int64("seed", 42, "seed для выбора случайных ключей (воспроизводимость прогона)")
	k := flag.Float64("k", 5, "ассерт latency: p99 <= k*p50 (см. README «Стенд #2» за обоснованием числа — реальный p99/p50 на idle-кластере ~1.1-1.15, K=5 оставляет запас на фон компакции/репаира, но всё ещё далеко от GC-масштаба)")
	flag.Parse()

	session, err := connect(*hosts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer session.Close()

	var ok bool
	switch *scenario {
	case "shard-distribution":
		ok = shardDistributionScenario(session, deriveMetricsHosts(*hosts), *n, *seed)
	case "latency":
		ok = latencyScenario(session, *n, *seed, *k)
	default:
		fmt.Fprintf(os.Stderr, "unknown -scenario %q (expected shard-distribution|latency)\n", *scenario)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

func connect(hosts string) (*gocql.Session, error) {
	hostList := strings.Split(hosts, ",")
	for i := range hostList {
		hostList[i] = strings.TrimSpace(hostList[i])
	}
	cluster := gocql.NewCluster(hostList...)
	cluster.Keyspace = "telemetry"
	cluster.Consistency = gocql.Quorum
	cluster.Timeout = 20 * time.Second
	cluster.ConnectTimeout = 15 * time.Second
	return cluster.CreateSession()
}

// deriveMetricsHosts превращает CQL hosts (host:9042,...) в Prometheus hosts
// (host:9180,...) — ScyllaDB публикует :9180/metrics на КАЖДОМ узле рядом с
// CQL native портом.
func deriveMetricsHosts(hosts string) []string {
	parts := strings.Split(hosts, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		host := p
		if idx := strings.LastIndex(p, ":"); idx >= 0 {
			host = p[:idx]
		}
		out = append(out, host+":9180")
	}
	return out
}

// randomKey — случайный ПОЛНЫЙ первичный ключ (device_id, day, event_time)
// внутри заведомо существующего перекрёстного набора датасета Task 1.
func randomKey(r *rand.Rand) (deviceID string, day time.Time, eventTime time.Time) {
	d := r.Intn(devices)
	day = refDate.AddDate(0, 0, r.Intn(days))
	p := r.Intn(perDay)
	eventTime = day.Add(time.Duration(p) * 15 * time.Minute)
	return fmt.Sprintf("dev-%05d", d), day, eventTime
}

const pointReadCQL = `SELECT value FROM readings WHERE device_id=? AND day=? AND event_time=?`

// ---------------------------------------------------------------------------
// Сценарий 1: shard-distribution — точечные чтения по случайным полным
// ключам должны разойтись по ВСЕМ шардам каждого узла (per-core reactor),
// а не осесть на одном. Доказательство — рост Prometheus-счётчика
// scylla_database_total_reads (label shard=N) на каждом узле между
// снапшотом ДО и ПОСЛЕ прогона.
// ---------------------------------------------------------------------------

const shardMetric = "scylla_database_total_reads"

func shardDistributionScenario(session *gocql.Session, metricsHosts []string, n int, seed int64) bool {
	fmt.Println("=== Стенд #2: shard-distribution (нагрузка по шардам, не по одному) ===")
	fmt.Println()
	fmt.Printf("Метрика: %s{shard=\"N\"} — Prometheus endpoint :9180/metrics КАЖДОГО узла,\n", shardMetric)
	fmt.Println("сумма по всем label class= (sl:default/sl:driver/system/streaming/...) на каждый shard.")
	fmt.Println()

	before, err := snapshotAll(metricsHosts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "snapshot before:", err)
		return false
	}
	fmt.Println("-- Снапшот ДО нагрузки --")
	printShardSnapshot(before)
	fmt.Println()

	fmt.Printf("Прогон %d точечных чтений по случайным (device_id, day, event_time)...\n", n)
	r := rand.New(rand.NewSource(seed))
	var readErrs int
	for i := 0; i < n; i++ {
		devID, day, et := randomKey(r)
		var val float64
		if err := session.Query(pointReadCQL, devID, day, et).Scan(&val); err != nil {
			readErrs++
			if readErrs <= 3 {
				fmt.Fprintf(os.Stderr, "read #%d (device=%s day=%s event=%s): %v\n",
					i, devID, day.Format("2006-01-02"), et.Format(time.RFC3339), err)
			}
		}
	}
	fmt.Printf("...готово (ошибок чтения: %d/%d)\n\n", readErrs, n)

	after, err := snapshotAll(metricsHosts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "snapshot after:", err)
		return false
	}
	fmt.Println("-- Снапшот ПОСЛЕ нагрузки --")
	printShardSnapshot(after)
	fmt.Println()

	fmt.Println("-- Дельта (after - before) по шардам --")
	pass := true
	var hostsSorted []string
	for h := range before {
		hostsSorted = append(hostsSorted, h)
	}
	sort.Strings(hostsSorted)
	for _, h := range hostsSorted {
		var shardIDs []string
		for s := range before[h] {
			shardIDs = append(shardIDs, s)
		}
		sort.Strings(shardIDs)
		shardsWithGrowth := 0
		for _, s := range shardIDs {
			delta := after[h][s] - before[h][s]
			fmt.Printf("   %s shard=%s: %.0f -> %.0f (delta=%.0f)\n", h, s, before[h][s], after[h][s], delta)
			if delta > 0 {
				shardsWithGrowth++
			}
		}
		if shardsWithGrowth >= 2 {
			fmt.Printf("OK: %s — %d/%d шардов приняли новые чтения (нагрузка распределена, не на одном шарде)\n", h, shardsWithGrowth, len(shardIDs))
		} else {
			fmt.Printf("FAIL: %s — только %d/%d шардов приняли новые чтения\n", h, shardsWithGrowth, len(shardIDs))
			pass = false
		}
	}
	if readErrs > 0 {
		fmt.Printf("\nFAIL: %d ошибок чтения при прогоне\n", readErrs)
		pass = false
	}
	return pass
}

// snapshotAll снимает shardMetric со всех metricsHosts.
func snapshotAll(metricsHosts []string) (map[string]map[string]float64, error) {
	out := map[string]map[string]float64{}
	for _, h := range metricsHosts {
		counters, err := fetchShardCounters(h, shardMetric)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", h, err)
		}
		out[h] = counters
	}
	return out, nil
}

func printShardSnapshot(snap map[string]map[string]float64) {
	var hostsSorted []string
	for h := range snap {
		hostsSorted = append(hostsSorted, h)
	}
	sort.Strings(hostsSorted)
	for _, h := range hostsSorted {
		var shardIDs []string
		for s := range snap[h] {
			shardIDs = append(shardIDs, s)
		}
		sort.Strings(shardIDs)
		for _, s := range shardIDs {
			fmt.Printf("   %s shard=%s: %.0f\n", h, s, snap[h][s])
		}
	}
}

var shardLabelRe = regexp.MustCompile(`shard="(\d+)"`)

// fetchShardCounters парсит Prometheus text-exposition с metricsHost
// (http://host:9180/metrics), суммируя значения metricName по всем строкам
// с данным label shard= (разные label class= на одном shard складываются —
// нас интересует итоговая нагрузка на ядро, не разбивка по классам
// обслуживания).
func fetchShardCounters(metricsHost, metricName string) (map[string]float64, error) {
	url := "http://" + metricsHost + "/metrics"
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: статус %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	result := map[string]float64{}
	prefix := metricName + "{"
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		m := shardLabelRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		val, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		result[m[1]] += val
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("метрика %s не найдена на %s (см. README — актуальное имя счётчика per-shard могло смениться в этом образе)", metricName, metricsHost)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Сценарий 2: latency — p50/p99/max клиентских латентностей точечного
// чтения. Точка статьи: у shard-per-core/Seastar НЕТ JVM stop-the-world GC —
// хвост латентности не должен разрываться на порядки характерными для GC
// паузами в сотни мс.
// ---------------------------------------------------------------------------

func latencyScenario(session *gocql.Session, n int, seed int64, k float64) bool {
	fmt.Println("=== Стенд #2: latency (p50/p99/max точечных чтений) ===")
	fmt.Println()

	r := rand.New(rand.NewSource(seed))
	durations := make([]time.Duration, 0, n)
	var failed int
	for i := 0; i < n; i++ {
		devID, day, et := randomKey(r)
		t0 := time.Now()
		var val float64
		err := session.Query(pointReadCQL, devID, day, et).Scan(&val)
		d := time.Since(t0)
		if err != nil {
			failed++
			if failed <= 3 {
				fmt.Fprintf(os.Stderr, "read #%d: %v\n", i, err)
			}
			continue
		}
		durations = append(durations, d)
	}
	if len(durations) == 0 {
		fmt.Fprintln(os.Stderr, "latency: все чтения завершились ошибкой")
		return false
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

	p50 := percentile(durations, 0.50)
	p90 := percentile(durations, 0.90)
	p99 := percentile(durations, 0.99)
	p999 := percentile(durations, 0.999)
	min := durations[0]
	max := durations[len(durations)-1]

	fmt.Printf("Успешных чтений: %d/%d (ошибок: %d)\n\n", len(durations), n, failed)
	fmt.Printf("min:  %8s (%d µs)\n", min, min.Microseconds())
	fmt.Printf("p50:  %8s (%d µs)\n", p50, p50.Microseconds())
	fmt.Printf("p90:  %8s (%d µs)\n", p90, p90.Microseconds())
	fmt.Printf("p99:  %8s (%d µs)\n", p99, p99.Microseconds())
	fmt.Printf("p999: %8s (%d µs)\n", p999, p999.Microseconds())
	fmt.Printf("max:  %8s (%d µs)\n\n", max, max.Microseconds())

	ratio := float64(p99) / float64(p50)
	fmt.Println("-- Ассерт: p99 <= K*p50 (инвариант «хвост не разрывается на порядки» —")
	fmt.Println("   отсутствие stop-the-world GC-пауз shard-per-core/Seastar-архитектуры;")
	fmt.Println("   контраст с JVM-базированной Cassandra, где GC даёт редкие, но")
	fmt.Println("   кратные сотням мс выбросы) --")
	pass := true
	if ratio <= k {
		fmt.Printf("OK: p99/p50 = %.2f <= K=%.0f\n", ratio, k)
	} else {
		fmt.Printf("FAIL(честно): p99/p50 = %.2f > K=%.0f — реальное соотношение хуже выбранного K,\n", ratio, k)
		fmt.Println("   зафиксировано как есть, K НЕ занижен задним числом для зелёного вывода.")
		pass = false
	}
	// Второй, более прямой сигнал про "нет GC": max не должен быть на порядки
	// (100x+) больше p50 в единицах десятков-сотен МИЛЛИСЕКУНД — именно так
	// выглядит classic stop-the-world пауза на JVM. Не проваливаем стенд по
	// этому пункту (это доп. наблюдение, не жёсткий контракт), но фиксируем
	// честно.
	if max >= 200*time.Millisecond {
		fmt.Printf("NOTE: max=%s >= 200ms — в этом прогоне есть выброс масштаба, характерного для GC-пауз (см. README, честная оговорка)\n", max)
	} else {
		fmt.Printf("NOTE: max=%s < 200ms — ни одного выброса GC-масштаба (сотни мс) за весь прогон\n", max)
	}
	return pass
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
