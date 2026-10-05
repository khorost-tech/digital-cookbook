// Сценарий 4: SQLite и DuckDB — обе встраиваемые (живут в процессе хоста, без
// сети и без сервера), поэтому наивное сравнение "какая быстрее" измеряло бы
// не архитектуру, а один-единственный микробенчмарк. Разница, если она есть,
// должна быть в ОРИЕНТАЦИИ ХРАНЕНИЯ (row-store у SQLite vs column-store у
// DuckDB), а не во встраиваемости — та у обеих одинаковая.
//
// Проверяем это ДВУМЯ запросами на одинаково засеянных данных:
//   - агрегация по всей таблице (SELECT user_id, sum(amount) ... GROUP BY) —
//     профиль, для которого колоночное хранение спроектировано;
//   - точечное чтение по первичному ключу (SELECT payload WHERE id = ?) —
//     профиль, для которого колоночное хранение платит штраф за сборку строки
//     из отдельных колонок, а row-store просто читает соседние байты.
//
// Если бы мы намеряли только агрегацию — вывод был бы "DuckDB быстрее", и
// это была бы однобокая, вводящая в заблуждение картина. Тезис серии
// подтверждается только тогда, когда две БД меняются местами на двух разных
// запросах. Если этого не происходит — тезис прямо опровергается в отчёте,
// а не подгоняется выбором данных или запроса.
//
// На агрегации есть ещё одна переменная, которую обязательно снять со счёта:
// idx_events_user (индекс по user_id, то есть ровно по колонке GROUP BY) не
// покрывающий — в нём нет amount. Если планировщик SQLite выбирает индексный
// проход, каждая строка стоит отдельного обращения к таблице по rowid за
// amount, и часть разрыва с DuckDB объясняется этим планом, а не построчным
// хранением как таковым. Поэтому агрегация на SQLite гоняется ДВАЖДЫ: с
// планом по умолчанию и с NOT INDEXED (принудительный отказ от индекса), а
// оба EXPLAIN QUERY PLAN снимаются и печатаются дословно — до какого-либо
// вывода о причине разницы.
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	duckdb "github.com/marcboeker/go-duckdb/v2"
	_ "github.com/mattn/go-sqlite3"

	"khorost.tech/cookbook/sqlite/internal/bench"
)

const (
	aggregationQuery = "SELECT user_id, sum(amount) FROM events GROUP BY user_id"
	// aggregationQueryNoIndex — тот же запрос с явным запретом индексного
	// прохода (SQLite-специфичный синтаксис NOT INDEXED). idx_events_user
	// покрывает GROUP BY (user_id), но НЕ покрывает amount — если планировщик
	// по умолчанию выбирает индексный проход, каждая строка стоит отдельного
	// обращения к таблице по rowid за amount. Замер с NOT INDEXED отделяет
	// эффект ПЛАНА (использован индекс или нет, около 3.7x на этом стенде) от
	// эффекта ХРАНЕНИЯ (row-store vs column-store, около 153x на full scan
	// против DuckDB) — без него разрыв с DuckDB нельзя было бы приписать
	// одной причине, а не другой.
	aggregationQueryNoIndex = "SELECT user_id, sum(amount) FROM events NOT INDEXED GROUP BY user_id"
	pointQuery              = "SELECT payload FROM events WHERE id = ?"
	createTable             = `CREATE TABLE IF NOT EXISTS events (
		id      INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL,
		amount  INTEGER NOT NULL,
		payload TEXT    NOT NULL
	)`
	createIndex = "CREATE INDEX IF NOT EXISTS idx_events_user ON events(user_id)"
)

