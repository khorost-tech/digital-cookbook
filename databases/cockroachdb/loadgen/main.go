// loadgen — генератор нагрузки для стенда CockroachDB.
//
// Считает не «сколько выжали», а долю ретраев и распределение задержек:
// стенду нужны не рекорды, а честная картина contention и цены коммита.
//
// Каждый воркер держит СВОЁ соединение из пула на всю транзакцию, включая
// повторы — иначе повтор после 40001 может уйти на другое соединение,
// и мы будем мерить не то, что думаем.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// monoSeq — строго монотонный ключ, общий для всех воркеров.
//
// Раньше монотонность обеспечивал серверный nextval(). Из-за этого
// «монотонный» вариант отличался от «случайного» не только порядком
// ключей: значение бралось на сервере, INSERT имел другую форму и меньше
// параметров, плюс добавлялась конкуренция за саму последовательность.
// Приписать разницу порядку ключей было нельзя. Теперь ВСЕ три варианта
// генерируют ключ на клиенте и шлют один и тот же INSERT с тремя
// параметрами. Полной изоляции переменной это НЕ даёт: генераторы у трёх
// вариантов разные и работают внутри измеряемой транзакции, так что
// сравниваются три клиентские стратегии целиком.
var monoSeq atomic.Int64

// uuidV4 собирает UUID на клиенте, чтобы третий вариант не отличался от
// второго ещё и местом генерации. Тип и размер ключа при этом всё равно
// меняются вместе с генератором — переменная не одна.
func uuidV4(rnd *rand.Rand) string {
	var b [16]byte
	for i := range b {
		b[i] = byte(rnd.Intn(256))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type result struct {
	Mode      string  `json:"mode"`
	Isolation string  `json:"isolation"`
	Workers   int     `json:"workers"`
	Ops       int64   `json:"ops"`
	Errors    int64   `json:"errors"`
	Retries   int64   `json:"retries"`
	P50ms     float64 `json:"p50_ms"`
	P95ms     float64 `json:"p95_ms"`
	P99ms     float64 `json:"p99_ms"`
	// Транзакции, оборванные истечением времени прогона: ошибка пришла
	// от САМОГО контекста (DeadlineExceeded/Canceled). Исход у них
	// неизвестен — часть могла зафиксироваться на сервере, не дойдя
	// ответом до клиента, поэтому это не «успех» и не «отказ базы».
	//
	// Классифицируем по ошибке через errors.Is, а НЕ по факту «контекст
	// уже истёк». Прежняя версия делала второе — и настоящая серверная
	// ошибка, пришедшая за миг до дедлайна, молча попадала сюда же.
	DeadlineOrUnknown int64 `json:"deadline_or_unknown"`
	// SQLSTATE'ы неуспешных транзакций с их числом (без прерванных по
	// времени). Без этого расхождение между счётчиком в базе и числом
	// успехов нельзя объяснить: неоднозначный коммит (40003) неотличим
	// от обычного отказа.
	ErrorCodes map[string]int64 `json:"error_codes"`
}

// sqlState — код ошибки, если она пришла от сервера; иначе категория,
// по которой видно, что до сервера дело не дошло.
func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "context"
	}
	return "other"
}

