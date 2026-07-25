// Command drivers — стенд #3 серии "ClickHouse: глубокое погружение":
// бенчмарк драйверов. Единый сценарий (батч-INSERT M строк общего датасета +
// один аналитический SELECT) прогоняется через 4 Go-пути:
//
//   - ch-go        — низкоуровневый колоночный, нативный протокол, ручное
//     заполнение proto.Col* (chgo.go)
//   - native        — clickhouse-go/v2 batch API, PrepareBatch/Append/Send
//     (nativedrv.go)
//   - dbsql         — clickhouse-go/v2 через database/sql, sql.DB + Exec в
//     транзакции (dbsql.go)
//   - http          — сырой net/http POST на HTTP-интерфейс ClickHouse, без
//     специализированного клиента (httpdriver.go)
//
// Java-зеркало (../java, client-v2 native + clickhouse-jdbc) добавляет ещё
// 2 драйвера/таблицы. Финальная фаза -phase=verify сверяет аналитический
// результат по ВСЕМ 6 таблицам (4 Go + 2 Java) через одно административное
// clickhouse-go/v2 native-соединение — это и есть авторитетная проверка
// "SELECT дал одинаковый результат у всех" межъязыковая/междрайверная.
//
// Запуск (из контейнера golang:1.25, сеть clickhouse-cookbook-net, см.
// ../../ops/drivers-bench.sh за полный live-сценарий):
//
//	docker run --rm --network clickhouse-cookbook-net \
//	  -v "$(pwd)/drivers/go:/app" -v "$(pwd)/dataset/out:/data" -w /app golang:1.25 \
//	  go run . -phase=all -csv=/data/events-drivers.csv -expect-rows=1000000 \
//	  -ch-addr=clickhouse:9000
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

type driverResult struct {
	name       string
	insertRows uint64
	insertDur  time.Duration
	agg        aggResult
	selectDur  time.Duration
}

func main() {
	phase := flag.String("phase", "all", "chgo|native|dbsql|http|all|verify")
	chAddr := flag.String("ch-addr", "clickhouse:9000", "ClickHouse native addr (ch-go, clickhouse-go native, admin-соединение)")
	httpAddr := flag.String("http-addr", "http://clickhouse:8123", "ClickHouse HTTP base URL (raw HTTP-драйвер)")
	dsn := flag.String("dsn", "clickhouse://default:@clickhouse:9000/demo", "DSN для clickhouse-go database/sql драйвера")
	csvPath := flag.String("csv", "/data/events-drivers.csv", "путь к CSV-датасету (dataset/main.go), одному на все 6 драйверов")
	batchSize := flag.Int("batch-size", 100_000, "размер батча вставки (все Go-драйверы, apples-to-apples)")
	expectRows := flag.Uint64("expect-rows", 1_000_000, "M — ожидаемое число строк на драйвер")
	verifyTables := flag.String("tables", "drivers_chgo,drivers_native,drivers_dbsql,drivers_http,drivers_java_client,drivers_java_jdbc", "-phase=verify: список таблиц (все 6 драйверов) для межъязыковой сверки")
	flag.Parse()

	ctx := context.Background()

	admin, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{*chAddr},
		Auth:        clickhouse.Auth{Database: "demo", Username: "default"},
		DialTimeout: 10 * time.Second,
		Settings:    clickhouse.Settings{"max_execution_time": 300},
	})
	if err != nil {
		log.Fatalf("admin open: %v", err)
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		log.Fatalf("admin ping: %v", err)
	}

	if *phase == "verify" {
		runVerify(ctx, admin, strings.Split(*verifyTables, ","), *expectRows)
		return
	}

	run := map[string]bool{}
	switch *phase {
	case "all":
		run["chgo"], run["native"], run["dbsql"], run["http"] = true, true, true, true
	case "chgo", "native", "dbsql", "http":
		run[*phase] = true
	default:
		fmt.Fprintf(os.Stderr, "unknown -phase=%s\n", *phase)
		os.Exit(2)
	}

	var results []driverResult
	var ref *aggResult
	var refName string

	execDriver := func(displayName, table string, insertFn func() (uint64, time.Duration, error), selectFn func() (aggResult, time.Duration, error)) {
		mustRun(func() error { return createTableAdmin(ctx, admin, table) })

		rows, idur, err := insertFn()
		if err != nil {
			log.Fatalf("%s insert: %v", displayName, err)
		}
		assertFailFast(rows == *expectRows, "%s: inserted rows (%d) == M (%d)", displayName, rows, *expectRows)

		actualRows, err := tableRowsAdmin(ctx, admin, table)
		if err != nil {
			log.Fatalf("%s tableRows: %v", displayName, err)
		}
		assertFailFast(actualRows == *expectRows, "%s: system.parts rows (%d) == M (%d)", displayName, actualRows, *expectRows)
		fmt.Printf("[insert] %-38s %d rows in %s (%.0f rows/s)\n", displayName, rows, idur, float64(rows)/idur.Seconds())

		agg, sdur, err := selectFn()
		if err != nil {
			log.Fatalf("%s select: %v", displayName, err)
		}
		fmt.Printf("[select] %-38s %s, groups=%d, totalRows=%d, totalCents=%d, checksum=%08x\n",
			displayName, sdur, agg.groups, agg.totalRows, agg.totalCents, agg.checksum)

		if ref == nil {
			r := agg
			ref = &r
			refName = displayName
		} else {
			assertAggEqual(fmt.Sprintf("%s vs %s", displayName, refName), agg, *ref, *expectRows)
		}
		results = append(results, driverResult{name: displayName, insertRows: rows, insertDur: idur, agg: agg, selectDur: sdur})
	}

	if run["chgo"] {
		fmt.Println("\n=== ch-go (низкоуровневый колоночный, ручные proto.Col*) ===")
		execDriver("ch-go", "drivers_chgo",
			func() (uint64, time.Duration, error) { return chGoInsert(ctx, *chAddr, "drivers_chgo", *csvPath, *batchSize) },
			func() (aggResult, time.Duration, error) { return chGoSelect(ctx, *chAddr, "drivers_chgo") },
		)
	}
	if run["native"] {
		fmt.Println("\n=== clickhouse-go native (PrepareBatch/Append/Send) ===")
		execDriver("clickhouse-go native", "drivers_native",
			func() (uint64, time.Duration, error) { return nativeInsert(ctx, admin, "drivers_native", *csvPath, *batchSize) },
			func() (aggResult, time.Duration, error) { return nativeSelect(ctx, admin, "drivers_native") },
		)
	}
	if run["dbsql"] {
		fmt.Println("\n=== clickhouse-go database/sql (sql.DB + Exec в транзакции) ===")
		execDriver("clickhouse-go database/sql", "drivers_dbsql",
			func() (uint64, time.Duration, error) { return dbsqlInsert(ctx, *dsn, "drivers_dbsql", *csvPath, *batchSize) },
			func() (aggResult, time.Duration, error) { return dbsqlSelect(ctx, *dsn, "drivers_dbsql") },
		)
	}
	if run["http"] {
		fmt.Println("\n=== raw HTTP (net/http POST, без ClickHouse-клиента) ===")
		execDriver("raw HTTP (net/http)", "drivers_http",
			func() (uint64, time.Duration, error) { return httpInsert(ctx, *httpAddr, "drivers_http", *csvPath, *expectRows) },
			func() (aggResult, time.Duration, error) { return httpSelect(ctx, *httpAddr, "drivers_http") },
		)
	}

	if len(results) > 1 {
		fmt.Println("\n=== СВОДКА (Go-драйверы этого прогона; полная сводка 6 драйверов — README/отчёт) ===")
		for _, r := range results {
			fmt.Printf("%-38s insert=%.0f rows/s (%s)  select=%s\n", r.name, float64(r.insertRows)/r.insertDur.Seconds(), r.insertDur, r.selectDur)
		}
	}

	fmt.Printf("\n[drivers] phase=%s завершена, все ассерты прошли\n", *phase)
}

