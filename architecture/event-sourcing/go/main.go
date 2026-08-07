// Стенд «Event Sourcing на практике» — Postgres как event store.
//
// Пример к статье https://khorost.tech/architecture/event-sourcing-in-practice/
// и к серии «event-sourcing-deep-dive».
//
// Домен: учёт кислорода в резервуарах лунной базы. Один поток (stream) = один
// резервуар. События (TankRegistered / OxygenAdded / OxygenConsumed) неизменяемы,
// текущее состояние — свёртка событий.
//
// Что демонстрирует один прогон (все проверки — assert-ы, при расхождении log.Fatalf):
//   1. Append с ожидаемой версией и оптимистичная блокировка через PRIMARY KEY(stream_id,version).
//   2. Реальный конфликт версий: две горутины пишут version=N в один стрим — одна падает на PK.
//   3. Async-проектор баланса: читает по global_pos, идемпотентный UPSERT, чекпоинт в той же
//      транзакции; «падение» и рестарт проектора — без потерь и дублей.
//   4. Снапшоты: снимок каждые N событий; загрузка агрегата = снапшот + хвост событий.
//   5. Blue-green rebuild: новая проекция balances_v2 реплеем всей истории + атомарный свап.
//   6. Ассерты: свёртка событий == проекция; конфликт реально произошёл; rebuild == онлайн-проекция.
//
// Запуск:
//   docker compose -f event-sourcing/compose/compose.yml up -d   # Postgres 16 на localhost:5452
//   cd event-sourcing/go && go run .
//   # DATABASE_URL переопределяет строку подключения (напр. при запуске в docker-сети).
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"math"
	mrand "math/rand"
	"os"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	numTanks      = 5   // резервуаров (стримов)
	snapshotEvery = 5   // снапшот каждые N версий стрима
	batchSize     = 16  // размер пачки проектора
	epsilon       = 1e-9
)

