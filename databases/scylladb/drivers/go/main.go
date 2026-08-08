// Command drivers (Go) — стенд #6 серии "ScyllaDB: глубокое погружение":
// shard-aware маршрутизация КАК ЦЕНА КОНФИГА одного драйвера, а не выбор
// между двумя разными драйверами.
//
// ПЕРЕРАБОТКА ДИЗАЙНА (см. README «Стенд #6» и task-7-brief.md): исходная
// идея — импортировать upstream github.com/gocql/gocql И форк
// github.com/scylladb/gocql под алиасами в одном бинаре — физически
// невозможна. Форк ScyllaDB слит с апстримом и САМОДЕКЛАРИРУЕТ module path
// "github.com/gocql/gocql" (см. github.com/scylladb/gocql@v1.18.3/go.mod) —
// используется через `replace` (см. go.mod рядом), поэтому два модуля с
// одинаковым путём не могут сосуществовать в одном go.mod: `go mod tidy`
// с попыткой добавить upstream github.com/gocql/gocql БЕЗ replace рядом с
// уже присутствующим replace-таргетом того же пути — конфликт module path,
// не конфликт версий. Контраст поэтому сделан ВНУТРИ одного (форкового)
// драйвера через gocql.PoolConfig.HostSelectionPolicy:
//
//	-mode aware: gocql.TokenAwareHostPolicy(gocql.RoundRobinHostPolicy()) —
//	  ЭТО ЖЕ дефолт форка (см. session.go:183 в scylladb/gocql — сессия без
//	  явного HostSelectionPolicy получает ровно эту политику). TokenAware
//	  прокидывает Token каждого запроса в hostConnPool.Pick(token, qry)
//	  (connectionpool.go) — форковый connPicker использует токен, чтобы
//	  выбрать НЕ ПРОСТО правильный узел (это умеет и апстримный token-aware),
//	  а КОНКРЕТНОЕ shard-aware TCP-соединение на этом узле (соединения
//	  устанавливаются через shard-aware порт, каждое привязано к одному
//	  shard'у сервера) — клиент бьёт прямо в CPU-шард, владеющий партицией.
//	-mode naive: gocql.RoundRobinHostPolicy() — БЕЗ токена, узел выбирается
//	  по кругу вне зависимости от того, чья это партиция; coordinator-узел
//	  почти всегда вынужден пересылать запрос внутренним RPC на владеющий
//	  shard (свой или чужого узла) — механизм тот же, что описан Стендом #2.
//
// Нагрузка — одинаковая в обоих режимах: N точечных чтений случайных
// партиций telemetry.readings (`WHERE device_id=? AND day=? LIMIT 1`,
// полный partition key good-модели, см. Стенд #1) с параметрами датасета
// Task 1 (devices=500, days=14, seed=42, refDate=2026-07-01) — читаем
// существующие партиции, не мутируем `readings`.
package main

