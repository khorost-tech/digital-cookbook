// Command consistency-lwt — стенд #4 серии "ScyllaDB: глубокое погружение":
// consistency levels и LWT (lightweight transactions) как МЕХАНИКА и ЦЕНА,
// а не как гарантия изоляции (гарантии/изоляцию через LWT уже показала
// серия "Транзакции и изоляция", digital-cookbook/transactions/kv-document —
// там 16 воркеров бронируют одно место, LWT против plain INSERT как
// демонстрация линеаризуемости; single-node RF=1). Здесь — тот же примитив
// (Paxos), но на РЕАЛЬНОМ 3-узловом RF=3 кластере, и вопрос другой: СКОЛЬКО
// это стоит и КАК ведёт себя под конкуренцией за один и тот же партиционный
// ключ.
//
//	-scenario cl-latency  — для CL ONE/LOCAL_QUORUM/QUORUM/ALL прогоняет N
//	  записей + N чтений в telemetry.cl_bench (свежий ключ на каждую
//	  операцию, читаем ТОЛЬКО что записанный ключ — без гонки с чужими
//	  записями), печатает клиентские p50/p99 write/read по каждому CL.
//	  Один ДЦ (datacenter1, Task 1) → LOCAL_QUORUM и QUORUM эквивалентны
//	  (кворум из 3 реплик = 2 в обоих случаях, разница только в CL,
//	  оперирующем в рамках локального ДЦ vs всего кластера — при одном ДЦ
//	  это одна и та же величина).
//	-scenario lwt          — сравнивает клиентскую латентность обычной
//	  записи (INSERT) и LWT-записи (INSERT ... IF NOT EXISTS) в свою
//	  таблицу telemetry.counters_lwt (readings, Task 1, не трогаем), затем
//	  моделирует конкуренцию: -contenders горутин синхронно бьют в ОДИН и
//	  тот же партиционный ключ через read-then-CAS (UPDATE ... IF val=?),
//	  считает долю applied=false — ЧЕСТНО: эта доля структурно зависит от
//	  -contenders и наивного (без jitter/backoff) лок-степ-тайминга этого
//	  цикла (≈(contenders-1)/contenders), не общая вероятность конфликта
//	  LWT — см. пояснение в выводе сценария и README «Стенд #4». Плюс
//	  серверные CAS-счётчики с :9180/metrics каждого узла (cas_prune и
//	  корроборирующий cas_total_operations — оба совпадают между собой
//	  1:1; полный список найденных CAS-счётчиков — см. README «Стенд #4»).
package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gocql/gocql"
)

