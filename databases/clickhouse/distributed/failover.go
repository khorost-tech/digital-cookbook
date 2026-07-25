package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// clusterState — снимок распределения строк по шардам ПЕРЕД тем, как
// ops/distributed-demo.sh начинает docker stop/start отдельных нод.
// Персистируется в JSON (флаг -state), т.к. последующие фазы failover-
// сценария (Step 4 брифа) запускаются КАЖДАЯ отдельным `docker run go run .`
// процессом (между ними bash делает docker stop/start — оркестрация вне
// Go-процесса, он не может management-командами трогать соседние
// контейнеры) и не разделяют память с фазой, где baseline был измерен.
type clusterState struct {
	Total  uint64 `json:"total"`
	Shard1 uint64 `json:"shard1"`
	Shard2 uint64 `json:"shard2"`
}

func saveState(path string, sc shardCounts) error {
	st := clusterState{Total: sc.total(), Shard1: sc.shard1, Shard2: sc.shard2}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func loadState(path string) (clusterState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return clusterState{}, err
	}
	var st clusterState
	if err := json.Unmarshal(b, &st); err != nil {
		return clusterState{}, err
	}
	return st, nil
}

// phaseBaseline — снимок состояния ПОСЛЕ insert-dist + replication (marker-
// строки уже включены в shard1) — это "здоровый" baseline, с которым
// сверяются все дальнейшие failover-фазы.
func phaseBaseline(ctx context.Context, coord clickhouse.Conn, statePath string) {
	fmt.Println("\n=== BASELINE ПЕРЕД FAILOVER-СЦЕНАРИЕМ (Step 4 брифа, подготовка) ===")
	sc := queryShardCounts(ctx, coord)
	fmt.Printf("[baseline] shard1=%d shard2=%d total=%d -> %s\n", sc.shard1, sc.shard2, sc.total(), statePath)
	assertFailFast(sc.shard1 > 0 && sc.shard2 > 0, "baseline: оба шарда непусты (shard1=%d, shard2=%d)", sc.shard1, sc.shard2)
	if err := saveState(statePath, sc); err != nil {
		log.Fatalf("save state %s: %v", statePath, err)
	}
}

// queryDistributedTotal — count() через Distributed с произвольными
// per-query SETTINGS (skip_unavailable_shards, connect_timeout_with_failover_ms
// и т.д. — передаются вызывающим кодом через ctx).
func queryDistributedTotal(ctx context.Context, coord clickhouse.Conn) (uint64, error) {
	var n uint64
	err := coord.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM demo.%s", tblDistributed)).Scan(&n)
	return n, err
}

// phaseAfterReplicaDown — Step 4 брифа, сценарий А: ОДНА реплика шарда 1
// (ch-s1-r2) остановлена docker stop СНАРУЖИ (ops/distributed-demo.sh) ДО
// вызова этой фазы. Coord (ch-s1-r1) не трогали — шарду 1 есть кому
// ответить. Ожидание: Distributed прозрачно использует живую реплику,
// ПОЛНЫЙ результат, без специальных настроек.
func phaseAfterReplicaDown(ctx context.Context, coord clickhouse.Conn, statePath string) {
	fmt.Println("\n=== СЦЕНАРИЙ А: ОДНА РЕПЛИКА ШАРДА НЕДОСТУПНА (Step 4 брифа) ===")
	st, err := loadState(statePath)
	if err != nil {
		log.Fatalf("load state %s: %v", statePath, err)
	}

	total, err := queryDistributedTotal(ctx, coord)
	if err != nil {
		log.Fatalf("query total with ch-s1-r2 down: %v (ожидался УСПЕШНЫЙ запрос — другая реплика шарда 1 жива)", err)
	}
	fmt.Printf("[replica-down] ch-s1-r2 остановлена (docker stop), ch-s1-r1 (та же реплика шарда 1) жива: SELECT count() FROM demo.%s = %d (baseline=%d)\n", tblDistributed, total, st.Total)
	assertFailFast(total == st.Total, "«реплика недоступна» != «шард недоступен»: результат ПОЛНЫЙ, без потерь, без skip_unavailable_shards: %d == %d", total, st.Total)
}

