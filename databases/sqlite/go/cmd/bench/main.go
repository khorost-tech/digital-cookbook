// Бенчмарк встраиваемой и серверной БД на одинаковой нагрузке.
// Плечи: sqlite (файл в процессе), pg-uds (сервер без сети), pg-tcp (сервер по сети).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"

	"khorost.tech/cookbook/sqlite/internal/bench"
)

const schemaPath = "/sql/schema.sql"

func main() {
	arm := flag.String("arm", "sqlite", "плечо: sqlite|pg-uds|pg-tcp")
	op := flag.String("op", "read", "операция: read|insert")
	n := flag.Int("n", 20000, "число операций")
	seed := flag.Int("seed", 200000, "минимальное число строк в events перед замером")
	sync := flag.String("sync", "FULL", "SQLite synchronous: FULL|NORMAL")
	pgSync := flag.String("pg-sync", "on", "synchronous_commit: on|off")
	writers := flag.Int("writers", 1, "число конкурентных писателей (сценарий border, только для op=insert)")
	busyTimeout := flag.Int("busy-timeout", 5000, "SQLite _busy_timeout в мс; 0 — контрольная проверка, что флаг вообще доходит до драйвера")
	flag.Parse()

	if *writers < 1 {
		fmt.Fprintln(os.Stderr, "-writers должно быть не меньше 1")
		os.Exit(1)
	}
	if *writers > 1 && bench.Op(*op) != bench.OpInsert {
		fmt.Fprintln(os.Stderr, "-writers > 1 поддерживается только для -op insert")
		os.Exit(1)
	}

	// Каждый writer — ОТДЕЛЬНОЕ соединение (для sqlite — отдельная физическая
	// connection к тому же файлу), а не общий пул: иначе при -writers>1 их
	// сериализовал бы сам Go до того, как дело дойдёт до блокировки на
	// стороне SQLite, и BusyErrors никогда бы не появился.
	dbs, durability, err := openWriters(*arm, *sync, *pgSync, *busyTimeout, *writers)
	if err != nil {
		fmt.Fprintln(os.Stderr, "открытие БД:", err)
		os.Exit(1)
	}
	defer func() {
		for _, d := range dbs {
			_ = d.Close()
		}
	}()
	db := dbs[0]

	fmt.Fprintf(os.Stderr, "версия движка: %s (%s)\n", engineVersion(db, *arm), durability)

	ctx := context.Background()

	if err := applySchema(ctx, db); err != nil {
		fmt.Fprintln(os.Stderr, "схема:", err)
		os.Exit(1)
	}

	maxID, err := ensureSeed(ctx, db, *arm, *seed)
	if err != nil {
		fmt.Fprintln(os.Stderr, "засев:", err)
		os.Exit(1)
	}

	// Прогрев обязателен и не входит в замер: первый прогон после старта
	// процесса/контейнера меряет холодный кэш страниц (page cache и/или
	// внутренний буферный пул), а не саму БД. Результат отбрасывается.
	warmupN := *n / 10
	if warmupN > 0 {
		if _, err := bench.Run(ctx, db, bench.Op(*op), warmupN, maxID, *arm); err != nil {
			fmt.Fprintln(os.Stderr, "прогрев:", err)
			os.Exit(1)
		}
	}

	// Для insert-прогона maxID уже мог вырасти за время прогрева — пересчитываем,
	// иначе read-операции в этом же процессе били бы по устаревшему диапазону
	// (для op=insert maxID здесь не используется вовсе).
	if bench.Op(*op) == bench.OpInsert {
		maxID, err = currentMaxID(ctx, db)
		if err != nil {
			fmt.Fprintln(os.Stderr, "maxID после прогрева:", err)
			os.Exit(1)
		}
	}

	var res bench.Result
	if *writers > 1 {
		res, err = bench.RunConcurrent(ctx, dbs, bench.Op(*op), *n, maxID, *arm)
	} else {
		res, err = bench.Run(ctx, db, bench.Op(*op), *n, maxID, *arm)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "прогон:", err)
		os.Exit(1)
	}
	res.Durability = durability

	out, err := json.Marshal(res)
	if err != nil {
		fmt.Fprintln(os.Stderr, "сериализация результата:", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}

// applySchema прогоняет sql/schema.sql целиком. Файл общий для всех плеч,
// исполняется по одному statement за раз — так надёжнее, чем multi-statement
// Exec, поведение которого для разных драйверов database/sql не гарантировано.
func applySchema(ctx context.Context, db *sql.DB) error {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		return err
	}
	for _, stmt := range strings.Split(string(raw), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

// ensureSeed досевает events до seed строк, если их меньше, и возвращает
// текущий max(id). Досев, а не безусловная вставка: скрипт запускает main
// заново на каждую строку матрицы (arm x op x durability), и без проверки
// счётчика каждый запуск досыпал бы ещё seed строк поверх уже насеянных.
func ensureSeed(ctx context.Context, db *sql.DB, arm string, seed int) (int, error) {
	maxID, err := currentMaxIDAllowEmpty(ctx, db)
	if err != nil {
		return 0, err
	}
	// id передаём явно (та же причина, что и в bench.insertQuery): SQLite
	// автоинкрементит rowid сам, PostgreSQL — нет, а схема у обеих одна.
	stmt := "INSERT INTO events (id, user_id, amount, payload) VALUES (?, ?, ?, ?)"
	if arm != "sqlite" {
		stmt = bench.RewritePlaceholders(stmt)
	}
	for maxID < seed {
		batch := 500
		if remain := seed - maxID; remain < batch {
			batch = remain
		}
		if err := insertBatch(ctx, db, stmt, maxID, batch); err != nil {
			return 0, err
		}
		maxID += batch
	}
	return currentMaxID(ctx, db)
}

func insertBatch(ctx context.Context, db *sql.DB, stmt string, base, n int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for i := 0; i < n; i++ {
		if _, err := tx.ExecContext(ctx, stmt, base+1+i,
			rand.Intn(1000), rand.Intn(10000), "payload-fixed-length-string"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func currentMaxID(ctx context.Context, db *sql.DB) (int, error) {
	maxID, err := currentMaxIDAllowEmpty(ctx, db)
	if err != nil {
		return 0, err
	}
	if maxID < 1 {
		return 0, fmt.Errorf("таблица events пуста после засева")
	}
	return maxID, nil
}

func currentMaxIDAllowEmpty(ctx context.Context, db *sql.DB) (int, error) {
	var maxID int
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM events").Scan(&maxID); err != nil {
		return 0, err
	}
	return maxID, nil
}

// openWriters открывает ровно writers независимых соединений с БД. Для sqlite
// это writers отдельных физических connection к одному файлу — так конкуренция
// писателей происходит на стороне движка, а не в пуле database/sql. Для
// Postgres по той же причине не переиспользуем один *sql.DB: writers=N должно
// значить одно и то же число независимых клиентов для обоих плеч.
func openWriters(arm, sync, pgSync string, busyTimeout, writers int) ([]*sql.DB, string, error) {
	dbs := make([]*sql.DB, 0, writers)
	var durability string
	for i := 0; i < writers; i++ {
		db, dur, err := open(arm, sync, pgSync, busyTimeout)
		if err != nil {
			for _, opened := range dbs {
				_ = opened.Close()
			}
			return nil, "", err
		}
		durability = dur
		dbs = append(dbs, db)
	}
	return dbs, durability, nil
}

func open(arm, sync, pgSync string, busyTimeout int) (*sql.DB, string, error) {
	switch arm {
	case "sqlite":
		// _journal_mode=WAL обязателен: без него SQLite блокирует читателей на запись,
		// и замер измерял бы устаревший режим, а не тот, о котором статья.
		// _busy_timeout настраивается флагом -busy-timeout (не через env): контрольная
		// проверка границы одного писателя (0 мс) гоняется именно им, чтобы значение
		// гарантированно доходило до драйвера, а не терялось по пути через окружение.
		dsn := fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=%s&_busy_timeout=%d",
			os.Getenv("SQLITE_PATH"), sync, busyTimeout)
		db, err := sql.Open("sqlite3", dsn)
		if db != nil {
			// database/sql пула не знает, что за файлом стоит один писатель:
			// он вправе открыть вторую физическую connection даже при
			// строго последовательных вызовах (например, впрок), а SQLite
			// на запись — один потребитель одновременно. Итог без этого —
			// периодический "database is locked" на insert, не связанный
			// ни с нагрузкой, ни с _busy_timeout (лочится ДО его отсчёта,
			// потому что locked-соединение вообще не то, что держит WAL).
			db.SetMaxOpenConns(1)
		}
		return db, "sqlite synchronous=" + sync, err
	case "pg-uds", "pg-tcp":
		env := "PG_UDS_DSN"
		if arm == "pg-tcp" {
			env = "PG_TCP_DSN"
		}
		// Значение валидируем до подстановки: оно уходит в параметры соединения,
		// и произвольная строка там меняла бы не только synchronous_commit.
		if pgSync != "on" && pgSync != "off" {
			return nil, "", fmt.Errorf("pg-sync должен быть on или off, получено %q", pgSync)
		}
		dsn := os.Getenv(env)
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "options=" + url.QueryEscape("-c synchronous_commit="+pgSync)
		db, err := sql.Open("pgx", dsn)
		return db, "postgres synchronous_commit=" + pgSync, err
	default:
		return nil, "", fmt.Errorf("неизвестное плечо %q", arm)
	}
}

func engineVersion(db *sql.DB, arm string) string {
	q := "SELECT sqlite_version()"
	if arm != "sqlite" {
		q = "SHOW server_version"
	}
	var v string
	if err := db.QueryRow(q).Scan(&v); err != nil {
		return "неизвестна: " + err.Error()
	}
	return v
}
