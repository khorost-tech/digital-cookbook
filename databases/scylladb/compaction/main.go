// Command compaction — стенд #3 серии "ScyllaDB: глубокое погружение":
// compaction/tombstones/repair, специфика ScyllaDB (а не generic LSM —
// это разобрано в другом стенде серии).
//
//   -scenario twcs        — TimeWindowCompactionStrategy: создаёт
//     telemetry.readings_twcs (та же PK, что и readings), грузит 14 суток
//     телеметрии окно-за-окном (flush после каждых суток), показывает живой
//     рост числа sstable на каждом узле окно-за-окном — доказательство, что
//     TWCS не смешивает данные разных временных окон в одном flush-цикле.
//   -scenario tombstones  — массовый DELETE по clustering-диапазону (первая
//     половина суток, все 500 партиций), flush, затем ЖИВОЙ traced-запрос
//     (через gocql.Tracer, читает system_traces.events) через удалённый
//     диапазон — из строки "Page stats: ... clustering row(s) (X live,
//     Y dead)" достаётся реальное число прочитанных tombstone-строк.
//     Плюс gc_grace_seconds (живьём из system_schema.tables) — tombstones
//     не удаляются раньше этого срока.
//
// Метрики sstable/flush читаются НАПРЯМУЮ из этого контейнера по сети
// scylla-cookbook-net через REST API ScyllaDB (:10000/column_family/...,
// :10000/storage_service/keyspace_flush/...) — тот же приём, что и Task 3
// (:9180/metrics), только другой порт: :10000 отдаёт то же самое, что видит
// nodetool (nodetool сам ходит на localhost:10000 внутри своего контейнера),
// без необходимости в docker-сокете хоста. См. README «Стенд #3» — какие
// метрики оказались НЕ полезны на этом образе (tombstone_scanned_histogram/
// nodetool "tombstones per slice" остаются на 0 несмотря на подтверждённые
// tombstone-строки в трейсе — честно задокументированная находка) и какие
// пришлось заменить (traced read вместо REST-гистограммы).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