import (
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

const (
	numDevices = 500
	numDays    = 14
)

var refDate = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

const pointReadCQL = `SELECT event_time, value FROM readings WHERE device_id = ? AND day = ? LIMIT 1`

func main() {
	mode := flag.String("mode", "aware", "aware|naive — политика выбора узла/шарда")
	hosts := flag.String("hosts", envOr("SCYLLA_HOSTS", "127.0.0.1:9042"), "scylla hosts (CQL, через запятую)")
	n := flag.Int("n", 100000, "число точечных чтений")
	warmup := flag.Int("warmup", 1000, "число прогревочных чтений ДО замера (не входят в throughput/p99)")
	seed := flag.Int64("seed", 42, "seed для выбора случайных партиций (воспроизводимость)")
	flag.Parse()

	var policy gocql.HostSelectionPolicy
	var policyDesc string
	switch *mode {
	case "aware":
		policy = gocql.TokenAwareHostPolicy(gocql.RoundRobinHostPolicy())
		policyDesc = "TokenAwareHostPolicy(RoundRobinHostPolicy()) — форк добавляет shard-routing поверх token-aware (= дефолт форка)"
	case "naive":
		policy = gocql.RoundRobinHostPolicy()
		policyDesc = "RoundRobinHostPolicy() — без token/shard-осведомлённости, coordinator форвардит на владеющий shard"
	default:
		fmt.Fprintf(os.Stderr, "unknown -mode %q (expected aware|naive)\n", *mode)
		os.Exit(2)
	}

	fmt.Printf("=== Стенд #6: drivers (Go) -mode=%s ===\n", *mode)
	fmt.Printf("policy: %s\n\n", policyDesc)

	hostList := strings.Split(*hosts, ",")
	for i := range hostList {
		hostList[i] = strings.TrimSpace(hostList[i])
	}

	cluster := gocql.NewCluster(hostList...)
	cluster.Keyspace = "telemetry"
	cluster.Consistency = gocql.Quorum
	cluster.Timeout = 15 * time.Second
	cluster.ConnectTimeout = 15 * time.Second
	cluster.PoolConfig.HostSelectionPolicy = policy

	session, err := cluster.CreateSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer session.Close()

	r := rand.New(rand.NewSource(*seed))

	// -- прогрев: соединения/shard-aware порт устанавливаются лениво, --
	// -- первые чтения платят за это отдельно — не включаем их в замер. --
	if *warmup > 0 {
		fmt.Printf("прогрев: %d чтений (не в замере)...\n", *warmup)
		werrs := 0
		for i := 0; i < *warmup; i++ {
			did, day := randomPartition(r)
			var et time.Time
			var val float64
			if err := session.Query(pointReadCQL, did, day).Scan(&et, &val); err != nil {
				werrs++
			}
		}
		if werrs > 0 {
			fmt.Printf("  (прогрев: %d ошибок из %d — не критично, не входят в замер)\n", werrs, *warmup)
		}
	}

	fmt.Printf("замер: %d точечных чтений telemetry.readings (случайные device_id+day)...\n\n", *n)

	durations := make([]time.Duration, 0, *n)
	errs := 0
	t0 := time.Now()
	for i := 0; i < *n; i++ {
		did, day := randomPartition(r)
		var et time.Time
		var val float64
		qt0 := time.Now()
		err := session.Query(pointReadCQL, did, day).Scan(&et, &val)
		qd := time.Since(qt0)
		if err != nil {
			errs++
			if errs <= 3 {
				fmt.Fprintf(os.Stderr, "  read #%d (device_id=%s, day=%s): %v\n", i, did, day.Format("2006-01-02"), err)
			}
			continue
		}
		durations = append(durations, qd)
	}
	elapsed := time.Since(t0)

	p50, p99 := stats(durations)
	success := len(durations)
	var throughput float64
	if elapsed.Seconds() > 0 {
		throughput = float64(success) / elapsed.Seconds()
	}

	fmt.Printf("-- Результат -mode=%s --\n", *mode)
	fmt.Printf("успешных чтений: %d/%d (ошибок: %d)\n", success, *n, errs)
	fmt.Printf("elapsed: %s\n", elapsed)
	fmt.Printf("throughput: %.1f rows/s\n", throughput)
	fmt.Printf("latency p50: %s\n", p50)
	fmt.Printf("latency p99: %s\n", p99)
	fmt.Println()

	// machine-читаемая строка — для сборки README-таблицы/сравнения aware vs naive.
	fmt.Printf("RESULT mode=%s n=%d success=%d errs=%d elapsed_ms=%d throughput_rows_s=%.1f p50_us=%d p99_us=%d\n",
		*mode, *n, success, errs, elapsed.Milliseconds(), throughput, p50.Microseconds(), p99.Microseconds())

	if errs > 0 {
		fmt.Fprintf(os.Stderr, "\nFAIL: %d ошибок чтения за прогон\n", errs)
		os.Exit(1)
	}
}

// randomPartition — случайный существующий partition key телеметрии
// Task 1 (device_id, day), тот же генератор параметров, что и
// dataset.Generate(42, 500, 14, 96): device_id "dev-%05d" (0..499),
// day = refDate + 0..13 суток.
func randomPartition(r *rand.Rand) (string, time.Time) {
	dev := r.Intn(numDevices)
	day := r.Intn(numDays)
	return fmt.Sprintf("dev-%05d", dev), refDate.AddDate(0, 0, day)
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
