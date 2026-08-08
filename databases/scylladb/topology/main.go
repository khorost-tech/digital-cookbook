// Command topology — стенд #5 серии "ScyllaDB: глубокое погружение":
// tablets (преемник vnodes) + multi-DC гео-репликация. В отличие от всех
// предыдущих стендов серии, работает НЕ с кластером Task 1
// (compose/compose.yml, scylla1/2/3, сеть scylla-cookbook-net — тот кластер
// НЕ трогается), а с ОТДЕЛЬНЫМ живым кластером `compose/multidc.yml` (2 ДЦ
// DC1/DC2 x 2 узла = 4, сеть scylla-multidc-net, узлы dc1a/dc1b/dc2a/dc2b).
//
//	-scenario multidc — создаёт keyspace telemetry_mdc
//	  ({'class':'NetworkTopologyStrategy','DC1':2,'DC2':2}), пишет N строк
//	  LOCAL_QUORUM с коордиатором, ПРИНУДИТЕЛЬНО закреплённым в DC1
//	  (cluster.HostFilter = gocql.DataCenterHostFilter("DC1") — драйвер
//	  вообще не открывает соединения к узлам DC2, поэтому координатором
//	  гарантированно становится узел DC1), читает ТЕ ЖЕ строки LOCAL_QUORUM
//	  с координатором, закреплённым в DC2 — подтверждает, что запись в DC1
//	  реплицировалась в DC2. Отдельно меряет латентность LOCAL_QUORUM
//	  (координатор DC1, ждёт только 2 локальные реплики DC1) против QUORUM
//	  (тот же координатор DC1, но ждёт большинство ВСЕХ 4 реплик кластера —
//	  минимум 3 из 4, то есть минимум 1 подтверждение от удалённого DC2).
//	-scenario tablets — создаёт отдельный keyspace telemetry_mdc_tablets
//	  ({'class':'NetworkTopologyStrategy','DC1':1,'DC2':1} — намеренно RF=1
//	  на ДЦ, не 2: это позволяет ops/topology-demo.sh decommission-ить ОДИН
//	  из двух узлов DC2 (dc2b), не нарушая топологию репликации — с RF
//	  DC2=2 такой decommission структурно невозможен, у DC2 осталось бы
//	  меньше узлов, чем требует RF), грузит детерминированный набор строк,
//	  затем читает РЕАЛЬНОЕ распределение tablets по узлам через
//	  system.tablets (см. README «Стенд #5» — схема этой таблицы в данной
//	  сборке ScyllaDB, live-проверено). Печатает текущее распределение —
//	  идемпотентно, безопасно вызывать ДО и ПОСЛЕ decommission (сравнение
//	  before/after делает ops/topology-demo.sh).
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