func main() {
	ctx := context.Background()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://esdemo:esdemo@localhost:5452/esdemo"
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("подключение к Postgres: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("Postgres недоступен (%s): %v\nПодними стенд: docker compose -f event-sourcing/compose/compose.yml up -d", dsn, err)
	}

	if err := applySchema(ctx, pool); err != nil {
		log.Fatalf("создание схемы: %v", err)
	}
	store := NewStore(pool)

	// ---------------------------------------------------------------
	// 1. Наполняем event store: регистрируем резервуары + активность.
	// ---------------------------------------------------------------
	fmt.Println("== 1. Наполнение event store ==")
	rng := mrand.New(mrand.NewSource(42))
	streams := make([]string, 0, numTanks)
	for i := 0; i < numTanks; i++ {
		sid := newUUID()
		streams = append(streams, sid)
		capacity := 1000.0 + float64(i)*250

		// version 0 -> регистрация даёт version 1.
		v, err := store.Append(ctx, sid, 0, []EventData{
			{Type: EvtRegistered, Data: map[string]float64{"capacity": capacity}},
		})
		if err != nil {
			log.Fatalf("регистрация резервуара: %v", err)
		}
		// Активность: 10..14 событий добавления/расхода.
		ops := 10 + rng.Intn(5)
		level := 0.0
		for j := 0; j < ops; j++ {
			var ev EventData
			if level < 200 || rng.Intn(3) != 0 {
				amt := math.Round(rng.Float64()*300*10) / 10
				level += amt
				ev = EventData{Type: EvtAdded, Data: map[string]float64{"amount": amt}}
			} else {
				amt := math.Round(rng.Float64()*float64(int(level))*0.5*10) / 10
				level -= amt
				ev = EventData{Type: EvtConsumed, Data: map[string]float64{"amount": amt}}
			}
			v, err = store.Append(ctx, sid, v, []EventData{ev})
			if err != nil {
				log.Fatalf("append события: %v", err)
			}
		}
		fmt.Printf("  резервуар %s: %d событий (версия %d)\n", short(sid), v, v)
	}

	// Снапшоты: снимок каждые snapshotEvery версий по каждому стриму.
	makeSnapshots(ctx, store, pool, streams, snapshotEvery)

	// ---------------------------------------------------------------
	// 2. Конфликт версий при параллельной записи в один стрим.
	// ---------------------------------------------------------------
	fmt.Println("== 2. Конфликт версий (две горутины пишут version=N) ==")
	conflictStream := streams[0]
	curVer := streamVersion(ctx, pool, conflictStream)
	conflicts := raceAppend(ctx, store, conflictStream, curVer)
	fmt.Printf("  обе горутины писали version=%d: успех=1, конфликт=%d\n", curVer+1, conflicts)
	if conflicts != 1 {
		log.Fatalf("ASSERT провален: ожидался ровно 1 конфликт версий, получено %d", conflicts)
	}

	// ---------------------------------------------------------------
	// 3. Async-проектор + падение/рестарт.
	// ---------------------------------------------------------------
	fmt.Println("== 3. Async-проектор баланса (падение и рестарт) ==")
	// Первый экземпляр обрабатывает часть истории и «падает».
	proj := NewProjector(pool, "balances", "balances")
	n, err := proj.RunBatch(ctx, 7)
	if err != nil {
		log.Fatalf("проектор (первая пачка): %v", err)
	}
	fmt.Printf("  экземпляр #1 обработал %d событий, чекпоинт=%d, затем «упал»\n", n, checkpoint(ctx, pool, "balances"))
	// Новый экземпляр продолжает с чекпоинта — без потерь и дублей.
	proj2 := NewProjector(pool, "balances", "balances")
	total, err := proj2.Drain(ctx, batchSize)
	if err != nil {
		log.Fatalf("проектор (дренаж): %v", err)
	}
	fmt.Printf("  экземпляр #2 догнал хвост: +%d событий, чекпоинт=%d\n", total, checkpoint(ctx, pool, "balances"))

	// Ассерт: свёртка событий по каждому стриму == проекция balances.
	assertProjectionMatchesFold(ctx, store, pool, "balances", streams)
	fmt.Println("  ASSERT ok: свёртка событий == проекция balances")

	// ---------------------------------------------------------------
	// 4. Снапшоты: загрузка агрегата = снапшот + хвост.
	// ---------------------------------------------------------------
	fmt.Println("== 4. Снапшоты (агрегат = снапшот + хвост событий) ==")
	for _, sid := range streams {
		agg, used, err := LoadAggregate(ctx, store, pool, sid)
		if err != nil {
			log.Fatalf("загрузка агрегата: %v", err)
		}
		full, err := foldTank(mustRead(ctx, store, sid))
		if err != nil {
			log.Fatalf("свёртка: %v", err)
		}
		if !used || math.Abs(agg.Level-full.Level) > epsilon || agg.Version != full.Version {
			log.Fatalf("ASSERT провален: агрегат из снапшота %s != полная свёртка (snap=%v)", short(sid), used)
		}
	}
	fmt.Printf("  ASSERT ok: по всем %d резервуарам «снапшот+хвост» == полная свёртка\n", len(streams))

	// ---------------------------------------------------------------
	// 5. Blue-green rebuild проекции.
	// ---------------------------------------------------------------
	fmt.Println("== 5. Blue-green rebuild (balances_v2 реплеем всей истории) ==")
	replayed, err := Rebuild(ctx, pool, batchSize)
	if err != nil {
		log.Fatalf("rebuild: %v", err)
	}
	fmt.Printf("  реплей всей истории с global_pos=0: %d событий -> balances_v2\n", replayed)

	// Ассерт: новая проекция побайтово совпадает с онлайн-проекцией.
	assertProjectionsEqual(ctx, pool, "balances", "balances_v2")
	fmt.Println("  ASSERT ok: balances_v2 (rebuild) == balances (онлайн)")

	// Атомарный свап и повторная проверка.
	if err := SwapProjections(ctx, pool); err != nil {
		log.Fatalf("swap: %v", err)
	}
	assertProjectionMatchesFold(ctx, store, pool, "balances", streams)
	fmt.Println("  ASSERT ok: после атомарного свапа balances == свёртка событий")

	fmt.Println("\nВсе проверки пройдены. Event Sourcing на практике: append+optimistic, проектор, снапшоты, rebuild.")
}