type queryResult struct {
	Engine  string  `json:"engine"`
	Query   string  `json:"query"`
	Version string  `json:"version"`
	Rows    int     `json:"rows"`
	Count   int     `json:"count"`
	P50     float64 `json:"p50_us"`
	// P95/P99 — указатели, а не float64: для агрегации (малая выборка, n=10)
	// они намеренно не публикуются (см. runAggregation) и остаются nil, чтобы
	// omitempty убрал их из JSON, а не подставил вводящий в заблуждение 0.
	P95       *float64 `json:"p95_us,omitempty"`
	P99       *float64 `json:"p99_us,omitempty"`
	OpsPerSec float64  `json:"ops_per_sec"`
	// Note — пояснение к результату, когда числа сами по себе могут ввести в
	// заблуждение (недостаточная выборка для хвостовых перцентилей и т.п.).
	Note string `json:"note,omitempty"`
}

func f64ptr(v float64) *float64 { return &v }

// row — одна строка events. Генерируется РОВНО ОДИН раз общим ГПСЧ и
// вставляется в обе БД без изменений: иначе разница в содержимом стала бы
// ещё одной переменной сравнения, помимо самого движка.
type row struct {
	id      int
	userID  int
	amount  int
	payload string
}

func main() {
	rows := flag.Int("rows", 1_000_000, "число строк в events (одинаково для обеих БД)")
	nAgg := flag.Int("n-agg", 10, "число повторов агрегирующего запроса (он тяжёлый — весь набор)")
	nPoint := flag.Int("n-point", 20000, "число точечных чтений по id")
	sqlitePath := flag.String("sqlite-path", "/data/olap-sqlite.db", "путь к файлу SQLite для этого сценария (отдельный от bench.db)")
	duckdbPath := flag.String("duckdb-path", "/data/olap.duckdb", "путь к файлу DuckDB для этого сценария")
	seed := flag.Int64("seed", 42, "семя ГПСЧ для генерации данных — одно и то же для обеих БД")
	flag.Parse()

	if *rows <= 0 {
		fmt.Fprintln(os.Stderr, "-rows должно быть больше нуля")
		os.Exit(1)
	}

	ctx := context.Background()

	// Свежие файлы на каждый прогон: досев поверх старых данных сделал бы
	// агрегацию нерепрезентативной (сумма и число групп поплыли бы), а старая
	// БД с другой схемой/данными — вообще некорректным сравнением.
	for _, p := range []string{*sqlitePath, *duckdbPath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "удаление старого файла", p, ":", err)
			os.Exit(1)
		}
	}

	sdb, err := openSQLite(*sqlitePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "открытие SQLite:", err)
		os.Exit(1)
	}
	defer func() { _ = sdb.Close() }()

	ddb, err := openDuckDB(*duckdbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "открытие DuckDB:", err)
		os.Exit(1)
	}
	defer func() { _ = ddb.Close() }()

	for _, s := range []struct {
		name string
		stmt string
	}{{"схема", createTable}, {"индекс", createIndex}} {
		if _, err := sdb.ExecContext(ctx, s.stmt); err != nil {
			fmt.Fprintln(os.Stderr, "sqlite", s.name, ":", err)
			os.Exit(1)
		}
		if _, err := ddb.ExecContext(ctx, s.stmt); err != nil {
			fmt.Fprintln(os.Stderr, "duckdb", s.name, ":", err)
			os.Exit(1)
		}
	}

	sqliteVer := scalarString(ctx, sdb, "SELECT sqlite_version()")
	duckdbVer := scalarString(ctx, ddb, "SELECT version()")
	fmt.Fprintf(os.Stderr, "версия SQLite: %s\n", sqliteVer)
	fmt.Fprintf(os.Stderr, "версия DuckDB: %s\n", duckdbVer)

	fmt.Fprintf(os.Stderr, "генерация %d строк (seed=%d)...\n", *rows, *seed)
	data := generate(*rows, *seed)

	fmt.Fprintln(os.Stderr, "засев SQLite...")
	t0 := time.Now()
	if err := seedTable(ctx, sdb, data); err != nil {
		fmt.Fprintln(os.Stderr, "засев sqlite:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "SQLite засеян за %s\n", time.Since(t0))

	fmt.Fprintln(os.Stderr, "засев DuckDB...")
	t0 = time.Now()
	if err := seedDuckDB(ctx, ddb, data); err != nil {
		fmt.Fprintln(os.Stderr, "засев duckdb:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "DuckDB засеян за %s\n", time.Since(t0))

	// EXPLAIN QUERY PLAN — дословно, ДО замера. idx_events_user некроющий
	// (нет amount): если планировщик выбрал индексный проход, часть разрыва
	// с DuckDB объясняется лишней строкой обращений к таблице за amount по
	// rowid (эффект плана, около 3.7x), а не ориентацией хранения (эффект
	// row-store vs column-store, около 153x на full scan против DuckDB).
	// Печатаем оба плана (по умолчанию и с NOT INDEXED) в fixtures/olap.txt
	// как есть.
	fmt.Fprintln(os.Stderr, "снимаем EXPLAIN QUERY PLAN для SQLite...")
	planDefault, err := explainPlan(ctx, sdb, aggregationQuery)
	if err != nil {
		fmt.Fprintln(os.Stderr, "explain (план по умолчанию):", err)
		os.Exit(1)
	}
	planNoIndex, err := explainPlan(ctx, sdb, aggregationQueryNoIndex)
	if err != nil {
		fmt.Fprintln(os.Stderr, "explain (NOT INDEXED):", err)
		os.Exit(1)
	}
	fmt.Println("== EXPLAIN QUERY PLAN: агрегация, план по умолчанию ==")
	for _, l := range planDefault {
		fmt.Println(l)
	}
	fmt.Println("== EXPLAIN QUERY PLAN: агрегация, NOT INDEXED (индекс запрещён явно) ==")
	for _, l := range planNoIndex {
		fmt.Println(l)
	}

	engines := []struct {
		name string
		db   *sql.DB
		ver  string
	}{
		{"sqlite", sdb, sqliteVer},
		{"duckdb", ddb, duckdbVer},
	}

	var results []queryResult
	for _, eng := range engines {
		// Прогрев не входит в замер (тот же приём, что в cmd/bench): первый
		// запрос после засева читает холодный кэш страниц/буферный пул.
		if _, err := runAggregation(ctx, eng.db, eng.name, eng.ver, *rows, 1, aggregationQuery, "aggregation"); err != nil {
			fmt.Fprintln(os.Stderr, eng.name, "прогрев агрегации:", err)
			os.Exit(1)
		}
		agg, err := runAggregation(ctx, eng.db, eng.name, eng.ver, *rows, *nAgg, aggregationQuery, "aggregation")
		if err != nil {
			fmt.Fprintln(os.Stderr, eng.name, "агрегация:", err)
			os.Exit(1)
		}
		results = append(results, agg)

		// Третий замер — только для SQLite: тот же запрос, но с NOT INDEXED,
		// чтобы разложить разрыв с DuckDB на "стоимость плана" и "стоимость
		// ориентации хранения" (см. комментарий к aggregationQueryNoIndex).
		// У DuckDB нет NOT INDEXED и своих B-tree индексов в этом смысле нет
		// (сканы по умолчанию колоночные) — сравнивать не с чем и незачем.
		if eng.name == "sqlite" {
			if _, err := runAggregation(ctx, eng.db, eng.name, eng.ver, *rows, 1, aggregationQueryNoIndex, "aggregation_noindex"); err != nil {
				fmt.Fprintln(os.Stderr, eng.name, "прогрев агрегации (NOT INDEXED):", err)
				os.Exit(1)
			}
			aggNoIdx, err := runAggregation(ctx, eng.db, eng.name, eng.ver, *rows, *nAgg, aggregationQueryNoIndex, "aggregation_noindex")
			if err != nil {
				fmt.Fprintln(os.Stderr, eng.name, "агрегация (NOT INDEXED):", err)
				os.Exit(1)
			}
			results = append(results, aggNoIdx)
		}

		warmupN := *nPoint / 10
		if warmupN > 0 {
			if _, err := runPoint(ctx, eng.db, eng.name, eng.ver, *rows, warmupN); err != nil {
				fmt.Fprintln(os.Stderr, eng.name, "прогрев точечного чтения:", err)
				os.Exit(1)
			}
		}
		pt, err := runPoint(ctx, eng.db, eng.name, eng.ver, *rows, *nPoint)
		if err != nil {
			fmt.Fprintln(os.Stderr, eng.name, "точечное чтение:", err)
			os.Exit(1)
		}
		results = append(results, pt)
	}

	for _, r := range results {
		out, err := json.Marshal(r)
		if err != nil {
			fmt.Fprintln(os.Stderr, "сериализация результата:", err)
			os.Exit(1)
		}
		fmt.Println(string(out))
	}
}

func openSQLite(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=NORMAL", path)
	return sql.Open("sqlite3", dsn)
}

func openDuckDB(path string) (*sql.DB, error) {
	return sql.Open("duckdb", path)
}

// explainPlan снимает EXPLAIN QUERY PLAN дословно (формат SQLite:
// id|parent|notused|detail) и возвращает строки detail как есть, без
// интерпретации — план должен уйти в фикстуру и отчёт буквально, чтобы
// решение "индекс использован или нет" принималось по факту, а не по догадке.
func explainPlan(ctx context.Context, db *sql.DB, query string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			return nil, err
		}
		lines = append(lines, detail)
	}
	return lines, rows.Err()
}

func scalarString(ctx context.Context, db *sql.DB, q string) string {
	var v string
	if err := db.QueryRowContext(ctx, q).Scan(&v); err != nil {
		return "неизвестна: " + err.Error()
	}
	return v
}

// generate строит все строки заранее ОДНИМ ГПСЧ с фиксированным seed —
// содержимое обеих БД должно быть идентичным, иначе разница в суммах
// агрегации могла бы быть принята за разницу в производительности.
func generate(n int, seed int64) []row {
	rng := rand.New(rand.NewSource(seed))
	data := make([]row, n)
	for i := 0; i < n; i++ {
		data[i] = row{
			id:      i + 1,
			userID:  rng.Intn(1000),
			amount:  rng.Intn(10000),
			payload: "payload-fixed-length-string",
		}
	}
	return data
}

// seedTable вставляет data батчами по ОДНОМУ multi-row INSERT на батч — для
// SQLite это заметно быстрее построчных ExecContext даже внутри общей
// транзакции. Годится только для SQLite: для DuckDB используется seedDuckDB
// (Appender API), см. её комментарий.
func seedTable(ctx context.Context, db *sql.DB, data []row) error {
	const batchSize = 1000
	for start := 0; start < len(data); start += batchSize {
		end := start + batchSize
		if end > len(data) {
			end = len(data)
		}
		if err := seedBatch(ctx, db, data[start:end]); err != nil {
			return fmt.Errorf("батч [%d:%d): %w", start, end, err)
		}
	}
	return nil
}

func seedBatch(ctx context.Context, db *sql.DB, batch []row) error {
	var b strings.Builder
	b.WriteString("INSERT INTO events (id, user_id, amount, payload) VALUES ")
	args := make([]any, 0, len(batch)*4)
	for i, r := range batch {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(?, ?, ?, ?)")
		args = append(args, r.id, r.userID, r.amount, r.payload)
	}
	_, err := db.ExecContext(ctx, b.String(), args...)
	return err
}

// seedDuckDB загружает data через нативный Appender, а не через INSERT.
// Multi-row INSERT (как для SQLite) на DuckDB измерялся отдельно и остался
// на ~2000 строк/с — 1 млн строк занял бы ~8 минут: DuckDB — колоночный
// движок, и построчные вставки через SQL (даже пачками) платят за пересборку
// векторов на каждый statement. Appender — задокументированный API самого
// DuckDB именно для объёмной загрузки и на порядки быстрее для этого сценария.
// Использование appender-а здесь не даёт SQLite скрытой форы: сравниваются
// не способы загрузки, а последующие SELECT-запросы над уже готовыми данными.
func seedDuckDB(ctx context.Context, db *sql.DB, data []row) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	return conn.Raw(func(driverConn any) error {
		dc, ok := driverConn.(driver.Conn)
		if !ok {
			return fmt.Errorf("неожиданный тип driver-соединения DuckDB: %T", driverConn)
		}
		appender, err := duckdb.NewAppenderFromConn(dc, "", "events")
		if err != nil {
			return err
		}
		for _, r := range data {
			if err := appender.AppendRow(r.id, r.userID, r.amount, r.payload); err != nil {
				_ = appender.Close()
				return err
			}
		}
		return appender.Close()
	})
}