// runVerify — авторитетная межъязыковая/междрайверная сверка (Step 4
// брифа): один и тот же аналитический SELECT (aggregate.go) выполняется
// через ЕДИНОЕ административное соединение (admin) над КАЖДОЙ из 6 таблиц
// драйверов (заполненных СВОИМИ драйверами в предыдущих фазах — Go this
// binary, Java see ../java) — не для замера latency (это уже сделано в
// -phase=chgo|native|dbsql|http и в Java-стенде), а чтобы исключить любую
// связь между "каким клиентом читали" и "что вернулось": если бы через
// admin-соединение результаты разошлись, это значило бы, что данные на
// диске РЕАЛЬНО разные — а не что разные клиенты по-разному декодируют один
// и тот же результат.
func runVerify(ctx context.Context, admin clickhouse.Conn, tables []string, expectRows uint64) {
	fmt.Println("=== VERIFY: межъязыковая/междрайверная сверка (все 6 таблиц драйверов) ===")

	var ref aggResult
	var refTable string
	for i, t := range tables {
		t = strings.TrimSpace(t)
		rows, err := tableRowsAdmin(ctx, admin, t)
		if err != nil {
			log.Fatalf("verify %s: tableRows: %v", t, err)
		}
		assertFailFast(rows == expectRows, "verify: %s system.parts rows (%d) == M (%d)", t, rows, expectRows)

		agg, dur, err := nativeSelect(ctx, admin, t)
		if err != nil {
			log.Fatalf("verify %s: select: %v", t, err)
		}
		fmt.Printf("[verify] %-24s %s groups=%d totalRows=%d totalCents=%d checksum=%08x\n",
			t, dur, agg.groups, agg.totalRows, agg.totalCents, agg.checksum)

		if i == 0 {
			ref = agg
			refTable = t
		} else {
			assertAggEqual(fmt.Sprintf("%s vs %s", t, refTable), agg, ref, expectRows)
		}
	}

	fmt.Printf("\n[verify] все %d таблиц драйверов дали ИДЕНТИЧНЫЙ аналитический результат (checksum=%08x) — межъязыковая/междрайверная согласованность подтверждена\n",
		len(tables), ref.checksum)
}

func mustRun(f func() error) {
	if err := f(); err != nil {
		log.Fatalf("%v", err)
	}
}