// phaseAfterShardDown — Step 4 брифа, сценарий Б: ОБЕ реплики шарда 2
// (ch-s2-r1, ch-s2-r2) остановлены. Coord (ch-s1-r1) не трогали. Показывает
// ДВЕ разные настройки:
//  1. default (skip_unavailable_shards=0) — Distributed ОБЯЗАН получить
//     ответ от каждого шарда, шард 2 недостижим целиком -> ошибка.
//  2. skip_unavailable_shards=1 — частичный результат (только шард 1).
func phaseAfterShardDown(ctx context.Context, coord clickhouse.Conn, statePath string) {
	fmt.Println("\n=== СЦЕНАРИЙ Б: ВЕСЬ ШАРД НЕДОСТУПЕН (Step 4 брифа) ===")
	st, err := loadState(statePath)
	if err != nil {
		log.Fatalf("load state %s: %v", statePath, err)
	}

	boundedCtx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"connect_timeout_with_failover_ms":    500,
		"connections_with_failover_max_tries": 1,
		"max_execution_time":                  30,
	}))
	_, errDefault := queryDistributedTotal(boundedCtx, coord)
	if errDefault == nil {
		log.Fatalf("[shard-down] ОШИБКА СЦЕНАРИЯ: запрос без skip_unavailable_shards УСПЕШНО прошёл, хотя шард 2 (ch-s2-r1 + ch-s2-r2) полностью остановлен — ожидалась ошибка")
	}
	fmt.Printf("[shard-down] default (skip_unavailable_shards=0), ОБЕ реплики шарда 2 остановлены: запрос упал с ошибкой (ожидаемо):\n  %v\n", errDefault)
	assertFailFast(errDefault != nil, "«шард недоступен» без skip_unavailable_shards — Distributed ОБЯЗАН получить ответ от каждого шарда -> ошибка (получена)")

	skipCtx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"skip_unavailable_shards":             1,
		"connect_timeout_with_failover_ms":    500,
		"connections_with_failover_max_tries": 1,
		"max_execution_time":                  30,
	}))
	partial, errSkip := queryDistributedTotal(skipCtx, coord)
	if errSkip != nil {
		log.Fatalf("query total with skip_unavailable_shards=1: %v", errSkip)
	}
	fmt.Printf("[shard-down] skip_unavailable_shards=1: SELECT count() FROM demo.%s = %d (только шард 1; baseline shard1=%d, полный baseline total=%d)\n", tblDistributed, partial, st.Shard1, st.Total)
	assertFailFast(partial == st.Shard1, "skip_unavailable_shards=1 вернул РОВНО данные живого шарда (частичный, но НЕ ошибка): %d == baseline.shard1=%d", partial, st.Shard1)
	assertFailFast(partial < st.Total, "частичный результат меньше полного baseline: %d < %d", partial, st.Total)
}

// phaseAfterRestartVerify — Step 4 брифа, финал: обе ноды возвращены
// (docker start, ops/distributed-demo.sh), кластер снова полностью здоров —
// данные НЕ потеряны (именованные volume'ы пережили stop/start), полный
// результат восстановлен без вмешательства (никакого re-insert).
func phaseAfterRestartVerify(ctx context.Context, coord clickhouse.Conn, statePath string) {
	fmt.Println("\n=== ВОССТАНОВЛЕНИЕ ПОСЛЕ FAILOVER-СЦЕНАРИЯ (Step 4 брифа) ===")
	st, err := loadState(statePath)
	if err != nil {
		log.Fatalf("load state %s: %v", statePath, err)
	}

	sc := queryShardCounts(ctx, coord)
	fmt.Printf("[restart-verify] все 4 ноды снова healthy: shard1=%d shard2=%d total=%d (baseline: shard1=%d shard2=%d total=%d)\n",
		sc.shard1, sc.shard2, sc.total(), st.Shard1, st.Shard2, st.Total)
	assertFailFast(sc.total() == st.Total, "после docker start обеих нод данные полностью восстановлены (ничего не потеряно, volume пережил stop): %d == %d", sc.total(), st.Total)
	assertFailFast(sc.shard1 == st.Shard1 && sc.shard2 == st.Shard2, "распределение по шардам не изменилось: shard1 %d==%d, shard2 %d==%d", sc.shard1, st.Shard1, sc.shard2, st.Shard2)
}
