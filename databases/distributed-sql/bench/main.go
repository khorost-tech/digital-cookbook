// bench — один инструмент замеров для всех движков стенда distributed-sql.
//
// Запускается в контейнере в сети dsql (см. scripts/lib.sh, функция bench),
// поэтому адреса узлов — имена контейнеров. Движок выбирается флагом -engine,
// драйвер — по движку: pgx для PG-wire, go-sql-driver/mysql для MySQL-wire.
//
//	bench latency  -engine crdb -n 300
//	bench skew     -engine tidb -iso default
//	bench compat   -engine yb
//	bench failover -engine crdb -key 2000001 -duration 40s
package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Адреса по умолчанию — изнутри сети dsql.
var dsns = map[string]string{
	"crdb":  "postgres://root@crdb1:26257/defaultdb?sslmode=disable",
	"yb":    "postgres://yugabyte@yb1:5433/yugabyte?sslmode=disable",
	"pg":    "postgres://postgres:bench@pg:5432/bench?sslmode=disable",
	"tidb":  "root@tcp(tidb:4000)/test",
	"ob":    "root@test@tcp(ob:2881)/test",
	"mysql": "root:bench@tcp(mysql:3306)/bench",
}

// pgWire — движок говорит на протоколе PostgreSQL.
func pgWire(engine string) bool {
	return engine == "crdb" || engine == "yb" || engine == "pg"
}

func open(engine, dsn string) *sql.DB {
	if dsn == "" {
		dsn = dsns[engine]
	}
	if dsn == "" {
		die("неизвестный движок %q", engine)
	}
	driver := "mysql"
	if pgWire(engine) {
		driver = "pgx"
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		die("open: %v", err)
	}
	db.SetMaxOpenConns(8)
	return db
}

// ph возвращает плейсхолдер n-го параметра в диалекте движка.
func ph(engine string, n int) string {
	if pgWire(engine) {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

// retryable — ошибка, после которой транзакцию надо повторить целиком.
// PG-wire: 40001 (serialization failure), 40P01 (deadlock).
// MySQL-wire: 1213 (deadlock), 9007 (TiDB write conflict), 8022 (TiDB
// «transaction aborted, retry»). Коды OceanBase сюда не внесены: на стенде он
// ни разу не вернул ретраибельной ошибки, а без проверки список не пополняем.
func retryable(err error) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code == "40001" || pe.Code == "40P01"
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1213, 9007, 8022:
			return true
		}
	}
	return false
}

// errLine — первая строка ошибки с кодом, как её видит приложение.
func errLine(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return fmt.Sprintf("SQLSTATE %s: %s", pe.Code, firstLine(pe.Message))
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return fmt.Sprintf("ERROR %d: %s", me.Number, firstLine(me.Message))
	}
	return firstLine(err.Error())
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

func must(err error, what string) {
	if err != nil {
		die("%s: %s", what, errLine(err))
	}
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "bench: "+f+"\n", a...)
	os.Exit(1)
}

// pct — перцентиль p (0..100) отсортированной выборки, в миллисекундах.
func pct(s []time.Duration, p float64) float64 {
	if len(s) == 0 {
		return 0
	}
	i := int(float64(len(s)-1) * p / 100)
	return float64(s[i].Microseconds()) / 1000
}

func summary(name string, s []time.Duration, retries int) string {
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return fmt.Sprintf("%-8s n=%-4d p50=%7.2f p95=%7.2f p99=%7.2f ms  retries=%d",
		name, len(s), pct(s, 50), pct(s, 95), pct(s, 99), retries)
}

func main() {
	if len(os.Args) < 2 {
		die("режим: latency | skew | compat | failover")
	}
	mode := os.Args[1]
	fs := flag.NewFlagSet(mode, flag.ExitOnError)
	engine := fs.String("engine", "", "crdb | yb | tidb | ob | pg | mysql")
	dsn := fs.String("dsn", "", "переопределить адрес по умолчанию")
	n := fs.Int("n", 300, "latency: операций на каждый вид")
	shards := fs.Int("shards", 4, "latency: сколько диапазонов ключей готовить")
	setup := fs.Bool("setup", false, "latency: пересоздать таблицу kv перед замером")
	iso := fs.String("iso", "default", "skew: default | serializable | for-update")
	key := fs.Int64("key", 1, "failover: ключ, в который пишем")
	dur := fs.Duration("duration", 40*time.Second, "failover: длительность прогона")
	attempt := fs.Duration("timeout", 30*time.Second, "failover: таймаут одной попытки")
	_ = fs.Parse(os.Args[2:])
	if *engine == "" {
		die("нужен -engine")
	}
	db := open(*engine, *dsn)
	defer db.Close()

	switch mode {
	case "latency":
		runLatency(db, *engine, *n, *shards, *setup)
	case "skew":
		runSkew(db, *engine, *iso)
	case "compat":
		runCompat(db, *engine)
	case "failover":
		runFailover(db, *engine, *key, *dur, *attempt)
	default:
		die("неизвестный режим %q", mode)
	}
}
