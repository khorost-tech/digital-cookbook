package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

// Ключи разложены по диапазонам: в диапазоне i лежат ключи i*1e6+1 .. i*1e6+rows.
// Границы диапазонов совпадают с точками сплита, поэтому «транзакция в два
// шарда» гарантированно задевает две разные консенсус-группы.
const (
	shardStep = 1_000_000
	rowsPer   = 1000
)

func shardKey(shard int) int64 {
	return int64(shard)*shardStep + 1 + rand.Int64N(rowsPer)
}

// ddlKV — DDL таблицы kv и предразбиения в диалекте движка. Печатается
// целиком, чтобы в фикстуре было видно, как именно резали таблицу.
func ddlKV(engine string, shards int) []string {
	var points []string
	for i := 1; i < shards; i++ {
		points = append(points, fmt.Sprintf("(%d)", i*shardStep))
	}
	pts := strings.Join(points, ", ")
	switch engine {
	case "crdb":
		return []string{
			"CREATE TABLE kv (k INT8 PRIMARY KEY, v INT8 NOT NULL DEFAULT 0)",
			"ALTER TABLE kv SPLIT AT VALUES " + pts,
			"ALTER TABLE kv SCATTER",
		}
	case "yb":
		// ASC в первичном ключе — range-шардинг; без него YugabyteDB режет по хешу,
		// и «соседние» ключи окажутся в разных таблетках.
		return []string{
			"CREATE TABLE kv (k BIGINT, v BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (k ASC)) SPLIT AT VALUES (" + pts + ")",
		}
	case "tidb":
		return []string{
			"CREATE TABLE kv (k BIGINT PRIMARY KEY CLUSTERED, v BIGINT NOT NULL DEFAULT 0)",
			"SPLIT TABLE kv BY " + pts,
		}
	case "ob":
		var parts []string
		for i := 1; i < shards; i++ {
			parts = append(parts, fmt.Sprintf("PARTITION p%d VALUES LESS THAN (%d)", i-1, i*shardStep))
		}
		parts = append(parts, fmt.Sprintf("PARTITION p%d VALUES LESS THAN MAXVALUE", shards-1))
		return []string{
			"CREATE TABLE kv (k BIGINT PRIMARY KEY, v BIGINT NOT NULL DEFAULT 0) PARTITION BY RANGE (k) (" +
				strings.Join(parts, ", ") + ")",
		}
	default: // pg, mysql — одиночный сервер, резать нечего
		return []string{"CREATE TABLE kv (k BIGINT PRIMARY KEY, v BIGINT NOT NULL DEFAULT 0)"}
	}
}

func setupKV(db *sql.DB, engine string, shards int) {
	_, _ = db.Exec("DROP TABLE IF EXISTS kv")
	for _, s := range ddlKV(engine, shards) {
		fmt.Println("-- " + s)
		_, err := db.Exec(s)
		must(err, s)
	}
	for sh := 0; sh < shards; sh++ {
		var vals []string
		for j := 1; j <= rowsPer; j++ {
			vals = append(vals, fmt.Sprintf("(%d, 0)", int64(sh)*shardStep+int64(j)))
		}
		_, err := db.Exec("INSERT INTO kv (k, v) VALUES " + strings.Join(vals, ", "))
		must(err, "insert kv")
	}
	fmt.Printf("-- загружено %d строк в %d диапазонов\n", shards*rowsPer, shards)
}

// Пределы повторов: без них конфликт, который не рассасывается, крутил бы
// цикл бесконечно, а повтор без паузы снова столкнул бы те же транзакции.
const (
	maxAttempts = 10
	baseBackoff = 5 * time.Millisecond
	maxBackoff  = 500 * time.Millisecond
)