func main() {
	scenario := flag.String("scenario", "multidc", "multidc|tablets")
	hosts := flag.String("hosts", envOr("SCYLLA_HOSTS", "127.0.0.1:9043"), "CQL-хосты ВСЕХ 4 узлов multi-DC кластера через запятую (dc1a:9042,dc1b:9042,dc2a:9042,dc2b:9042); DC узла определяется по префиксу имени хоста dc1/dc2")
	n := flag.Int("n", 10000, "multidc: число write+read операций на каждый замер (LOCAL_QUORUM репликация + LOCAL_QUORUM vs QUORUM латентность)")
	rows := flag.Int("rows", 5000, "tablets: число строк, загружаемых в telemetry_mdc_tablets.tbench")
	seed := flag.Int64("seed", 42, "seed для значений (воспроизводимость)")
	flag.Parse()

	hostList := splitHosts(*hosts)

	var ok bool
	switch *scenario {
	case "multidc":
		ok = multidcScenario(hostList, *n, *seed)
	case "tablets":
		ok = tabletsScenario(hostList, *rows, *seed)
	default:
		fmt.Fprintf(os.Stderr, "unknown -scenario %q (expected multidc|tablets)\n", *scenario)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func splitHosts(hosts string) []string {
	parts := strings.Split(hosts, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitHostsByDC делит полный список CQL-хостов на DC1/DC2 по префиксу имени
// хоста (dc1a/dc1b -> DC1, dc2a/dc2b -> DC2) — соглашение ИМЕННО этого стенда
// (см. compose/multidc.yml, имена контейнеров/сервисов).
func splitHostsByDC(hosts []string) (dc1, dc2 []string, err error) {
	for _, h := range hosts {
		name := h
		if idx := strings.Index(h, ":"); idx >= 0 {
			name = h[:idx]
		}
		switch {
		case strings.HasPrefix(name, "dc1"):
			dc1 = append(dc1, h)
		case strings.HasPrefix(name, "dc2"):
			dc2 = append(dc2, h)
		default:
			return nil, nil, fmt.Errorf("host %q: не удалось определить DC по префиксу имени (ожидался dc1*/dc2*)", h)
		}
	}
	if len(dc1) == 0 || len(dc2) == 0 {
		return nil, nil, fmt.Errorf("нужны хосты в ОБОИХ DC (dc1=%v dc2=%v) — передайте все 4 узла в -hosts/SCYLLA_HOSTS", dc1, dc2)
	}
	return dc1, dc2, nil
}

// connectDC создаёт сессию, координатор для КАЖДОГО запроса которой
// гарантированно физически принадлежит localDC — HostFilter не даёт
// драйверу вообще открыть пул соединений к узлам другого ДЦ (в отличие от
// одной лишь DCAwareRoundRobinPolicy, которая по умолчанию допускает
// fail-over на другой ДЦ при недоступности локального). Это именно то,
// что нужно, чтобы честно утверждать "запись/чтение с координатором в DC1"
// или "в DC2" — не полагаясь на догадку, куда решит пойти политика выбора.
func connectDC(hosts []string, localDC string, defaultCL gocql.Consistency) (*gocql.Session, error) {
	cluster := gocql.NewCluster(hosts...)
	cluster.Consistency = defaultCL
	cluster.Timeout = 20 * time.Second
	cluster.ConnectTimeout = 15 * time.Second
	cluster.HostFilter = gocql.DataCenterHostFilter(localDC)
	cluster.PoolConfig.HostSelectionPolicy = gocql.DCAwareRoundRobinPolicy(localDC, gocql.HostPolicyOptionDisableDCFailover)
	return cluster.CreateSession()
}

// connectSingleHost открывает сессию, физически закреплённую РОВНО на одном
// узле (WhiteListHostFilter отбрасывает остальные, даже если control
// connection узнаёт о них из system.peers) — нужно для system.local host_id
// каждого КОНКРЕТНОГО узла (nodeHostIDs), где важно "чей это ответ", а не
// "ответ какого-то узла кластера".
func connectSingleHost(host string) (*gocql.Session, error) {
	cluster := gocql.NewCluster(host)
	cluster.Timeout = 15 * time.Second
	cluster.ConnectTimeout = 15 * time.Second
	cluster.HostFilter = gocql.WhiteListHostFilter(host)
	return cluster.CreateSession()
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

// ---------------------------------------------------------------------------
// -scenario multidc
// ---------------------------------------------------------------------------

const createMdcKeyspaceCQL = `CREATE KEYSPACE IF NOT EXISTS telemetry_mdc
  WITH replication = {'class':'NetworkTopologyStrategy','DC1':2,'DC2':2}`
const createMdcTableCQL = `CREATE TABLE IF NOT EXISTS telemetry_mdc.mdc_bench (id text PRIMARY KEY, val double)`
const mdcWriteCQL = `INSERT INTO telemetry_mdc.mdc_bench (id, val) VALUES (?, ?)`
const mdcReadCQL = `SELECT val FROM telemetry_mdc.mdc_bench WHERE id = ?`

func multidcScenario(hosts []string, n int, seed int64) bool {
	fmt.Println("=== Стенд #5: multidc (репликация DC1->DC2 + LOCAL_QUORUM vs QUORUM) ===")
	fmt.Println()

	dc1Hosts, dc2Hosts, err := splitHostsByDC(hosts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "split hosts by DC:", err)
		return false
	}
	fmt.Printf("DC1 хосты: %v\nDC2 хосты: %v\n\n", dc1Hosts, dc2Hosts)

	sessionDC1, err := connectDC(dc1Hosts, "DC1", gocql.LocalQuorum)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect DC1:", err)
		return false
	}
	defer sessionDC1.Close()

	fmt.Println("-- Схема: telemetry_mdc (NetworkTopologyStrategy, DC1:2, DC2:2) --")
	if err := sessionDC1.Query(createMdcKeyspaceCQL).Exec(); err != nil {
		fmt.Fprintln(os.Stderr, "create keyspace telemetry_mdc:", err)
		return false
	}
	if err := sessionDC1.Query(createMdcTableCQL).Exec(); err != nil {
		fmt.Fprintln(os.Stderr, "create table mdc_bench:", err)
		return false
	}
	fmt.Println("OK: keyspace/table готовы")
	fmt.Println()

	runID := time.Now().UnixNano()

	// -- Фаза A: N записей LOCAL_QUORUM, координатор ЖЁСТКО в DC1 --
	fmt.Printf("-- Фаза A: %d записей LOCAL_QUORUM (координатор закреплён в DC1, ждёт 2/2 реплик DC1) --\n", n)
	keys := make([]string, 0, n)
	vals := make([]float64, 0, n)
	var lqWriteDur []time.Duration
	var lqWriteErrs int
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("mdc-%d-lq-%06d", runID, i)
		val := float64(i) + float64(seed)/1000.0
		t0 := time.Now()
		err := sessionDC1.Query(mdcWriteCQL, key, val).Consistency(gocql.LocalQuorum).Exec()
		d := time.Since(t0)
		if err != nil {
			lqWriteErrs++
			if lqWriteErrs <= 3 {
				fmt.Fprintf(os.Stderr, "  write #%d: %v\n", i, err)
			}
			continue
		}
		lqWriteDur = append(lqWriteDur, d)
		keys = append(keys, key)
		vals = append(vals, val)
	}
	lqP50, lqP99 := stats(lqWriteDur)
	fmt.Printf("Записано: %d/%d (ошибок: %d), LOCAL_QUORUM write p50=%s p99=%s\n\n", len(keys), n, lqWriteErrs, lqP50, lqP99)

	// -- Фаза B: чтение ТЕХ ЖЕ ключей LOCAL_QUORUM, координатор ЖЁСТКО в DC2 --
	fmt.Println("-- Фаза B: чтение записанных строк LOCAL_QUORUM с координатором, закреплённым в DC2 --")
	sessionDC2, err := connectDC(dc2Hosts, "DC2", gocql.LocalQuorum)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect DC2:", err)
		return false
	}
	defer sessionDC2.Close()

	var replicated, mismatched, readErrs int
	for i, key := range keys {
		var got float64
		err := sessionDC2.Query(mdcReadCQL, key).Consistency(gocql.LocalQuorum).Scan(&got)
		if err != nil {
			readErrs++
			if readErrs <= 3 {
				fmt.Fprintf(os.Stderr, "  read #%d (%s): %v\n", i, key, err)
			}
			continue
		}
		if got == vals[i] {
			replicated++
		} else {
			mismatched++
		}
	}
	fmt.Printf("Прочитано из DC2 (LOCAL_QUORUM): совпало=%d, расхождение=%d, ошибок чтения=%d (из %d записанных)\n\n",
		replicated, mismatched, readErrs, len(keys))

	// -- Фаза C: LOCAL_QUORUM vs QUORUM латентность (тот же координатор DC1) --
	fmt.Printf("-- Фаза C: %d НОВЫХ записей QUORUM (координатор DC1, но ждёт кворум ВСЕХ 4 реплик кластера -- минимум 3 из 4, значит минимум 1 подтверждение из DC2) --\n", n)
	var qWriteDur []time.Duration
	var qWriteErrs int
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("mdc-%d-q-%06d", runID, i)
		val := float64(i) + float64(seed)/1000.0
		t0 := time.Now()
		err := sessionDC1.Query(mdcWriteCQL, key, val).Consistency(gocql.Quorum).Exec()
		d := time.Since(t0)
		if err != nil {
			qWriteErrs++
			if qWriteErrs <= 3 {
				fmt.Fprintf(os.Stderr, "  write #%d: %v\n", i, err)
			}
			continue
		}
		qWriteDur = append(qWriteDur, d)
	}
	qP50, qP99 := stats(qWriteDur)
	fmt.Printf("Записано: %d/%d (ошибок: %d), QUORUM write p50=%s p99=%s\n\n", len(qWriteDur), n, qWriteErrs, qP50, qP99)

	fmt.Println("-- Таблица: LOCAL_QUORUM (координатор DC1, ждёт только DC1) vs QUORUM (координатор DC1, ждёт кворум ВСЕГО кластера, вкл. DC2) --")
	fmt.Printf("%-14s %12s %12s\n", "CL", "write_p50", "write_p99")
	fmt.Printf("%-14s %10dus %10dus\n", "LOCAL_QUORUM", lqP50.Microseconds(), lqP99.Microseconds())
	fmt.Printf("%-14s %10dus %10dus\n", "QUORUM", qP50.Microseconds(), qP99.Microseconds())
	fmt.Println()

	pass := true
	fmt.Println("-- Ассерты --")
	if replicated == len(keys) && len(keys) > 0 {
		fmt.Printf("OK: dc1_to_dc2_replication — все %d строк, записанных LOCAL_QUORUM в DC1, видны LOCAL_QUORUM-чтением из DC2\n", replicated)
	} else {
		fmt.Printf("FAIL: dc1_to_dc2_replication — реплицировано %d/%d (расхождение=%d, ошибок=%d)\n", replicated, len(keys), mismatched, readErrs)
		pass = false
	}
	if qP50 >= lqP50 {
		fmt.Printf("OK: lat[QUORUM] p50=%s >= lat[LOCAL_QUORUM] p50=%s (QUORUM платит за ожидание удалённого DC2)\n", qP50, lqP50)
	} else {
		fmt.Printf("НАБЛЮДЕНИЕ (не форсируем зелёный): lat[QUORUM] p50=%s < lat[LOCAL_QUORUM] p50=%s -- см. README «Стенд #5»\n", qP50, lqP50)
		fmt.Println("   честная оговорка: оба ДЦ на одном Docker-хосте, cross-DC RTT здесь не настоящий сетевой RTT")
		fmt.Println("   между реальными датацентрами -- разница может быть мала или шумной, не архитектурный вывод.")
	}
	if lqWriteErrs > 0 || qWriteErrs > 0 || readErrs > 0 {
		fmt.Printf("\nFAIL: транспортные ошибки (lq_write=%d, q_write=%d, dc2_read=%d)\n", lqWriteErrs, qWriteErrs, readErrs)
		pass = false
	}
	return pass
}

