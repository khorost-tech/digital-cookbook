// Package bench гоняет одинаковую нагрузку на разные СУБД и считает перцентили.
// Ключевое требование: клиентский код общий для всех плеч — различаются только
// драйвер и DSN, иначе сравнение измеряло бы разницу в коде, а не в БД.
package bench

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Op string

const (
	OpRead   Op = "read"
	OpInsert Op = "insert"
)

type Result struct {
	Arm        string  `json:"arm"`
	Op         string  `json:"op"`
	Durability string  `json:"durability"`
	Count      int     `json:"count"`
	Writers    int     `json:"writers"`
	BusyErrors int     `json:"busy_errors"`
	P50        float64 `json:"p50_us"`
	P95        float64 `json:"p95_us"`
	P99        float64 `json:"p99_us"`
	OpsPerSec  float64 `json:"ops_per_sec"`
}

// Percentile возвращает q-й перцентиль по НЕотсортированной копии выборки.
func Percentile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	// Метод nearest-rank: idx = ceil(q*n) - 1, а не floor((n-1)*q) — иначе
	// p95/p99 на малых выборках занижаются на позицию (проверено тестом).
	idx := int(math.Ceil(q*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

// readQuery/insertQuery — единственный источник текста запроса для всех плеч.
// SQLite использует `?` как есть; для Postgres тот же текст проходит через
// RewritePlaceholders. Цикл выполнения ниже один на все три плеча — различаться
// могут только текст плейсхолдеров (диктуется драйвером) и DSN.
const (
	readQuery = "SELECT id, user_id, amount, payload FROM events WHERE id = ?"
	// id передаётся явно, а не оставляется движку: `INTEGER PRIMARY KEY` в
	// SQLite — это псевдоним rowid и автоинкрементится сам, а в PostgreSQL
	// это обычная колонка без DEFAULT, и вставка без id падает на NOT NULL.
	// Явный id — единственный способ оставить и схему, и запрос идентичными
	// для обоих движков вместо ветки на автоинкремент под каждый диалект.
	insertQuery = "INSERT INTO events (id, user_id, amount, payload) VALUES (?, ?, ?, ?)"
)

// Run выполняет n операций и возвращает перцентили в микросекундах.
// arm нужен только для выбора синтаксиса плейсхолдеров (sqlite: `?`, иначе
// `$N` через RewritePlaceholders) и для заполнения поля Arm в результате —
// сам цикл выполнения запроса от arm не зависит.
//
// base — верхняя граница уже существующих id: для OpRead это диапазон
// выборки ([1, base]), для OpInsert — стартовый id следующей вставки
// (base+1, base+2, ...). Одно число, один и тот же смысл «сколько строк уже
// есть» для обеих операций и всех трёх плеч.
func Run(ctx context.Context, db *sql.DB, op Op, n int, base int, arm string) (Result, error) {
	if n <= 0 {
		return Result{}, fmt.Errorf("n должно быть больше нуля, получено %d", n)
	}
	readQ, insertQ := readQuery, insertQuery
	if arm != "sqlite" {
		readQ = RewritePlaceholders(readQuery)
		insertQ = RewritePlaceholders(insertQuery)
	}

	lat := make([]float64, 0, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		t0 := time.Now()
		var err error
		switch op {
		case OpRead:
			var id, userID, amount int
			var payload string
			row := db.QueryRowContext(ctx, readQ, rand.Intn(base)+1)
			err = row.Scan(&id, &userID, &amount, &payload)
		case OpInsert:
			_, err = db.ExecContext(ctx, insertQ, base+1+i,
				rand.Intn(1000), rand.Intn(10000), "payload-fixed-length-string")
		default:
			return Result{}, fmt.Errorf("неизвестная операция %q", op)
		}
		if err != nil {
			return Result{}, fmt.Errorf("%s: %w", op, err)
		}
		lat = append(lat, float64(time.Since(t0).Microseconds()))
	}
	elapsed := time.Since(start).Seconds()
	return Result{
		Arm:       arm,
		Op:        string(op),
		Count:     n,
		Writers:   1,
		P50:       Percentile(lat, 0.50),
		P95:       Percentile(lat, 0.95),
		P99:       Percentile(lat, 0.99),
		OpsPerSec: float64(n) / elapsed,
	}, nil
}

// isBusyContention сообщает, является ли ошибка признаком конкуренции писателей
// (SQLite: заблокированный файл), а не какой-то другой поломкой. Проверка по
// тексту, а не по коду ошибки: mattn/go-sqlite3 не всегда всплывает как
// sqlite3.Error через database/sql (может быть обёрнута), но текст сообщения
// от libsqlite3 стабилен на обеих формулировках.
func isBusyContention(err error) bool {
	s := err.Error()
	return strings.Contains(s, "database is locked") || strings.Contains(s, "SQLITE_BUSY")
}

// RunConcurrent — сценарий «граница одного писателя»: n вставок делится между
// len(dbs) конкурентными писателями (каждый — своё соединение, а не общий пул,
// иначе Go сериализовал бы их сам и до реальной блокировки SQLite дело бы не
// дошло). Ошибки конкуренции (database is locked / SQLITE_BUSY) считаются в
// BusyErrors и НЕ прерывают прогон — их частота и есть искомая граница.
// Латентность неуспешной попытки тоже попадает в перцентили: клиент реально
// прождал до busy_timeout прежде чем получить отказ, и p99 обязан это видеть.
// Любая другая ошибка (не конкуренция) по-прежнему прерывает прогон.
func RunConcurrent(ctx context.Context, dbs []*sql.DB, op Op, n int, base int, arm string) (Result, error) {
	if n <= 0 {
		return Result{}, fmt.Errorf("n должно быть больше нуля, получено %d", n)
	}
	if len(dbs) == 0 {
		return Result{}, fmt.Errorf("нужен хотя бы один writer")
	}
	if op != OpInsert {
		return Result{}, fmt.Errorf("RunConcurrent поддерживает только insert, получено %q", op)
	}
	insertQ := insertQuery
	if arm != "sqlite" {
		insertQ = RewritePlaceholders(insertQuery)
	}

	writers := len(dbs)
	var (
		mu         sync.Mutex
		lat        = make([]float64, 0, n)
		busyErrors int32
		firstErr   error
		wg         sync.WaitGroup
	)
	nextID := int64(base)

	start := time.Now()
	for w := 0; w < writers; w++ {
		share := n / writers
		if w < n%writers {
			share++
		}
		wg.Add(1)
		go func(db *sql.DB, share int) {
			defer wg.Done()
			for i := 0; i < share; i++ {
				id := atomic.AddInt64(&nextID, 1)
				t0 := time.Now()
				_, err := db.ExecContext(ctx, insertQ, id,
					rand.Intn(1000), rand.Intn(10000), "payload-fixed-length-string")
				d := float64(time.Since(t0).Microseconds())

				mu.Lock()
				lat = append(lat, d)
				if err != nil {
					if isBusyContention(err) {
						atomic.AddInt32(&busyErrors, 1)
					} else if firstErr == nil {
						firstErr = err
					}
				}
				mu.Unlock()
			}
		}(dbs[w], share)
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()

	if firstErr != nil {
		return Result{}, fmt.Errorf("insert: %w", firstErr)
	}

	return Result{
		Arm:        arm,
		Op:         string(op),
		Count:      len(lat),
		Writers:    writers,
		BusyErrors: int(busyErrors),
		P50:        Percentile(lat, 0.50),
		P95:        Percentile(lat, 0.95),
		P99:        Percentile(lat, 0.99),
		OpsPerSec:  float64(len(lat)) / elapsed,
	}, nil
}