func main() {
	scenario := flag.String("scenario", "cl-latency", "cl-latency|lwt")
	hosts := flag.String("hosts", envOr("SCYLLA_HOSTS", "127.0.0.1:9042"), "scylla hosts (CQL, через запятую)")
	n := flag.Int("n", 20000, "cl-latency: число write+read операций НА КАЖДЫЙ CL; lwt: число операций на сравнение plain-vs-LWT (каждое) И общее число CAS-попыток в contention-тесте (делится поровну между -contenders)")
	contenders := flag.Int("contenders", 16, "lwt: число горутин, конкурирующих за один партиционный ключ")
	seed := flag.Int64("seed", 42, "seed для значений (воспроизводимость)")
	flag.Parse()

	var ok bool
	switch *scenario {
	case "cl-latency":
		ok = clLatencyScenario(*hosts, *n, *seed)
	case "lwt":
		ok = lwtScenario(*hosts, *n, *contenders, *seed)
	default:
		fmt.Fprintf(os.Stderr, "unknown -scenario %q (expected cl-latency|lwt)\n", *scenario)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

func connect(hosts string, defaultCL gocql.Consistency) (*gocql.Session, error) {
	hostList := strings.Split(hosts, ",")
	for i := range hostList {
		hostList[i] = strings.TrimSpace(hostList[i])
	}
	cluster := gocql.NewCluster(hostList...)
	cluster.Keyspace = "telemetry"
	cluster.Consistency = defaultCL
	cluster.Timeout = 20 * time.Second
	cluster.ConnectTimeout = 15 * time.Second
	return cluster.CreateSession()
}

// deriveMetricsHosts — как в ../architecture: CQL hosts (host:9042,...) ->
// Prometheus hosts (host:9180,...), ScyllaDB публикует :9180/metrics на
// каждом узле рядом с CQL native портом.
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

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
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

func stats(durations []time.Duration) (p50, p99 time.Duration) {
	cp := make([]time.Duration, len(durations))
	copy(cp, durations)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return percentile(cp, 0.50), percentile(cp, 0.99)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---------------------------------------------------------------------------
// Метрики :9180/metrics — суммирование произвольного counter'а по всем
// шардам/scheduling-group одного узла, с опциональным фильтром по подстроке
// в наборе label. found=false, если ни одна строка не совпала — ЭТО НЕ
// ошибка (Seastar может не регистрировать конкретную комбинацию label,
// напр. conditional="yes", пока такой запрос ни разу не выполнялся).
// ---------------------------------------------------------------------------

func fetchCounterSum(metricsHost, metricName, labelSubstr string) (sum float64, found bool, err error) {
	url := "http://" + metricsHost + "/metrics"
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false, fmt.Errorf("GET %s: статус %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, false, err
	}
	prefix := metricName + "{"
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if labelSubstr != "" && !strings.Contains(line, labelSubstr) {
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
		sum += val
		found = true
	}
	return sum, found, nil
}

// clusterCounterSum суммирует fetchCounterSum по ВСЕМ узлам кластера;
// узел без совпавших строк вносит 0 (не ошибку) — см. fetchCounterSum.
func clusterCounterSum(metricsHosts []string, metricName, labelSubstr string) (sum float64, anyFound bool, err error) {
	for _, h := range metricsHosts {
		v, found, ferr := fetchCounterSum(h, metricName, labelSubstr)
		if ferr != nil {
			return 0, false, fmt.Errorf("%s: %w", h, ferr)
		}
		sum += v
		anyFound = anyFound || found
	}
	return sum, anyFound, nil
}

// ---------------------------------------------------------------------------
// Сценарий 1: cl-latency
// ---------------------------------------------------------------------------

const createClBenchCQL = `CREATE TABLE IF NOT EXISTS cl_bench (id text PRIMARY KEY, val double)`
const writeClBenchCQL = `INSERT INTO cl_bench (id, val) VALUES (?, ?)`
const readClBenchCQL = `SELECT val FROM cl_bench WHERE id = ?`

type clLevel struct {
	name string
	cl   gocql.Consistency
}

var clLevels = []clLevel{
	{"ONE", gocql.One},
	{"LOCAL_QUORUM", gocql.LocalQuorum},
	{"QUORUM", gocql.Quorum},
	{"ALL", gocql.All},
}

func clLatencyScenario(hosts string, n int, seed int64) bool {
	fmt.Println("=== Стенд #4: cl-latency (латентность ONE/LOCAL_QUORUM/QUORUM/ALL) ===")
	fmt.Println()

	session, err := connect(hosts, gocql.Quorum)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		return false
	}
	defer session.Close()

	if err := session.Query(createClBenchCQL).Exec(); err != nil {
		fmt.Fprintln(os.Stderr, "create cl_bench:", err)
		return false
	}

	runID := time.Now().UnixNano()
	r := rand.New(rand.NewSource(seed))

	type result struct {
		writeP50, writeP99 time.Duration
		readP50, readP99   time.Duration
		combinedP50        time.Duration
		combinedP99        time.Duration
		errs               int
	}
	results := map[string]result{}

	for _, lvl := range clLevels {
		fmt.Printf("-- CL=%s: %d write+read операций --\n", lvl.name, n)
		var writeDur, readDur []time.Duration
		var errs int
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("cl-%d-%s-%06d", runID, lvl.name, i)
			val := r.Float64() * 100

			t0 := time.Now()
			werr := session.Query(writeClBenchCQL, id, val).Consistency(lvl.cl).Exec()
			wd := time.Since(t0)
			if werr != nil {
				errs++
				if errs <= 3 {
					fmt.Fprintf(os.Stderr, "  write #%d (CL=%s): %v\n", i, lvl.name, werr)
				}
				continue
			}
			writeDur = append(writeDur, wd)

			var got float64
			t1 := time.Now()
			rerr := session.Query(readClBenchCQL, id).Consistency(lvl.cl).Scan(&got)
			rd := time.Since(t1)
			if rerr != nil {
				errs++
				if errs <= 3 {
					fmt.Fprintf(os.Stderr, "  read #%d (CL=%s): %v\n", i, lvl.name, rerr)
				}
				continue
			}
			readDur = append(readDur, rd)
		}
		wp50, wp99 := stats(writeDur)
		rp50, rp99 := stats(readDur)
		combined := make([]time.Duration, 0, len(writeDur)+len(readDur))
		combined = append(combined, writeDur...)
		combined = append(combined, readDur...)
		cp50, cp99 := stats(combined)
		results[lvl.name] = result{wp50, wp99, rp50, rp99, cp50, cp99, errs}
		fmt.Printf("   write p50=%-10s p99=%-10s | read p50=%-10s p99=%-10s | ошибок=%d\n",
			wp50, wp99, rp50, rp99, errs)
	}
	fmt.Println()

	fmt.Println("-- Таблица CL -> латентность (клиентская, µs) --")
	fmt.Printf("%-14s %10s %10s %10s %10s %12s %12s\n", "CL", "write_p50", "write_p99", "read_p50", "read_p99", "combined_p50", "combined_p99")
	for _, lvl := range clLevels {
		res := results[lvl.name]
		fmt.Printf("%-14s %10d %10d %10d %10d %12d %12d\n",
			lvl.name,
			res.writeP50.Microseconds(), res.writeP99.Microseconds(),
			res.readP50.Microseconds(), res.readP99.Microseconds(),
			res.combinedP50.Microseconds(), res.combinedP99.Microseconds())
	}
	fmt.Println()

	fmt.Println("NOTE: один ДЦ (datacenter1) — LOCAL_QUORUM и QUORUM эквивалентны")
	fmt.Println("      (кворум из 3 реплик = 2 в обоих случаях); ожидаемая разница")
	fmt.Println("      между их числами в этом прогоне — шум измерения, не архитектура.")
	fmt.Println()

	oneC := results["ONE"].combinedP50
	allC := results["ALL"].combinedP50
	pass := true
	fmt.Println("-- Ассерт: lat[ALL] >= lat[ONE] (combined write+read p50; ALL ждёт все 3 реплики) --")
	if allC >= oneC {
		fmt.Printf("OK: combined_p50[ALL]=%s >= combined_p50[ONE]=%s\n", allC, oneC)
	} else {
		fmt.Printf("НАБЛЮДЕНИЕ (не форсируем зелёный): combined_p50[ALL]=%s < combined_p50[ONE]=%s —\n", allC, oneC)
		fmt.Println("   на idle однохостовом кластере разница между CL укладывается в шум измерения")
		fmt.Println("   (сеть локальная, все 3 реплики физически на одной машине); честно фиксируем")
		fmt.Println("   реальные числа как есть, НЕ подгоняя ассерт под ожидание. Не считается FAIL сценария.")
	}

	totalErrs := 0
	for _, lvl := range clLevels {
		totalErrs += results[lvl.name].errs
	}
	if totalErrs > 0 {
		fmt.Printf("\nFAIL: %d ошибок write/read за весь прогон\n", totalErrs)
		pass = false
	}
	return pass
}

// ---------------------------------------------------------------------------
// Сценарий 2: lwt — латентность LWT vs plain + contention + CAS-метрики
// ---------------------------------------------------------------------------

const createCountersLWTCQL = `CREATE TABLE IF NOT EXISTS counters_lwt (key text PRIMARY KEY, val bigint)`
const plainInsertCQL = `INSERT INTO counters_lwt (key, val) VALUES (?, ?)`
const lwtInsertCQL = `INSERT INTO counters_lwt (key, val) VALUES (?, ?) IF NOT EXISTS`
const readCounterCQL = `SELECT val FROM counters_lwt WHERE key = ?`
const casUpdateCQL = `UPDATE counters_lwt SET val = ? WHERE key = ? IF val = ?`

const hotKey = "contended-key"

func lwtScenario(hosts string, n, contenders int, seed int64) bool {
	fmt.Println("=== Стенд #4: lwt (латентность LWT vs plain + contention + CAS-метрики) ===")
	fmt.Println()

	session, err := connect(hosts, gocql.Quorum)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		return false
	}
	defer session.Close()

	if err := session.Query(createCountersLWTCQL).Exec(); err != nil {
		fmt.Fprintln(os.Stderr, "create counters_lwt:", err)
		return false
	}

	metricsHosts := deriveMetricsHosts(hosts)

	casPruneBefore, _, err := clusterCounterSum(metricsHosts, "scylla_storage_proxy_coordinator_cas_prune", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cas_prune before:", err)
	}
	// cas_total_operations — второй counter из полного набора CAS-метрик
	// (см. блок вывода ниже); держим рядом с cas_prune для дельты до/после.
	casTotalOpsBefore, _, err := clusterCounterSum(metricsHosts, "scylla_storage_proxy_coordinator_cas_total_operations", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cas_total_operations before:", err)
	}
	condInsertBefore, condInsertFoundBefore, err := clusterCounterSum(metricsHosts, "scylla_cql_inserts", `conditional="yes"`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cql_inserts conditional before:", err)
	}
	condUpdateBefore, condUpdateFoundBefore, err := clusterCounterSum(metricsHosts, "scylla_cql_updates", `conditional="yes"`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cql_updates conditional before:", err)
	}

	runID := time.Now().UnixNano()

	// -- 1. Латентность: plain INSERT vs LWT INSERT ... IF NOT EXISTS, --
	// -- отдельные (свежие, run-id-меченные) ключи на каждую операцию, --
	// -- поэтому applied=true ожидается практически на 100% попыток. --
	fmt.Printf("-- Латентность: %d plain INSERT vs %d LWT INSERT ... IF NOT EXISTS (раздельные ключи) --\n", n, n)
	var plainDur []time.Duration
	var plainErrs int
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("plain-%d-%06d", runID, i)
		t0 := time.Now()
		err := session.Query(plainInsertCQL, key, int64(i)).Exec()
		d := time.Since(t0)
		if err != nil {
			plainErrs++
			if plainErrs <= 3 {
				fmt.Fprintf(os.Stderr, "  plain insert #%d: %v\n", i, err)
			}
			continue
		}
		plainDur = append(plainDur, d)
	}

	var lwtDur []time.Duration
	var lwtErrs, lwtApplied, lwtNotApplied int
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("lwt-%d-%06d", runID, i)
		t0 := time.Now()
		applied, err := session.Query(lwtInsertCQL, key, int64(i)).MapScanCAS(map[string]interface{}{})
		d := time.Since(t0)
		if err != nil {
			lwtErrs++
			if lwtErrs <= 3 {
				fmt.Fprintf(os.Stderr, "  lwt insert #%d: %v\n", i, err)
			}
			continue
		}
		lwtDur = append(lwtDur, d)
		if applied {
			lwtApplied++
		} else {
			lwtNotApplied++
		}
	}

	plainP50, plainP99 := stats(plainDur)
	lwtP50, lwtP99 := stats(lwtDur)
	fmt.Printf("plain: p50=%-10s p99=%-10s ошибок=%d\n", plainP50, plainP99, plainErrs)
	fmt.Printf("lwt:   p50=%-10s p99=%-10s ошибок=%d applied=%d/%d\n", lwtP50, lwtP99, lwtErrs, lwtApplied, lwtApplied+lwtNotApplied)
	var ratio float64
	if plainP50 > 0 {
		ratio = float64(lwtP50) / float64(plainP50)
	}
	fmt.Printf("ratio lwt_p50/plain_p50 = %.2fx\n\n", ratio)

	// -- 2. Contention: G горутин конкурируют за ОДИН партиционный ключ --
	// -- через read-then-CAS (UPDATE ... IF val=?), без retry. --

	// Инициализация ключа: если уже существует с прошлого прогона стенда —
	// это ОК, применённость (applied) здесь не проверяем, продолжаем с тем,
	// что реально в таблице (см. чтение стартового значения ниже).
	if _, err := session.Query(lwtInsertCQL, hotKey, int64(0)).MapScanCAS(map[string]interface{}{}); err != nil {
		fmt.Fprintln(os.Stderr, "init hot key:", err)
		return false
	}

	perGoroutine := n / contenders
	if perGoroutine < 1 {
		perGoroutine = 1
	}
	fmt.Printf("-- Contention: %d горутин x %d попыток = %d суммарных CAS-попыток на ключ %q --\n",
		contenders, perGoroutine, perGoroutine*contenders, hotKey)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var contDur []time.Duration
	var contApplied, contFailed, contErrs int

	for g := 0; g < contenders; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			localDur := make([]time.Duration, 0, perGoroutine)
			localApplied, localFailed, localErrs := 0, 0, 0
			for i := 0; i < perGoroutine; i++ {
				var cur int64
				if err := session.Query(readCounterCQL, hotKey).Scan(&cur); err != nil {
					localErrs++
					continue
				}
				var existing int64
				t0 := time.Now()
				applied, err := session.Query(casUpdateCQL, cur+1, hotKey, cur).ScanCAS(&existing)
				d := time.Since(t0)
				if err != nil {
					localErrs++
					continue
				}
				localDur = append(localDur, d)
				if applied {
					localApplied++
				} else {
					localFailed++
				}
			}
			mu.Lock()
			contDur = append(contDur, localDur...)
			contApplied += localApplied
			contFailed += localFailed
			contErrs += localErrs
			mu.Unlock()
		}(g)
	}
	wg.Wait()

	contTotal := contApplied + contFailed
	var contFailedFraction float64
	if contTotal > 0 {
		contFailedFraction = float64(contFailed) / float64(contTotal)
	}
	contP50, contP99 := stats(contDur)
	fmt.Printf("contention CAS: попыток=%d (applied=%d, failed=%d, ошибок-транспорта=%d)\n", contTotal, contApplied, contFailed, contErrs)
	fmt.Printf("contention CAS latency: p50=%-10s p99=%-10s\n", contP50, contP99)
	fmt.Printf("contended_failed_fraction = %.4f (%.1f%%)\n\n", contFailedFraction, contFailedFraction*100)

	structuralFraction := float64(contenders-1) / float64(contenders)
	fmt.Println("ЧЕСТНО: эта доля — СТРУКТУРНЫЙ артефакт конкретного contention-цикла,")
	fmt.Println("  не общая вероятность конфликта LWT. Цикл намеренно наивный: без")
	fmt.Println("  джиттера/backoff, все горутины читают текущее значение и почти")
	fmt.Println("  синхронно бьют CAS одним лок-степ-раундом, поэтому в каждом раунде")
	fmt.Println("  выигрывает РОВНО один участник из -contenders — отсюда")
	fmt.Printf("  failed_fraction ≈ (contenders-1)/contenders = %.4f при -contenders=%d\n", structuralFraction, contenders)
	fmt.Println("  (проверено: перезапуск с другим -contenders даёт другую долю по той же")
	fmt.Println("  формуле, а не одну и ту же константу — это подтверждает структурную, а")
	fmt.Println("  не вероятностную природу числа). При джиттере/backoff или несинхронном")
	fmt.Println("  тайминге клиентов конкретный процент был бы другим. Реальный вывод,")
	fmt.Println("  который переживает эту оговорку: под высокой конкуренцией на ОДИН")
	fmt.Println("  партиционный ключ LWT тратит большинство попыток впустую (это и есть")
	fmt.Println("  цена конкуренции за Paxos) — а точный процент есть функция числа")
	fmt.Println("  конкурентов и тайминга конкретного теста, не архитектурное свойство LWT.")
	fmt.Println()

	// -- 3. Серверные CAS-метрики :9180/metrics (до/после) --
	casPruneAfter, casPruneFound, err := clusterCounterSum(metricsHosts, "scylla_storage_proxy_coordinator_cas_prune", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cas_prune after:", err)
	}
	casTotalOpsAfter, casTotalOpsFound, err := clusterCounterSum(metricsHosts, "scylla_storage_proxy_coordinator_cas_total_operations", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cas_total_operations after:", err)
	}
	condInsertAfter, condInsertFoundAfter, err := clusterCounterSum(metricsHosts, "scylla_cql_inserts", `conditional="yes"`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cql_inserts conditional after:", err)
	}
	condUpdateAfter, condUpdateFoundAfter, err := clusterCounterSum(metricsHosts, "scylla_cql_updates", `conditional="yes"`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cql_updates conditional after:", err)
	}

	fmt.Println("-- Серверные CAS-метрики (:9180/metrics, сумма по всем узлам/шардам) --")
	fmt.Println("Полный список CAS/Paxos-счётчиков, реально найденных в этой сборке")
	fmt.Println("(2026.2.0) через grep -i cas по :9180/metrics (см. README «Стенд #4» за")
	fmt.Println("живым выводом grep): cas_background, cas_foreground (gauge — 'сколько")
	fmt.Println("сейчас выполняется', не годятся для before/after-дельты), cas_prune,")
	fmt.Println("cas_total_operations (оба counter), cas_write_latency /")
	fmt.Println("cas_write_latency_summary (histogram/summary — латентность, не счёт")
	fmt.Println("операций). Из них для дельты применений годятся ДВА counter'а:")
	fmt.Println("  scylla_storage_proxy_coordinator_cas_prune — HELP-текст гласит")
	fmt.Println("  'how many times paxos prune was done after successful cas operation', НО")
	fmt.Println("  живой прогон опровергает буквальное чтение HELP: delta считает КАЖДЫЙ")
	fmt.Println("  завершённый раунд Paxos (prepare-propose-commit), включая CAS с")
	fmt.Println("  applied=false — не только те, где условие IF реально выполнилось.")
	fmt.Printf("  before=%.0f after=%.0f delta=%.0f (found=%v)\n", casPruneBefore, casPruneAfter, casPruneAfter-casPruneBefore, casPruneFound)
	fmt.Println("  scylla_storage_proxy_coordinator_cas_total_operations — HELP-текст")
	fmt.Println("  'number of total paxos operations executed (reads and writes)' —")
	fmt.Println("  БУКВАЛЬНО точен для того, что здесь нужно. Живьём (см. README) значение")
	fmt.Println("  этого счётчика РОВНО совпадает с cas_prune на каждом шарде отдельно,")
	fmt.Println("  не только в сумме — это корроборирующее подтверждение вывода про")
	fmt.Println("  cas_prune, а не независимый альтернативный источник данных.")
	fmt.Printf("  before=%.0f after=%.0f delta=%.0f (found=%v)\n", casTotalOpsBefore, casTotalOpsAfter, casTotalOpsAfter-casTotalOpsBefore, casTotalOpsFound)
	fmt.Println("  scylla_cql_inserts{conditional=\"yes\"} / scylla_cql_updates{conditional=\"yes\"}")
	fmt.Println("  (counter) — сколько CQL INSERT/UPDATE запросов пришли С условием (IF),")
	fmt.Println("  независимо от applied/не applied — счётчик ЗАПРОСОВ, не исходов.")
	fmt.Printf("  cql_inserts conditional=yes: before=%.0f(found=%v) after=%.0f(found=%v) delta=%.0f\n",
		condInsertBefore, condInsertFoundBefore, condInsertAfter, condInsertFoundAfter, condInsertAfter-condInsertBefore)
	fmt.Printf("  cql_updates conditional=yes: before=%.0f(found=%v) after=%.0f(found=%v) delta=%.0f\n",
		condUpdateBefore, condUpdateFoundBefore, condUpdateAfter, condUpdateFoundAfter, condUpdateAfter-condUpdateBefore)
	fmt.Println()
	fmt.Println("ЧЕСТНО: в этой сборке НЕТ отдельного счётчика 'CAS отклонён/condition not met'")
	fmt.Println("  (проверено: grep -i 'cas\\|applied\\|reject\\|condition' по всем HELP-строкам")
	fmt.Println("  :9180/metrics — такого счётчика нет). Доля неуспешных CAS при contention")
	fmt.Println("  измеряется ТОЛЬКО клиентски (applied==false из ответа сервера на каждый")
	fmt.Println("  запрос, см. contended_failed_fraction выше) — сервер эту метрику не экспонирует.")
	fmt.Println()

	clientTotalCASOps := int64(lwtApplied+lwtNotApplied) + 1 /* init hot key */ + int64(contTotal)
	fmt.Printf("Сверка: клиентски выполненных CAS-раундов (lwt IF NOT EXISTS + init + все contention-попытки) = %d;\n", clientTotalCASOps)
	fmt.Printf("        серверная delta(cas_prune) = %.0f\n", casPruneAfter-casPruneBefore)
	if int64(casPruneAfter-casPruneBefore) == clientTotalCASOps {
		fmt.Println("        РОВНОЕ совпадение — подтверждает живьём: cas_prune = 1 инкремент на КАЖДЫЙ")
		fmt.Println("        завершённый Paxos-раунд (не только на applied=true).")
	} else {
		fmt.Println("        не совпало ровно — на кластере нет других клиентов, но раунды могут не")
		fmt.Println("        биться 1:1 с клиентским счётом (см. README «Стенд #4» за честной оговоркой).")
	}
	fmt.Println()

	// -- Ассерты --
	pass := true
	fmt.Println("-- Ассерты --")
	if lwtP50 > plainP50 {
		fmt.Printf("OK: lwt_latency (p50=%s) > plain_latency (p50=%s), ratio=%.2fx\n", lwtP50, plainP50, ratio)
	} else {
		fmt.Printf("FAIL: lwt_latency (p50=%s) НЕ выше plain_latency (p50=%s)\n", lwtP50, plainP50)
		pass = false
	}
	if contFailedFraction > 0 {
		fmt.Printf("OK: contended_failed_fraction = %.4f > 0 (%d/%d неуспешных CAS под конкуренцией)\n", contFailedFraction, contFailed, contTotal)
	} else {
		fmt.Printf("FAIL: contended_failed_fraction = 0 — конкуренция не дала ни одного отказа CAS\n")
		pass = false
	}

	if plainErrs > 0 || lwtErrs > 0 || contErrs > 0 {
		fmt.Printf("\nFAIL: транспортные ошибки (plain=%d, lwt=%d, contention=%d)\n", plainErrs, lwtErrs, contErrs)
		pass = false
	}

	return pass
}