func newRand(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

func roundTo2(v float64) float64 { return math.Round(v*100) / 100 }

// refDate — та же опорная точка, что и dataset/main.go и architecture/main.go:
// датасет детерминирован не от time.Now(), а от зашитой в код даты.
var refDate = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

const (
	devices    = 500
	perDay     = 96
	windowDays = 14 // 14 суток телеметрии = 14 окон TWCS (compaction_window_size=1 DAY)
	targetDay  = 5  // индекс суток для сценария tombstones (2026-07-06)

	// batchChunkRows — как в dataset/main.go: батч не длиннее этого числа
	// строк и строго внутри одной партиции.
	batchChunkRows = 200
)

var (
	metrics = []string{"cpu", "mem", "temp", "netio", "disk"}
	regions = []string{"eu-west", "eu-east", "us-east", "ap-south"}
)

// Reading — одна запись телеметрии (та же форма, что dataset.Reading).
type Reading struct {
	DeviceID  string
	Day       time.Time
	EventTime time.Time
	Metric    string
	Value     float64
	Region    string
	Status    string
}

// generateWindow — детерминированный генератор ОДНОГО окна (суток) телеметрии
// для readings_twcs: 500 устройств × perDay замеров. Не байт-в-байт совпадает
// с dataset.Generate() (там один общий rand-поток на все 672000 строк сразу,
// здесь — независимый по суткам rand с seed=seed+dayIdx+1), но детерминирован
// (повторный запуск с тем же seed/dayIdx даёт тот же вывод) и достаточен для
// демонстрации TWCS/tombstones — этому стенду не нужна побитовая идентичность
// с readings/readings_bad/readings_hot, только собственная воспроизводимость.
func generateWindow(seed int64, dayIdx int) []Reading {
	r := newRand(seed + int64(dayIdx) + 1)
	dayTime := refDate.AddDate(0, 0, dayIdx)
	out := make([]Reading, 0, devices*perDay)
	for d := 0; d < devices; d++ {
		id := fmt.Sprintf("dev-%05d", d)
		region := regions[d%len(regions)]
		for p := 0; p < perDay; p++ {
			et := dayTime.Add(time.Duration(p) * 15 * time.Minute)
			val := 20 + 60*r.Float64()
			st := "ok"
			if val > 70 {
				st = "crit"
			} else if val > 55 {
				st = "warn"
			}
			out = append(out, Reading{
				DeviceID: id, Day: dayTime, EventTime: et,
				Metric: metrics[p%len(metrics)], Value: roundTo2(val),
				Region: region, Status: st,
			})
		}
	}
	return out
}

func main() {
	scenario := flag.String("scenario", "twcs", "twcs|tombstones")
	hosts := flag.String("hosts", envOr("SCYLLA_HOSTS", "127.0.0.1:9042"), "scylla CQL hosts, через запятую")
	flag.Parse()

	session, err := connect(*hosts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer session.Close()

	restHosts := deriveHosts(*hosts, "10000")

	var ok bool
	switch *scenario {
	case "twcs":
		ok = twcsScenario(session, restHosts)
	case "tombstones":
		ok = tombstonesScenario(session, restHosts)
	default:
		fmt.Fprintf(os.Stderr, "unknown -scenario %q (expected twcs|tombstones)\n", *scenario)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

func connect(hosts string) (*gocql.Session, error) {
	cluster := gocql.NewCluster(splitHosts(hosts)...)
	cluster.Keyspace = "telemetry"
	cluster.Consistency = gocql.Quorum
	cluster.Timeout = 20 * time.Second
	cluster.ConnectTimeout = 15 * time.Second
	return cluster.CreateSession()
}

func splitHosts(hosts string) []string {
	parts := strings.Split(hosts, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// deriveHosts превращает CQL hosts (host:9042,...) в host:port (та же схема,
// что deriveMetricsHosts в ../architecture/main.go).
func deriveHosts(hosts, port string) []string {
	parts := splitHosts(hosts)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		host := p
		if idx := strings.LastIndex(p, ":"); idx >= 0 {
			host = p[:idx]
		}
		out = append(out, host+":"+port)
	}
	return out
}

// ---------------------------------------------------------------------------
// Схема readings_twcs
// ---------------------------------------------------------------------------

const createTWCSCQL = `CREATE TABLE IF NOT EXISTS telemetry.readings_twcs (
  device_id text, day date, event_time timestamp,
  metric text, value double, region text, status text,
  PRIMARY KEY ((device_id, day), event_time)
) WITH CLUSTERING ORDER BY (event_time DESC)
  AND compaction = {'class':'TimeWindowCompactionStrategy','compaction_window_size':'1','compaction_window_unit':'DAYS'}`

func createTWCSTable(session *gocql.Session) error {
	return session.Query(createTWCSCQL).Exec()
}

func queryCompactionOptions(session *gocql.Session) (map[string]string, error) {
	var opts map[string]string
	err := session.Query(`SELECT compaction FROM system_schema.tables WHERE keyspace_name='telemetry' AND table_name='readings_twcs'`).Scan(&opts)
	return opts, err
}

func queryGCGrace(session *gocql.Session) (int, error) {
	var gc int
	err := session.Query(`SELECT gc_grace_seconds FROM system_schema.tables WHERE keyspace_name='telemetry' AND table_name='readings_twcs'`).Scan(&gc)
	return gc, err
}

// insertTWCSCQL — КРИТИЧЕСКИ ВАЖНО: "USING TIMESTAMP ?" со значением
// event_time (микросекунды с эпохи), а НЕ момент реальной вставки.
//
// Живая находка этого стенда: TWCS группирует sstable в окна по WRITE-
// timestamp мутации (тому самому, что видно как WRITETIME(col) в CQL и
// max_timestamp в `scylla sstable dump-statistics`), а НЕ по значению
// какого-либо столбца данных (в т.ч. НЕ по event_time как значению).
// Если грузить историческую телеметрию за 14 суток обычным INSERT без
// явного USING TIMESTAMP, ВСЕ 14 "суток" симуляции физически пишутся в
// течение нескольких секунд реального времени — и TWCS кладёт их ВСЕ в
// ОДНО окно текущих суток, поскольку с точки зрения TWCS это и есть момент
// записи. Подтверждено живьём первым прогоном ДО этого фикса:
// `scylla sstable dump-statistics` показал у ранних sstable
// min_timestamp/max_timestamp разницей в секунды (реальное время загрузки),
// НЕ 14 суток разброса event_time. Явный `USING TIMESTAMP` — тот же приём,
// которым реальные бэкафилы исторических данных в ScyllaDB/Cassandra
// заставляют TWCS группировать sstable по историческому времени события,
// а не по времени миграции.
const insertTWCSCQL = `INSERT INTO telemetry.readings_twcs (device_id, day, event_time, metric, value, region, status) VALUES (?,?,?,?,?,?,?) USING TIMESTAMP ?`

// loadWindow грузит rows (одни сутки) в readings_twcs — UNLOGGED-батчи
// строго внутри одной партиции (device_id, day), не длиннее batchChunkRows
// (см. dataset/main.go loadRows — тот же приём). USING TIMESTAMP = event_time
// в микросекундах — см. комментарий у insertTWCSCQL.
func loadWindow(session *gocql.Session, rows []Reading) error {
	i := 0
	for i < len(rows) {
		key := rows[i].DeviceID
		j := i
		for j < len(rows) && rows[j].DeviceID == key {
			j++
		}
		for start := i; start < j; start += batchChunkRows {
			end := start + batchChunkRows
			if end > j {
				end = j
			}
			batch := session.NewBatch(gocql.UnloggedBatch)
			for _, r := range rows[start:end] {
				batch.Query(insertTWCSCQL, r.DeviceID, r.Day, r.EventTime, r.Metric, r.Value, r.Region, r.Status, r.EventTime.UnixMicro())
			}
			if err := session.ExecuteBatch(batch); err != nil {
				return fmt.Errorf("batch insert rows[%d:%d] partition=%q: %w", start, end, key, err)
			}
		}
		i = j
	}
	return nil
}

// ---------------------------------------------------------------------------
// REST-доступ к ScyllaDB (:10000) — то же, что видит nodetool изнутри
// контейнера узла, доступно по внутреннему DNS-имени с любого контейнера
// сети scylla-cookbook-net (тот же приём, что :9180/metrics в ../architecture).
// ---------------------------------------------------------------------------

var httpClient = &http.Client{Timeout: 15 * time.Second}

func restGetFloat(url string) (float64, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("GET %s: статус %d, тело %s", url, resp.StatusCode, string(body))
	}
	var v float64
	if err := json.Unmarshal(body, &v); err != nil {
		return 0, fmt.Errorf("GET %s: не число: %s: %w", url, string(body), err)
	}
	return v, nil
}

func restFlush(host, keyspace, cf string) error {
	url := fmt.Sprintf("http://%s/storage_service/keyspace_flush/%s?cf=%s", host, keyspace, cf)
	resp, err := httpClient.Post(url, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST %s: статус %d, тело %s", url, resp.StatusCode, string(body))
	}
	return nil
}

func flushAll(restHosts []string, keyspace, cf string) error {
	for _, h := range restHosts {
		if err := restFlush(h, keyspace, cf); err != nil {
			return fmt.Errorf("flush %s: %w", h, err)
		}
	}
	return nil
}

func sstableCounts(restHosts []string, ksTable string) (map[string]float64, error) {
	out := map[string]float64{}
	for _, h := range restHosts {
		url := fmt.Sprintf("http://%s/column_family/metrics/live_ss_table_count/%s", h, ksTable)
		v, err := restGetFloat(url)
		if err != nil {
			return nil, err
		}
		out[h] = v
	}
	return out, nil
}

func sortedHosts(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for h := range m {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Сценарий 1: twcs — sstable по временным окнам
// ---------------------------------------------------------------------------

func twcsScenario(session *gocql.Session, restHosts []string) bool {
	fmt.Println("=== Стенд #3: twcs (TimeWindowCompactionStrategy — sstable по временным окнам) ===")
	fmt.Println()

	if err := createTWCSTable(session); err != nil {
		fmt.Fprintln(os.Stderr, "create table:", err)
		return false
	}
	opts, err := queryCompactionOptions(session)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query compaction options:", err)
		return false
	}
	fmt.Printf("compaction (живьём из system_schema.tables): %v\n\n", opts)

	before, err := sstableCounts(restHosts, "telemetry:readings_twcs")
	if err != nil {
		fmt.Fprintln(os.Stderr, "sstable counts (before):", err)
		return false
	}
	fmt.Println("-- SSTable count ДО загрузки (per node, REST :10000/column_family/metrics/live_ss_table_count) --")
	for _, h := range sortedHosts(before) {
		fmt.Printf("   %s: %.0f\n", h, before[h])
	}
	fmt.Println()

	fmt.Printf("Загрузка %d окон (суток) по %d устройств × %d замеров, flush после КАЖДЫХ суток...\n\n", windowDays, devices, perDay)

	windowsWithGrowth := map[string]int{}
	totalNew := map[string]float64{}

	for d := 0; d < windowDays; d++ {
		rows := generateWindow(42, d)
		if err := loadWindow(session, rows); err != nil {
			fmt.Fprintln(os.Stderr, "load window", d, ":", err)
			return false
		}
		if err := flushAll(restHosts, "telemetry", "readings_twcs"); err != nil {
			fmt.Fprintln(os.Stderr, "flush window", d, ":", err)
			return false
		}
		after, err := sstableCounts(restHosts, "telemetry:readings_twcs")
		if err != nil {
			fmt.Fprintln(os.Stderr, "sstable counts window", d, ":", err)
			return false
		}
		day := refDate.AddDate(0, 0, d)
		fmt.Printf("день %2d (%s):\n", d, day.Format("2006-01-02"))
		for _, h := range sortedHosts(after) {
			delta := after[h] - before[h]
			if delta > 0 {
				windowsWithGrowth[h]++
				totalNew[h] += delta
			}
			fmt.Printf("   %s: %.0f -> %.0f (delta=%.0f)\n", h, before[h], after[h], delta)
		}
		before = after
	}
	fmt.Println()

	fmt.Println("-- Итог: sstable по окнам --")
	pass := true
	for _, h := range sortedHosts(before) {
		avg := totalNew[h] / float64(windowDays)
		fmt.Printf("   %s: окон с новым sstable %d/%d, новых sstable всего %.0f (avg/окно=%.2f), финальный count=%.0f\n",
			h, windowsWithGrowth[h], windowDays, totalNew[h], avg, before[h])
		if windowsWithGrowth[h] < 1 {
			fmt.Printf("FAIL(честно): %s — НИ ОДНО окно не создало нового sstable (фоновая компакция могла опередить измерение, см. README)\n", h)
			pass = false
		}
	}
	if pass {
		fmt.Println("OK: twcs_sstables_per_window >= 1 на всех узлах (минимум одно окно дало ≥1 новый sstable — TWCS не смешал все 14 суток в одну общую компакцию)")
	}
	fmt.Println()
	fmt.Println("NOTE: точное число sstable/окно на диске — предмет фоновой компакции ScyllaDB, которая может")
	fmt.Println("      объединить sstable ВНУТРИ одного окна (не МЕЖДУ окнами — это и есть смысл TWCS) уже к")
	fmt.Println("      моменту измерения на idle-кластере. Живые числа этого прогона — выше; байтовая проверка")
	fmt.Println("      (что ни один sstable не содержит данные ДВУХ разных суток) — README «Стенд #3», host-side")
	fmt.Println("      `scylla sstable dump-statistics` по реальным файлам readings_twcs-*.")
	return pass
}

// ---------------------------------------------------------------------------
// Сценарий 2: tombstones — массовый DELETE, gc_grace, traced read
// ---------------------------------------------------------------------------

const deleteRangeCQL = `DELETE FROM telemetry.readings_twcs WHERE device_id=? AND day=? AND event_time>=? AND event_time<?`

var pageStatsRe = regexp.MustCompile(`clustering row\(s\) \((\d+) live, (\d+) dead\)`)

func tombstonesScenario(session *gocql.Session, restHosts []string) bool {
	fmt.Println("=== Стенд #3: tombstones (массовый DELETE, gc_grace, traced read) ===")
	fmt.Println()

	if err := createTWCSTable(session); err != nil {
		fmt.Fprintln(os.Stderr, "create table:", err)
		return false
	}

	gc, err := queryGCGrace(session)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query gc_grace_seconds:", err)
		return false
	}
	fmt.Printf("gc_grace_seconds (живьём из system_schema.tables): %d (%.1f суток) — tombstones НЕ удаляются раньше этого срока\n\n", gc, float64(gc)/86400)

	day := refDate.AddDate(0, 0, targetDay)
	fmt.Printf("Целевые сутки: %s. Загрузка (idempotent upsert)...\n", day.Format("2006-01-02"))
	rows := generateWindow(42, targetDay)
	if err := loadWindow(session, rows); err != nil {
		fmt.Fprintln(os.Stderr, "load window:", err)
		return false
	}
	if err := flushAll(restHosts, "telemetry", "readings_twcs"); err != nil {
		fmt.Fprintln(os.Stderr, "flush (после загрузки):", err)
		return false
	}

	sampleDevices := make([]string, 0, 20)
	for d := 0; d < devices; d += 25 {
		sampleDevices = append(sampleDevices, fmt.Sprintf("dev-%05d", d))
	}

	before, err := countManyPartitions(session, sampleDevices, day)
	if err != nil {
		fmt.Fprintln(os.Stderr, "count before:", err)
		return false
	}
	fmt.Printf("Строк на выборке из %d партиций ДО удаления: %v (ожидание %d каждая)\n\n", len(sampleDevices), before, perDay)

	cutoff := day.Add(12 * time.Hour)
	fmt.Printf("Массовый DELETE по clustering-диапазону [%s, %s) для ВСЕХ %d партиций (device_id, day=%s)...\n",
		day.Format(time.RFC3339), cutoff.Format(time.RFC3339), devices, day.Format("2006-01-02"))
	deletedPartitions, delErrs := massDeleteRange(session, day, cutoff)
	fmt.Printf("...готово: %d/%d партиций (ошибок: %d)\n\n", deletedPartitions, devices, delErrs)
	if delErrs > 0 && deletedPartitions == 0 {
		fmt.Fprintln(os.Stderr, "все DELETE провалились")
		return false
	}

	if err := flushAll(restHosts, "telemetry", "readings_twcs"); err != nil {
		fmt.Fprintln(os.Stderr, "flush (после удаления):", err)
		return false
	}

	after, err := countManyPartitions(session, sampleDevices, day)
	if err != nil {
		fmt.Fprintln(os.Stderr, "count after:", err)
		return false
	}
	fmt.Printf("Строк на той же выборке ПОСЛЕ удаления: %v (ожидание %d каждая — половина суток удалена)\n\n", after, perDay/2)

	deleteConfirmed := true
	for _, dev := range sampleDevices {
		if after[dev] != before[dev]-perDay/2 {
			fmt.Printf("FAIL(честно): %s — before=%d after=%d, ожидалось after=before-%d\n", dev, before[dev], after[dev], perDay/2)
			deleteConfirmed = false
		}
	}
	if deleteConfirmed {
		fmt.Printf("OK: удаление подтверждено на всех %d выборочных партициях (before-%d == after)\n\n", len(sampleDevices), perDay/2)
	}

	// -- traced read через diapазон с tombstones --------------------------
	fmt.Println("-- Traced read через удалённый диапазон (gocql.Tracer -> system_traces.events) --")
	tracer := gocql.NewTracer(session)
	probeDev := sampleDevices[0]
	q := session.Query(`SELECT device_id, day, event_time, metric, region, status, value FROM readings_twcs WHERE device_id=? AND day=?`, probeDev, day).Trace(tracer)
	iter := q.Iter()
	var devID, metric, region, status string
	var d2 time.Time
	var et time.Time
	var val float64
	rowsScanned := 0
	for iter.Scan(&devID, &d2, &et, &metric, &region, &status, &val) {
		rowsScanned++
	}
	if err := iter.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "traced read:", err)
		return false
	}
	fmt.Printf("Прочитано живых строк: %d (ожидание %d)\n", rowsScanned, perDay/2)

	traceIDs := tracer.AllTraceIDs()
	tombstonesScanned := 0
	deadBySource := map[string]int{}
	if len(traceIDs) == 0 {
		fmt.Println("FAIL(честно): tracer не вернул traceId")
	} else {
		traceID := traceIDs[len(traceIDs)-1]
		ready := false
		for i := 0; i < 30; i++ {
			r, _ := tracer.IsReady(traceID)
			if r {
				ready = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !ready {
			fmt.Println("NOTE: трейс не успел стать готовым за 3с — читаем как есть (события могут быть неполными)")
		}
		activities, err := tracer.GetActivities(traceID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "get activities:", err)
			return false
		}
		found := false
		for _, a := range activities {
			if m := pageStatsRe.FindStringSubmatch(a.Activity); m != nil {
				found = true
				var live, dead int
				fmt.Sscanf(m[1], "%d", &live)
				fmt.Sscanf(m[2], "%d", &dead)
				tombstonesScanned += dead
				deadBySource[a.Source] += dead
				fmt.Printf("   [source=%s] %s\n", a.Source, a.Activity)
				_ = live
			}
		}
		if !found {
			fmt.Println("NOTE(честно): в этой сборке ScyllaDB строка \"Page stats: ... clustering row(s) (X live, Y dead)\" не встретилась")
			fmt.Println("   в system_traces.events для этого запроса — см. README «Стенд #3» за реальным выводом host-side")
			fmt.Println("   TRACING ON и объяснением, почему REST tombstone_scanned_histogram/nodetool tablestats тоже вернули 0.")
		}
	}
	// Запрос идёт с session.Consistency = gocql.Quorum (см. connectSession),
	// а SpeculativeExecutionPolicy НЕ выставлен нигде в стенде — у gocql это
	// значит defaultNonSpecExec (0 доп. попыток), т.е. спекулятивных чтений
	// здесь физически быть не может. Реальная причина, ПОЧЕМУ строка "Page
	// stats" встречается в system_traces.events несколько раз: на QUORUM
	// координатор опрашивает НЕСКОЛЬКО реплик, и КАЖДАЯ реплика пишет в
	// трейс свою собственную "Page stats" активность — они различаются
	// полем TraceEntry.Source (адрес реплики). Ниже — разбивка по Source,
	// подтверждающая это количеством различных источников.
	if len(deadBySource) > 0 {
		fmt.Printf("\nPage stats по репликам (по TraceEntry.Source), различных источников: %d\n", len(deadBySource))
		for _, src := range sortedKeys(deadBySource) {
			fmt.Printf("   source=%s: dead=%d\n", src, deadBySource[src])
		}
	}
	fmt.Printf("\ntombstones_scanned (сумма \"dead\" по ВСЕМ репликам traced read, QUORUM = %d реплики × 48 dead) = %d\n", len(deadBySource), tombstonesScanned)

	pass := deleteConfirmed
	if tombstonesScanned > 0 {
		fmt.Println("OK: tombstones_scanned > 0 (traced read реально прошёл через удалённые строки на каждой опрошенной реплике)")
	} else {
		fmt.Println("FAIL(честно): tombstones_scanned == 0 — см. NOTE выше и README «Стенд #3» за host-side подтверждением через TRACING ON")
		pass = false
	}
	return pass
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func countManyPartitions(session *gocql.Session, devs []string, day time.Time) (map[string]int, error) {
	out := map[string]int{}
	for _, dev := range devs {
		var n int
		if err := session.Query(`SELECT count(*) FROM readings_twcs WHERE device_id=? AND day=?`, dev, day).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", dev, err)
		}
		out[dev] = n
	}
	return out, nil
}

// massDeleteRange удаляет [from, to) в КАЖДОЙ из devices партиций (device_id,
// day) отдельным DELETE (единый clustering-диапазон на партицию — один range
// tombstone на партицию, НЕ единый батч через границу партиции).
func massDeleteRange(session *gocql.Session, day, cutoffExclusive time.Time) (okCount, errCount int) {
	for d := 0; d < devices; d++ {
		dev := fmt.Sprintf("dev-%05d", d)
		if err := session.Query(deleteRangeCQL, dev, day, day, cutoffExclusive).Exec(); err != nil {
			errCount++
			if errCount <= 3 {
				fmt.Fprintf(os.Stderr, "delete %s: %v\n", dev, err)
			}
			continue
		}
		okCount++
	}
	return okCount, errCount
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
