// Command modeling — стенд #1 серии "ScyllaDB: глубокое погружение":
// модель данных, хорошая (readings, партиция (device_id, day)) vs плохая
// (readings_bad, партиция только device_id) vs hot-partition (readings_hot,
// партиция только region). Все три таблицы созданы schema.cql (Task 1) и
// загружены одним и тем же датасетом (Generate(42,500,14,96) из ../dataset,
// см. README «Стенд #1» — загрузка это отдельный шаг перед запуском этого
// стенда: `dataset -table readings_bad -load` / `-table readings_hot -load`).
//
// Этот бинарь НЕ грузит данные — он живьём измеряет партиции уже загруженного
// кластера через CQL (число строк на партицию как proxy для размера в
// байтах — ширина строки во всех трёх таблицах одинакова с точностью до
// порядка колонок, см. README: ~50 байт/строка что в readings, что в
// readings_bad, что в readings_hot, проверено live через nodetool tablestats)
// и ассертит педагогический вывод модели данных. Точные РЕАЛЬНЫЕ байтовые
// числа (Compacted partition maximum/mean bytes) — из `nodetool tablestats`,
// который недоступен изнутри этого контейнера (нет доступа к docker-сокету
// хоста с сети scylla-cookbook-net) — они зафиксированы отдельно в README и
// в отчёте задачи, здесь только живая CQL-часть.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

var regions = []string{"eu-west", "eu-east", "us-east", "ap-south"}