// ---------------------------------------------------------------------------
// -scenario tablets
// ---------------------------------------------------------------------------

const createTabletsKeyspaceCQL = `CREATE KEYSPACE IF NOT EXISTS telemetry_mdc_tablets
  WITH replication = {'class':'NetworkTopologyStrategy','DC1':1,'DC2':1}`
const createTabletsTableCQL = `CREATE TABLE IF NOT EXISTS telemetry_mdc_tablets.tbench (id text PRIMARY KEY, val double)`
const tabletsWriteCQL = `INSERT INTO telemetry_mdc_tablets.tbench (id, val) VALUES (?, ?)`

// tabletReplica отражает один элемент list<frozen<tuple<uuid,int>>> колонки
// system.tablets.replicas: (host_id узла-реплики, номер шарда на этом узле).
// gocql (scylladb fork v1.18.3) умеет разворачивать CQL tuple в Go struct с
// полями по порядку -- проверено живьём на этой сборке (см. README).
type tabletReplica struct {
	HostID gocql.UUID
	Shard  int
}

func tabletsScenario(hosts []string, rows int, seed int64) bool {
	fmt.Println("=== Стенд #5: tablets (распределение по узлам через system.tablets) ===")
	fmt.Println()

	dc1Hosts, dc2Hosts, err := splitHostsByDC(hosts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "split hosts by DC:", err)
		return false
	}

	// Любая сессия делает DDL/DML -- координатор не важен для этого сценария
	// (в отличие от multidc, здесь не измеряется CL-латентность по ДЦ).
	session, err := connectDC(dc1Hosts, "DC1", gocql.Quorum)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		return false
	}
	defer session.Close()

	fmt.Println("-- Схема: telemetry_mdc_tablets (NetworkTopologyStrategy, DC1:1, DC2:1 -- ")
	fmt.Println("   намеренно RF=1/ДЦ, чтобы decommission одного из двух узлов DC2 не нарушал топологию) --")
	if err := session.Query(createTabletsKeyspaceCQL).Exec(); err != nil {
		fmt.Fprintln(os.Stderr, "create keyspace telemetry_mdc_tablets:", err)
		return false
	}
	if err := session.Query(createTabletsTableCQL).Exec(); err != nil {
		fmt.Fprintln(os.Stderr, "create table tbench:", err)
		return false
	}

	fmt.Printf("-- Загрузка %d детерминированных строк (идемпотентно, безопасно перезапускать) --\n", rows)
	r := seed // не нужен math/rand -- id/val полностью детерминированы индексом
	var loadErrs int
	for i := 0; i < rows; i++ {
		id := fmt.Sprintf("tablet-%06d", i)
		val := float64(i%1000) + float64(r%97)/100.0
		if err := session.Query(tabletsWriteCQL, id, val).Exec(); err != nil {
			loadErrs++
			if loadErrs <= 3 {
				fmt.Fprintf(os.Stderr, "  insert #%d: %v\n", i, err)
			}
		}
	}
	fmt.Printf("Загружено: %d/%d (ошибок: %d)\n\n", rows-loadErrs, rows, loadErrs)
	if loadErrs > 0 {
		fmt.Fprintln(os.Stderr, "FAIL: ошибки загрузки, дальнейшее измерение распределения ненадёжно")
		return false
	}

	// -- host_id каждого из 4 узлов, спрошенный НАПРЯМУЮ у каждого узла --
	allHosts := append(append([]string{}, dc1Hosts...), dc2Hosts...)
	nodeIDs := map[string]gocql.UUID{}
	for _, h := range allHosts {
		s, err := connectSingleHost(h)
		if err != nil {
			fmt.Fprintf(os.Stderr, "connect single host %s: %v\n", h, err)
			return false
		}
		var id gocql.UUID
		err = s.Query("SELECT host_id FROM system.local").Scan(&id)
		s.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "host_id %s: %v\n", h, err)
			return false
		}
		name := h
		if idx := strings.Index(h, ":"); idx >= 0 {
			name = h[:idx]
		}
		nodeIDs[name] = id
	}
	fmt.Println("-- host_id узлов (спрошено напрямую у каждого, system.local) --")
	var nodeNames []string
	for name := range nodeIDs {
		nodeNames = append(nodeNames, name)
	}
	sort.Strings(nodeNames)
	for _, name := range nodeNames {
		fmt.Printf("   %-6s host_id=%s\n", name, nodeIDs[name])
	}
	fmt.Println()

	// -- table_id таблицы tbench в system.tablets --
	var tableID gocql.UUID
	var tabletCount int
	err = session.Query(
		`SELECT table_id, tablet_count FROM system.tablets WHERE keyspace_name=? AND table_name=? LIMIT 1 ALLOW FILTERING`,
		"telemetry_mdc_tablets", "tbench",
	).Scan(&tableID, &tabletCount)
	if err != nil {
		fmt.Fprintln(os.Stderr, "table_id lookup:", err)
		return false
	}
	fmt.Printf("table_id=%s tablet_count(объявленный, static)=%d\n\n", tableID, tabletCount)

	// -- Реальное распределение: по всем tablet-строкам (last_token) этой --
	// -- таблицы суммируем replicas-вхождения на узел. --
	iter := session.Query(`SELECT last_token, replicas FROM system.tablets WHERE table_id=?`, tableID).Iter()
	perNode := map[string]int{}
	var lastToken int64
	var replicas []tabletReplica
	tabletRows := 0
	for iter.Scan(&lastToken, &replicas) {
		tabletRows++
		for _, rep := range replicas {
			node := "?unknown-host_id"
			for name, id := range nodeIDs {
				if id == rep.HostID {
					node = name
					break
				}
			}
			perNode[node]++
		}
		replicas = nil
	}
	if err := iter.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "iterate system.tablets:", err)
		return false
	}

	fmt.Printf("-- Распределение реплик tablets по узлам (строк-tablets в system.tablets: %d) --\n", tabletRows)
	var allNodeNames []string
	for name := range nodeIDs {
		allNodeNames = append(allNodeNames, name)
	}
	sort.Strings(allNodeNames)
	total := 0
	for _, name := range allNodeNames {
		cnt := perNode[name]
		total += cnt
		fmt.Printf("   %-6s: %d tablet-реплик\n", name, cnt)
	}
	if unk := perNode["?unknown-host_id"]; unk > 0 {
		fmt.Printf("   ВНИМАНИЕ: %d реплик с host_id, не совпавшим ни с одним из известных 4 узлов\n", unk)
	}
	fmt.Printf("   ИТОГО: %d (ожидание: tablet-строк %d x RF/tablet 2 (DC1:1+DC2:1) = %d)\n\n", total, tabletRows, tabletRows*2)

	pass := true
	fmt.Println("-- Ассерт --")
	if tabletRows > 0 && total == tabletRows*2 {
		fmt.Println("OK: сумма реплик по узлам точно равна tablet-строк x RF(=2) -- распределение прочитано без потерь/дублей")
	} else {
		fmt.Printf("FAIL: сумма реплик (%d) != tablet-строк x RF (%d) -- см. вывод выше\n", total, tabletRows*2)
		pass = false
	}
	return pass
}