func main() {
	dsn := flag.String("dsn", "postgresql://root@127.0.0.1:15436/bank?sslmode=disable", "строка подключения")
	mode := flag.String("mode", "hot",
		"insert-seq | insert-uuid | split-mono | split-rand | split-randint | hot | hot-rmw | jdbc-rmw | read | read-hot | geo-insert | geo-read")
	isolation := flag.String("isolation", "serializable", "serializable | read committed")
	workers := flag.Int("workers", 8, "число воркеров")
	duration := flag.Duration("duration", 30*time.Second, "длительность прогона")
	maxRetries := flag.Int("max-retries", 10, "предел повторов одной транзакции")
	flag.Parse()

	iso, err := parseIsolation(*isolation)
	if err != nil {
		die("%v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(*dsn)
	if err != nil {
		die("разбор dsn: %v", err)
	}
	// Пул ровно под число воркеров: каждый воркер занимает соединение
	// на всю транзакцию, включая повторы.
	cfg.MaxConns = int32(*workers)
	cfg.MinConns = int32(*workers)

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		die("пул: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		die("кластер не отвечает: %v", err)
	}

	// Монотонный счётчик продолжается с того места, где таблица кончилась.
	//
	// Без этого каждый новый запуск генератора начинал бы с единицы и бился
	// в уже существующие ключи: при замере деградации, где таблица между
	// интервалами НЕ очищается, это давало лавину 23505 и падение ops,
	// неотличимое по форме от эффекта горячего диапазона. Один прогон был
	// испорчен ровно так.
	if *mode == "split-mono" {
		var maxID int64
		if err := pool.QueryRow(context.Background(),
			`SELECT coalesce(max(id), 0) FROM acct_mono`).Scan(&maxID); err != nil {
			die("не удалось прочитать текущий максимум acct_mono: %v", err)
		}
		monoSeq.Store(maxID)
	}

	var (
		mu      sync.Mutex
		lat     []float64
		ops     int64
		errCnt  int64
		aborted int64
		retries int64
		lastErr error
		codes   = map[string]int64{}
		wg      sync.WaitGroup
	)

	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rnd := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)))
			for ctx.Err() == nil {
				start := time.Now()
				attempts, err := runTxn(ctx, pool, *mode, iso, *maxRetries, rnd, id)
				elapsed := time.Since(start).Seconds() * 1000

				mu.Lock()
				retries += int64(attempts - 1)
				if err != nil {
					if errors.Is(err, context.DeadlineExceeded) ||
						errors.Is(err, context.Canceled) {
						// Оборвал сам контекст: исход неизвестен.
						aborted++
					} else {
						// Всё остальное — настоящая ошибка, даже если
						// прогон к этому моменту уже кончился.
						errCnt++
						codes[sqlState(err)]++
						lastErr = err
					}
				} else {
					ops++
					lat = append(lat, elapsed)
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	sort.Float64s(lat)
	out := result{
		Mode: *mode, Isolation: *isolation, Workers: *workers,
		Ops: ops, Errors: errCnt, DeadlineOrUnknown: aborted, Retries: retries,
		P50ms: pct(lat, 0.50), P95ms: pct(lat, 0.95), P99ms: pct(lat, 0.99),
		ErrorCodes: codes,
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		die("вывод: %v", err)
	}
	// Ошибки не прячем: молчаливый прогон с нулём операций выглядел бы
	// как успешный замер, а это самый опасный вид неправды в фикстурах.
	if lastErr != nil {
		fmt.Fprintf(os.Stderr, "loadgen: последняя ошибка (%d всего): %v\n", errCnt, lastErr)
	}
	if ops == 0 {
		die("ноль успешных операций — мерить нечего")
	}
}

func parseIsolation(s string) (pgx.TxIsoLevel, error) {
	switch strings.ToLower(strings.ReplaceAll(s, "_", " ")) {
	case "serializable":
		return pgx.Serializable, nil
	case "read committed":
		return pgx.ReadCommitted, nil
	default:
		return "", fmt.Errorf("неизвестный уровень изоляции %q (serializable | read committed)", s)
	}
}

// runTxn выполняет одну транзакцию с корректным retry-циклом и возвращает
// число ПОПЫТОК (1 — прошло с первого раза).
func runTxn(ctx context.Context, pool *pgxpool.Pool, mode string, iso pgx.TxIsoLevel,
	maxRetries int, rnd *rand.Rand, id int) (int, error) {

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 1, err
	}
	defer conn.Release()

	opts := pgx.TxOptions{IsoLevel: iso}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := pgx.BeginTxFunc(ctx, conn, opts, func(tx pgx.Tx) error {
			return statement(ctx, tx, mode, rnd, id)
		})
		if err == nil {
			return attempt, nil
		}
		if !retryable(err) {
			return attempt, err
		}
		// Экспоненциальная пауза с джиттером: без неё воркеры синхронно
		// бьются в один ключ и мерят собственную синхронность.
		back := time.Duration(1<<uint(attempt-1)) * time.Millisecond
		if back > 100*time.Millisecond {
			back = 100 * time.Millisecond
		}
		select {
		case <-time.After(back + time.Duration(rnd.Intn(2000))*time.Microsecond):
		case <-ctx.Done():
			return attempt, ctx.Err()
		}
	}
	return maxRetries, fmt.Errorf("исчерпан предел повторов (%d)", maxRetries)
}