// backoff — экспоненциальная пауза с полным джиттером, прерываемая контекстом.
func backoff(ctx context.Context, attempt int) error {
	d := baseBackoff << attempt
	if d > maxBackoff || d <= 0 {
		d = maxBackoff
	}
	t := time.NewTimer(time.Duration(rand.Int64N(int64(d)) + 1))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// txnRows — явная транзакция, обновляющая по одной строке в каждом из shardsList.
// Ключи выбираются ДО цикла повторов: повтор обязан выполнить ту же самую
// операцию, а не новую. Повторяется по retryable-ошибкам не больше
// maxAttempts раз; возвращает число повторов.
func txnRows(ctx context.Context, db *sql.DB, engine string, shardsList []int) (int, error) {
	upd := "UPDATE kv SET v = v + 1 WHERE k = " + ph(engine, 1)
	keys := make([]int64, len(shardsList))
	for i, sh := range shardsList {
		keys[i] = shardKey(sh)
	}
	for attempt := 0; ; attempt++ {
		err := func() error {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			for _, k := range keys {
				if _, err := tx.ExecContext(ctx, upd, k); err != nil {
					return err
				}
			}
			return tx.Commit()
		}()
		if err == nil || !retryable(err) {
			return attempt, err
		}
		if attempt+1 >= maxAttempts {
			return attempt, fmt.Errorf("сдались после %d попыток: %w", maxAttempts, err)
		}
		if err := backoff(ctx, attempt); err != nil {
			return attempt, err
		}
	}
}

func runLatency(db *sql.DB, engine string, n, shards int, setup bool) {
	if setup {
		setupKV(db, engine, shards)
	}
	if n == 0 {
		return // только подготовка таблицы
	}
	ctx := context.Background()
	// Одно соединение: меряем цену операции, а не очередь за пулом.
	db.SetMaxOpenConns(1)

	type op struct {
		name string
		run  func() (int, error)
	}
	sel := "SELECT v FROM kv WHERE k = " + ph(engine, 1)
	upd := "UPDATE kv SET v = v + 1 WHERE k = " + ph(engine, 1)
	ops := []op{
		{"read", func() (int, error) {
			var v int64
			return 0, db.QueryRowContext(ctx, sel, shardKey(0)).Scan(&v)
		}},
		{"write1", func() (int, error) {
			_, err := db.ExecContext(ctx, upd, shardKey(0))
			return 0, err
		}},
		{"txn1x2", func() (int, error) { return txnRows(ctx, db, engine, []int{0, 0}) }},
		{"txn2", func() (int, error) { return txnRows(ctx, db, engine, []int{0, 1}) }},
	}
	if shards >= 4 {
		// txn1x4 против txn4: одинаковое число операторов, разное число
		// диапазонов. Разводит цену «ещё одного оператора» и «ещё одного шарда».
		ops = append(ops,
			op{"txn1x4", func() (int, error) { return txnRows(ctx, db, engine, []int{0, 0, 0, 0}) }},
			op{"txn4", func() (int, error) { return txnRows(ctx, db, engine, []int{0, 1, 2, 3}) }})
	}

	// Прогрев: соединение, кэши планов, лизы — не в зачёт.
	for i := 0; i < 30; i++ {
		for _, o := range ops {
			if _, err := o.run(); err != nil {
				die("%s (прогрев): %s", o.name, errLine(err))
			}
		}
	}
	samples := make([][]time.Duration, len(ops))
	retries := make([]int, len(ops))
	// Виды операций чередуются, чтобы фоновые колебания легли на все поровну.
	for i := 0; i < n; i++ {
		for j, o := range ops {
			t := time.Now()
			r, err := o.run()
			if err != nil {
				die("%s: %s", o.name, errLine(err))
			}
			samples[j] = append(samples[j], time.Since(t))
			retries[j] += r
		}
	}
	fmt.Printf("engine=%s  n=%d на вид, одно соединение, последовательно\n", engine, n)
	for j, o := range ops {
		fmt.Println(summary(o.name, samples[j], retries[j]))
	}
}