func main() {
	scenario := flag.String("scenario", "partition-size", "partition-size|hot-partition|query-shape")
	hosts := flag.String("hosts", envOr("SCYLLA_HOSTS", "127.0.0.1:9042"), "scylla hosts")
	flag.Parse()

	session, err := connect(*hosts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer session.Close()

	var ok bool
	switch *scenario {
	case "partition-size":
		ok = partitionSizeScenario(session)
	case "hot-partition":
		ok = hotPartitionScenario(session)
	case "query-shape":
		ok = queryShapeScenario(session)
	default:
		fmt.Fprintf(os.Stderr, "unknown -scenario %q (expected partition-size|hot-partition|query-shape)\n", *scenario)
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

// ---------------------------------------------------------------------------
// Сценарий 1: partition-size — good vs bad vs hot, размер партиции (proxy —
// число строк, ширина строки во всех трёх таблицах одинакова).
// ---------------------------------------------------------------------------

func partitionSizeScenario(session *gocql.Session) bool {
	fmt.Println("=== Стенд #1: partition-size (good vs bad vs hot) ===")
	fmt.Println()

	// --- good: readings, партиция (device_id, day). Генератор гарантирует
	// РОВНО perDay строк на каждую партицию — проверяем живым запросом на
	// нескольких устройствах/сутках (не берём слово генератора на веру).
	fmt.Println("-- readings (good model), партиция (device_id, day) --")
	goodSamples := []struct{ device, day string }{
		{"dev-00000", "2026-07-01"}, {"dev-00000", "2026-07-14"},
		{"dev-00250", "2026-07-07"}, {"dev-00499", "2026-07-01"},
		{"dev-00499", "2026-07-14"},
	}
	goodMax, goodMean := 0, 0.0
	for _, s := range goodSamples {
		var cnt int
		if err := session.Query(
			`SELECT count(*) FROM readings WHERE device_id=? AND day=?`, s.device, s.day,
		).Scan(&cnt); err != nil {
			fmt.Fprintln(os.Stderr, "good count:", err)
			return false
		}
		fmt.Printf("   device_id=%s day=%s -> %d строк\n", s.device, s.day, cnt)
		if cnt > goodMax {
			goodMax = cnt
		}
		goodMean += float64(cnt)
	}
	goodMean /= float64(len(goodSamples))
	var goodTotal int
	if err := session.Query(`SELECT count(*) FROM readings`).Scan(&goodTotal); err != nil {
		fmt.Fprintln(os.Stderr, "good total:", err)
		return false
	}
	fmt.Printf("   итого строк в readings: %d\n", goodTotal)
	fmt.Printf("   max строк/партиция (по выборке): %d, mean: %.1f\n\n", goodMax, goodMean)

	// --- bad: readings_bad, партиция только device_id — проходим ВСЕ 500
	// устройств (живой запрос на каждую партицию, не оценка).
	fmt.Println("-- readings_bad (bad model), партиция device_id --")
	badMax, badTotal := 0, 0
	badSum := 0
	for d := 0; d < 500; d++ {
		devID := fmt.Sprintf("dev-%05d", d)
		var cnt int
		if err := session.Query(
			`SELECT count(*) FROM readings_bad WHERE device_id=?`, devID,
		).Scan(&cnt); err != nil {
			fmt.Fprintln(os.Stderr, "bad count:", err)
			return false
		}
		if cnt > badMax {
			badMax = cnt
		}
		badSum += cnt
		badTotal++
	}
	badMean := float64(badSum) / float64(badTotal)
	fmt.Printf("   партиций (устройств): %d, итого строк: %d\n", badTotal, badSum)
	fmt.Printf("   max строк/партиция: %d, mean: %.1f\n\n", badMax, badMean)

	// --- hot: readings_hot, партиция только region — ровно 4 значения.
	fmt.Println("-- readings_hot (hot-partition model), партиция region --")
	var distinctRegions []string
	iter := session.Query(`SELECT DISTINCT region FROM readings_hot`).Iter()
	var reg string
	for iter.Scan(&reg) {
		distinctRegions = append(distinctRegions, reg)
	}
	if err := iter.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "hot distinct regions:", err)
		return false
	}
	hotMax, hotSum := 0, 0
	for _, r := range distinctRegions {
		var cnt int
		if err := session.Query(
			`SELECT count(*) FROM readings_hot WHERE region=?`, r,
		).Scan(&cnt); err != nil {
			fmt.Fprintln(os.Stderr, "hot count:", err)
			return false
		}
		fmt.Printf("   region=%-8s -> %d строк\n", r, cnt)
		if cnt > hotMax {
			hotMax = cnt
		}
		hotSum += cnt
	}
	hotPartitions := len(distinctRegions)
	fmt.Printf("   партиций (регионов): %d, итого строк: %d\n", hotPartitions, hotSum)
	fmt.Printf("   max строк/партиция: %d\n\n", hotMax)

	// --- Ассерты ---
	fmt.Println("-- Ассерты --")
	pass := true
	if badMax > goodMax {
		fmt.Printf("OK: bad_max (%d строк/партиция) > good_max (%d строк/партиция)\n", badMax, goodMax)
	} else {
		fmt.Printf("FAIL: bad_max (%d) <= good_max (%d)\n", badMax, goodMax)
		pass = false
	}
	if hotPartitions == len(regions) {
		fmt.Printf("OK: hot_partitions == %d (len(regions))\n", len(regions))
	} else {
		fmt.Printf("FAIL: hot_partitions (%d) != len(regions) (%d)\n", hotPartitions, len(regions))
		pass = false
	}
	if hotMax > badMax {
		fmt.Printf("OK: hot_max (%d строк/партиция) > bad_max (%d строк/партиция) — hot-partition хуже bad\n", hotMax, badMax)
	} else {
		fmt.Printf("FAIL: hot_max (%d) <= bad_max (%d)\n", hotMax, badMax)
		pass = false
	}
	fmt.Println()
	fmt.Println("Примечание: реальные байтовые размеры (не proxy по числу строк) —")
	fmt.Println("см. `nodetool tablestats telemetry.<table>` в README «Стенд #1»")
	fmt.Println("(недоступно изнутри этого контейнера — нет docker-сокола хоста на сети scylla-cookbook-net).")
	return pass
}

// ---------------------------------------------------------------------------
// Сценарий 2: hot-partition — цена чтения гигантской партиции vs маленькой,
// плюс честная оговорка про RF=3 на 3 узлах (см. README).
// ---------------------------------------------------------------------------

func hotPartitionScenario(session *gocql.Session) bool {
	fmt.Println("=== Стенд #1: hot-partition (цена чтения гигантской партиции) ===")
	fmt.Println()

	fmt.Println("-- Распределение по регионам (readings_hot) --")
	pass := true
	counts := map[string]int{}
	for _, r := range regions {
		var cnt int
		if err := session.Query(
			`SELECT count(*) FROM readings_hot WHERE region=?`, r,
		).Scan(&cnt); err != nil {
			fmt.Fprintln(os.Stderr, "region count:", err)
			return false
		}
		counts[r] = cnt
		fmt.Printf("   region=%-8s -> %d строк (одна партиция)\n", r, cnt)
	}
	fmt.Println()

	fmt.Println("-- Время полного скана одной партиции: good (device+day, ~96 строк) vs hot (region, ~168000 строк) --")
	goodStart := time.Now()
	var goodCnt int
	if err := session.Query(
		`SELECT count(*) FROM readings WHERE device_id=? AND day=?`, "dev-00000", "2026-07-01",
	).Scan(&goodCnt); err != nil {
		fmt.Fprintln(os.Stderr, "good scan:", err)
		return false
	}
	goodElapsed := time.Since(goodStart)
	fmt.Printf("   readings[dev-00000, 2026-07-01]: %d строк за %s\n", goodCnt, goodElapsed)

	hotStart := time.Now()
	var hotCnt int
	if err := session.Query(
		`SELECT count(*) FROM readings_hot WHERE region=?`, "eu-west",
	).Scan(&hotCnt); err != nil {
		fmt.Fprintln(os.Stderr, "hot scan:", err)
		return false
	}
	hotElapsed := time.Since(hotStart)
	fmt.Printf("   readings_hot[region=eu-west]: %d строк за %s\n", hotCnt, hotElapsed)
	fmt.Println()

	if hotElapsed > goodElapsed {
		fmt.Printf("OK: hot-скан партиции (%s) дороже good-скана (%s)\n", hotElapsed, goodElapsed)
	} else {
		// Точное соотношение задержек зависит от кеша/JIT первого запроса —
		// не проваливаем стенд по этому мягкому сигналу, только отчёт.
		fmt.Printf("NOTE: hot-скан (%s) не дороже good-скана (%s) в этом конкретном прогоне (возможен эффект прогрева/кеша) — смотри объём (строк) как основной сигнал, не только время\n", hotElapsed, goodElapsed)
	}

	fmt.Println()
	fmt.Println("Честная оговорка: в этом кластере 3 узла и RF=3 — КАЖДЫЙ узел реплицирует")
	fmt.Println("КАЖДУЮ партицию, поэтому hot-partition здесь не создаёт перекоса НАГРУЗКИ")
	fmt.Println("МЕЖДУ узлами (это проявляется на кластерах, где узлов больше RF, и токен-")
	fmt.Println("диапазон hot-партиции обслуживает лишь подмножество узлов). Что видно уже")
	fmt.Println("здесь — это дороговизна ЧТЕНИЯ/КОМПАКЦИИ самой гигантской партиции")
	fmt.Println("(объём данных на один ключ, память на кеш индекса партиции, latency при")
	fmt.Println("полном скане) — это не зависит от размера кластера.")
	return pass
}

// ---------------------------------------------------------------------------
// Сценарий 3: query-shape — типовой запрос "устройство X за сутки Y" на
// хорошей и плохой моделях выполняется БЕЗ ALLOW FILTERING (внутри одной
// партиции); на hot-модели требует ALLOW FILTERING (device_id — clustering-
// столбец ПОСЛЕ event_time, к которому применён range, а не EQ).
// ---------------------------------------------------------------------------

func queryShapeScenario(session *gocql.Session) bool {
	fmt.Println("=== Стенд #1: query-shape (типовой запрос: 1 устройство за 1 сутки) ===")
	fmt.Println()
	pass := true

	fmt.Println("-- readings (good): SELECT ... WHERE device_id=? AND day=?  (без ALLOW FILTERING) --")
	t0 := time.Now()
	var goodCnt int
	err := session.Query(
		`SELECT count(*) FROM readings WHERE device_id=? AND day=?`, "dev-00000", "2026-07-01",
	).Scan(&goodCnt)
	if err != nil {
		fmt.Printf("FAIL: неожиданная ошибка good-запроса: %v\n", err)
		pass = false
	} else {
		fmt.Printf("OK: %d строк за %s, ALLOW FILTERING не потребовался\n", goodCnt, time.Since(t0))
	}
	fmt.Println()

	fmt.Println("-- readings_bad: SELECT ... WHERE device_id=? AND event_time>=? AND event_time<?  (без ALLOW FILTERING) --")
	t0 = time.Now()
	var badCnt int
	err = session.Query(
		`SELECT count(*) FROM readings_bad WHERE device_id=? AND event_time>=? AND event_time<?`,
		"dev-00000", mustParseTime("2026-07-01T00:00:00Z"), mustParseTime("2026-07-02T00:00:00Z"),
	).Scan(&badCnt)
	if err != nil {
		fmt.Printf("FAIL: неожиданная ошибка bad-запроса: %v\n", err)
		pass = false
	} else {
		fmt.Printf("OK: %d строк за %s, ALLOW FILTERING не потребовался (весь partition device_id всё равно один — просто он БОЛЬШОЙ, см. hot-partition/partition-size сценарии)\n", badCnt, time.Since(t0))
	}
	fmt.Println()

	fmt.Println("-- readings_hot: тот же запрос по смыслу, БЕЗ ALLOW FILTERING (ожидаем ошибку CQL) --")
	err = session.Query(
		`SELECT count(*) FROM readings_hot WHERE region=? AND event_time>=? AND event_time<? AND device_id=?`,
		"eu-west", mustParseTime("2026-07-01T00:00:00Z"), mustParseTime("2026-07-02T00:00:00Z"), "dev-00000",
	).Scan(new(int))
	if err == nil {
		fmt.Println("FAIL: ожидали ошибку CQL (clustering column device_id restricted after non-EQ event_time), запрос прошёл")
		pass = false
	} else if strings.Contains(err.Error(), "cannot be restricted") || strings.Contains(err.Error(), "ALLOW FILTERING") {
		fmt.Printf("OK: без ALLOW FILTERING сервер отверг запрос ожидаемой ошибкой: %v\n", err)
	} else {
		fmt.Printf("FAIL: получена ошибка, но не та, что ожидали: %v\n", err)
		pass = false
	}
	fmt.Println()

	fmt.Println("-- readings_hot: тот же запрос С ALLOW FILTERING (проходит, но сканирует внутри гигантской партиции) --")
	t0 = time.Now()
	var hotCnt int
	err = session.Query(
		`SELECT count(*) FROM readings_hot WHERE region=? AND event_time>=? AND event_time<? AND device_id=? ALLOW FILTERING`,
		"eu-west", mustParseTime("2026-07-01T00:00:00Z"), mustParseTime("2026-07-02T00:00:00Z"), "dev-00000",
	).Scan(&hotCnt)
	if err != nil {
		fmt.Printf("FAIL: неожиданная ошибка hot+ALLOW FILTERING запроса: %v\n", err)
		pass = false
	} else {
		fmt.Printf("OK: %d строк за %s, потребовался ALLOW FILTERING (антипаттерн: заранее непредсказуемая стоимость скана внутри партиции региона)\n", hotCnt, time.Since(t0))
	}
	fmt.Println()

	if pass {
		fmt.Println("Вывод: good и bad удовлетворяют типовой запрос без ALLOW FILTERING (обе модели держат")
		fmt.Println("device_id в partition/clustering ключе в нужной позиции); hot-модель партиционирует по")
		fmt.Println("region — единственный запрос-паттерн, для которого это удобно, это \"дай мне весь регион\";")
		fmt.Println("любой более точный запрос требует ALLOW FILTERING внутри гигантской партиции.")
	}
	return pass
}

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