// runAggregation гоняет n раз полный запрос агрегации по всей таблице и
// возвращает латентность ВСЕГО запроса (выполнение + вычитывание всех групп)
// в микросекундах. query/label позволяют прогнать один и тот же по смыслу
// запрос в двух вариантах (план по умолчанию / NOT INDEXED) без дублирования
// кода замера.
//
// P95/P99 намеренно НЕ публикуются (остаются nil): при n=10 (агрегация
// тяжёлая — полный скан 1 млн строк, гонять её 20000 раз как точечное чтение
// нереалистично по времени) nearest-rank даёт ОДИН И ТОТ ЖЕ индекс выборки
// (последний, т.е. максимум) и для 95-го, и для 99-го перцентиля — это не
// два разных хвостовых значения, а один и тот же max, дважды напечатанный
// под разными именами. Публиковать их как отдельные величины вводило бы в
// заблуждение. Из тех же соображений p50 по 10 замерам — тоже оценка ПОРЯДКА
// величины, а не точное число (см. Note и отчёт задачи 7).
func runAggregation(ctx context.Context, db *sql.DB, engine, ver string, totalRows, n int, query, label string) (queryResult, error) {
	if n <= 0 {
		return queryResult{}, fmt.Errorf("n должно быть больше нуля, получено %d", n)
	}
	lat := make([]float64, 0, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		t0 := time.Now()
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return queryResult{}, err
		}
		groups := 0
		for rows.Next() {
			var userID int
			var sum int64
			if err := rows.Scan(&userID, &sum); err != nil {
				_ = rows.Close()
				return queryResult{}, err
			}
			groups++
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return queryResult{}, err
		}
		_ = rows.Close()
		if groups == 0 {
			return queryResult{}, fmt.Errorf("агрегация вернула 0 групп — данные не засеяны")
		}
		lat = append(lat, float64(time.Since(t0).Microseconds()))
	}
	elapsed := time.Since(start).Seconds()
	return queryResult{
		Engine:    engine,
		Query:     label,
		Version:   ver,
		Rows:      totalRows,
		Count:     n,
		P50:       bench.Percentile(lat, 0.50),
		OpsPerSec: float64(n) / elapsed,
		Note: fmt.Sprintf(
			"p95/p99 не публикуются: при n=%d nearest-rank даёт один и тот же индекс (максимум выборки) для обоих перцентилей — недостаточная выборка, чтобы различать хвосты; p50 — оценка порядка величины, не точное число",
			n,
		),
	}, nil
}

// runPoint гоняет n точечных чтений по случайному id в диапазоне [1, totalRows]
// и возвращает перцентили латентности ОДНОГО чтения в микросекундах.
func runPoint(ctx context.Context, db *sql.DB, engine, ver string, totalRows, n int) (queryResult, error) {
	if n <= 0 {
		return queryResult{}, fmt.Errorf("n должно быть больше нуля, получено %d", n)
	}
	lat := make([]float64, 0, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		id := rand.Intn(totalRows) + 1
		t0 := time.Now()
		var payload string
		if err := db.QueryRowContext(ctx, pointQuery, id).Scan(&payload); err != nil {
			return queryResult{}, err
		}
		lat = append(lat, float64(time.Since(t0).Microseconds()))
	}
	elapsed := time.Since(start).Seconds()
	return queryResult{
		Engine:    engine,
		Query:     "point",
		Version:   ver,
		Rows:      totalRows,
		Count:     n,
		P50:       bench.Percentile(lat, 0.50),
		P95:       f64ptr(bench.Percentile(lat, 0.95)),
		P99:       f64ptr(bench.Percentile(lat, 0.99)),
		OpsPerSec: float64(n) / elapsed,
	}, nil
}