func statement(ctx context.Context, tx pgx.Tx, mode string, rnd *rand.Rand, id int) error {
	switch mode {
	case "insert-seq":
		_, err := tx.Exec(ctx,
			`INSERT INTO accounts_seq (owner, balance) VALUES ($1, $2)`,
			fmt.Sprintf("worker-%d", id), rnd.Float64()*1000)
		return err
	case "insert-uuid":
		_, err := tx.Exec(ctx,
			`INSERT INTO accounts_uuid (owner, balance) VALUES ($1, $2)`,
			fmt.Sprintf("worker-%d", id), rnd.Float64()*1000)
		return err
	case "hot":
		// Однооператорная запись: конкуренты НЕ отменяются, а встают в
		// очередь на ключе. Contention виден задержкой, а не ретраем —
		// это и есть предмет замера в этом режиме.
		_, err := tx.Exec(ctx, `UPDATE hot_counter SET hits = hits + 1 WHERE id = 1`)
		return err
	case "hot-rmw":
		// Прочитать, а потом записать прочитанное. Именно здесь рождается
		// ретрай: пока транзакция думала, значение под ней перезаписали,
		// и её запись оказывается «слишком старой» — по метрикам это
		// txn.restarts.writetooold, а не serializable-конфликт.
		var hits int64
		if err := tx.QueryRow(ctx, `SELECT hits FROM hot_counter WHERE id = 1`).Scan(&hits); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE hot_counter SET hits = $1 WHERE id = 1`, hits+1)
		return err
	case "read":
		var n int64
		return tx.QueryRow(ctx, `SELECT count(*) FROM accounts_uuid`).Scan(&n)
	case "split-mono":
		// Строго монотонный ключ: все вставки целятся в один и тот же конец
		// ключевого пространства. Значение — с клиента, как и у двух других
		// вариантов, чтобы отличий было как можно меньше.
		_, err := tx.Exec(ctx,
			`INSERT INTO acct_mono (id, owner, balance) VALUES ($1, $2, $3)`,
			monoSeq.Add(1), fmt.Sprintf("worker-%d", id), rnd.Float64()*1000)
		return err
	case "split-randint":
		// Тот же INT8 и тот же INSERT, что у split-mono. Отличается
		// прежде всего распределением значений — случайное вместо
		// возрастающего, — но и генератор здесь свой.
		_, err := tx.Exec(ctx,
			`INSERT INTO acct_randint (id, owner, balance) VALUES ($1, $2, $3)`,
			rnd.Int63(), fmt.Sprintf("worker-%d", id), rnd.Float64()*1000)
		return err
	case "split-rand":
		// Тот же INSERT, случайное значение — но другого типа и размера.
		// Пара с split-randint различается типом и размером ключа —
		// но не только ими: генератор здесь тоже свой.
		_, err := tx.Exec(ctx,
			`INSERT INTO acct_rand (id, owner, balance) VALUES ($1, $2, $3)`,
			uuidV4(rnd), fmt.Sprintf("worker-%d", id), rnd.Float64()*1000)
		return err
	case "jdbc-rmw":
		// Та же «прочитал — записал» по ключу, в который одновременно
		// бьёт Java-программа: без этой конкуренции retry-цикл на JDBC
		// не сработал бы ни разу и замер был бы пустым.
		var hits int64
		if err := tx.QueryRow(ctx, `SELECT hits FROM jdbc_counter WHERE id = 1`).Scan(&hits); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE jdbc_counter SET hits = $1 WHERE id = 1`, hits+1)
		return err
	case "geo-insert":
		// Вставка в многорегиональную таблицу: регион строки берётся
		// из региона узла-шлюза, через который пришёл запрос.
		_, err := tx.Exec(ctx,
			`INSERT INTO users (email, note) VALUES ($1, $2)`,
			fmt.Sprintf("user-%d-%d@example.com", id, rnd.Int63()),
			fmt.Sprintf("note-%d", rnd.Intn(1000)))
		return err
	case "geo-read":
		var n int64
		return tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	case "read-hot":
		// Точечное чтение ключа, который прямо сейчас переписывают с
		// другого узла. Именно так ловится uncertainty: значение могло
		// быть записано «почти одновременно» с началом чтения.
		var hits int64
		return tx.QueryRow(ctx, `SELECT hits FROM hot_counter WHERE id = 1`).Scan(&hits)
	default:
		return fmt.Errorf("неизвестный режим %q", mode)
	}
}

// retryable распознаёт ретраибельные ошибки CockroachDB по SQLSTATE 40001.
// Именно так их и положено ловить: текст различается для contention и для
// uncertainty, а код у них общий.
func retryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001"
	}
	return false
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	return sorted[i]
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "loadgen: "+format+"\n", args...)
	os.Exit(1)
}