// makeSnapshots делает снимок каждые every версий по каждому стриму.
func makeSnapshots(ctx context.Context, store *Store, pool *pgxpool.Pool, streams []string, every int) {
	saved := 0
	for _, sid := range streams {
		events := mustRead(ctx, store, sid)
		t := &Tank{}
		for _, e := range events {
			if err := t.Apply(e); err != nil {
				log.Fatalf("свёртка для снапшота: %v", err)
			}
			if t.Version%int64(every) == 0 {
				if err := SaveSnapshot(ctx, pool, t); err != nil {
					log.Fatalf("сохранение снапшота: %v", err)
				}
				saved++
			}
		}
	}
	fmt.Printf("  снапшотов сохранено: %d (каждые %d версий)\n", saved, every)
}

// raceAppend запускает две горутины, обе пишут version=curVer+1 в один стрим.
// Возвращает число получивших ErrVersionConflict (ожидается ровно 1).
func raceAppend(ctx context.Context, store *Store, streamID string, curVer int64) int {
	var wg sync.WaitGroup
	var mu sync.Mutex
	conflicts := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			_, err := store.Append(ctx, streamID, curVer, []EventData{
				{Type: EvtAdded, Data: map[string]float64{"amount": 10 + float64(worker)}},
			})
			if err == ErrVersionConflict {
				mu.Lock()
				conflicts++
				mu.Unlock()
			} else if err != nil {
				log.Fatalf("raceAppend неожиданная ошибка: %v", err)
			}
		}(i)
	}
	wg.Wait()
	return conflicts
}

// --- ассерты и вспомогательные чтения ---

type balRow struct {
	level   float64
	version int64
}

func readProjection(ctx context.Context, pool *pgxpool.Pool, table string) map[string]balRow {
	rows, err := pool.Query(ctx, fmt.Sprintf(`SELECT stream_id, level, version FROM %s`, table))
	if err != nil {
		log.Fatalf("чтение проекции %s: %v", table, err)
	}
	defer rows.Close()
	out := map[string]balRow{}
	for rows.Next() {
		var id string
		var b balRow
		if err := rows.Scan(&id, &b.level, &b.version); err != nil {
			log.Fatalf("scan проекции: %v", err)
		}
		out[id] = b
	}
	return out
}

func assertProjectionMatchesFold(ctx context.Context, store *Store, pool *pgxpool.Pool, table string, streams []string) {
	proj := readProjection(ctx, pool, table)
	for _, sid := range streams {
		t, err := foldTank(mustRead(ctx, store, sid))
		if err != nil {
			log.Fatalf("свёртка: %v", err)
		}
		b, ok := proj[sid]
		if !ok {
			log.Fatalf("ASSERT провален: в проекции %s нет стрима %s", table, short(sid))
		}
		if math.Abs(b.level-t.Level) > epsilon || b.version != t.Version {
			log.Fatalf("ASSERT провален: %s стрим %s: проекция(level=%.1f,v=%d) != свёртка(level=%.1f,v=%d)",
				table, short(sid), b.level, b.version, t.Level, t.Version)
		}
	}
}

func assertProjectionsEqual(ctx context.Context, pool *pgxpool.Pool, a, b string) {
	pa := readProjection(ctx, pool, a)
	pb := readProjection(ctx, pool, b)
	if len(pa) != len(pb) {
		log.Fatalf("ASSERT провален: разное число строк %s=%d %s=%d", a, len(pa), b, len(pb))
	}
	for id, va := range pa {
		vb, ok := pb[id]
		if !ok || math.Abs(va.level-vb.level) > epsilon || va.version != vb.version {
			log.Fatalf("ASSERT провален: стрим %s расходится: %s=%+v %s=%+v", short(id), a, va, b, vb)
		}
	}
}

func mustRead(ctx context.Context, store *Store, sid string) []Event {
	events, err := store.ReadStream(ctx, sid)
	if err != nil {
		log.Fatalf("чтение стрима: %v", err)
	}
	return events
}

func streamVersion(ctx context.Context, pool *pgxpool.Pool, sid string) int64 {
	var v int64
	if err := pool.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM events WHERE stream_id=$1`, sid).Scan(&v); err != nil {
		log.Fatalf("streamVersion: %v", err)
	}
	return v
}

func checkpoint(ctx context.Context, pool *pgxpool.Pool, name string) int64 {
	var v int64
	if err := pool.QueryRow(ctx, `SELECT coalesce(last_pos,0) FROM projection_checkpoint WHERE name=$1`, name).Scan(&v); err != nil {
		return 0
	}
	return v
}

// newUUID — UUID v4 из crypto/rand (без внешних зависимостей).
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		log.Fatalf("newUUID: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func short(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}
