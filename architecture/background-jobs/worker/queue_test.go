package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testDSN() string {
	if v := os.Getenv("BJ_DSN"); v != "" {
		return v
	}
	return "postgres://jobs:jobs@localhost:5456/jobs"
}

// setup чистит очередь перед каждым тестом: тесты идут против ЖИВОГО PG, и
// остатки предыдущего теста дали бы ложный результат.
func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	db, err := Connect(ctx, testDSN())
	if err != nil {
		// НЕ Skip: пропуск выглядел бы как зелёный прогон, и «тесты прошли»
		// означало бы «тесты не выполнялись». Пропуск возможен только явным
		// опт-аутом, и тогда это видно в выводе.
		if os.Getenv("BJ_SKIP_INTEGRATION") == "1" {
			t.Skip("BJ_SKIP_INTEGRATION=1 — интеграционные тесты пропущены НАМЕРЕННО")
		}
		t.Fatalf("PostgreSQL недоступен (%v) — поднимите compose/compose.yml "+
			"или задайте BJ_SKIP_INTEGRATION=1, если пропуск осознан", err)
	}
	if _, err := db.Exec(ctx, "TRUNCATE jobs RESTART IDENTITY"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func enqueue(t *testing.T, db *pgxpool.Pool, kind string) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(context.Background(),
		"INSERT INTO jobs (kind) VALUES ($1) RETURNING id", kind).Scan(&id)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return id
}

// Взятая джоба больше не выдаётся: после перехода queued -> running она не
// проходит фильтр claim. Падающий вариант: если бы claim не менял состояние или
// не фильтровал по нему, второй вызов вернул бы ту же строку — двойное исполнение.
//
// Что этот тест НЕ доказывает — пользу SKIP LOCKED. Вызовы здесь
// ПОСЛЕДОВАТЕЛЬНЫЕ, гонки нет, и тест проходит даже с убранным SKIP LOCKED
// (проверено экспериментом). Эксклюзивность обеспечивают блокировка строки и
// переход состояния, а SKIP LOCKED покупает пропускную способность.
// Корректность под настоящей конкуренцией проверяет
// TestConcurrentClaimGivesDistinctJobs ниже; цену сериализации без SKIP LOCKED
// измеряет отдельный живой замер (артефакт 6, scripts/skip-locked-demo.sh).
func TestClaimedJobIsNotHandedOutAgain(t *testing.T) {
	ctx := context.Background()
	db := setup(t)
	enqueue(t, db, "email")

	first, err := Claim(ctx, db, "w1", time.Minute)
	if err != nil || first == nil {
		t.Fatalf("первый claim: job=%v err=%v", first, err)
	}
	second, err := Claim(ctx, db, "w2", time.Minute)
	if err != nil {
		t.Fatalf("второй claim: %v", err)
	}
	if second != nil {
		t.Fatalf("одна джоба выдана дважды: id=%d обоим воркерам", second.ID)
	}
	if first.Attempt != 1 {
		t.Fatalf("attempt после первого claim = %d, ожидался 1", first.Attempt)
	}
}

// Корректность под НАСТОЯЩЕЙ конкуренцией: N воркеров стартуют одновременно и
// разбирают N джоб. Каждый обязан получить свою — ни одна джоба не должна
// достаться двоим. Падающий вариант: если бы захват не был атомарным (например,
// «сначала SELECT, потом отдельным запросом UPDATE»), двое успели бы выбрать
// одну строку и один и тот же заказ выполнился бы дважды.
//
// Пул создаётся СВОЙ, с MaxConns >= числа воркеров: с пулом по умолчанию
// горутины ждали бы соединение и выполнялись бы фактически по очереди — гонка
// не состоялась бы, а тест всё равно был бы зелёным.
func TestConcurrentClaimGivesDistinctJobs(t *testing.T) {
	const workers = 8

	ctx := context.Background()
	db := setup(t)
	for i := 0; i < workers; i++ {
		enqueue(t, db, "concurrent")
	}

	cfg, err := pgxpool.ParseConfig(testDSN())
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.MaxConns = workers
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	var wg sync.WaitGroup
	ids := make([]int64, workers)
	errs := make([]error, workers)
	start := make(chan struct{})

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // все стартуют разом, иначе гонки не будет
			j, err := Claim(ctx, pool, fmt.Sprintf("w%d", i), time.Minute)
			if err != nil {
				errs[i] = err
				return
			}
			if j != nil {
				ids[i] = j.ID
			}
		}(i)
	}
	close(start)
	wg.Wait()

	seen := make(map[int64]int, workers)
	claimed := 0
	for i, id := range ids {
		if errs[i] != nil {
			t.Fatalf("воркер w%d: %v", i, errs[i])
		}
		if id == 0 {
			continue
		}
		claimed++
		seen[id]++
		if seen[id] > 1 {
			t.Fatalf("джоба id=%d выдана %d раз — двойное исполнение", id, seen[id])
		}
	}
	if claimed != workers {
		t.Fatalf("разобрано %d джоб из %d — часть воркеров осталась без работы", claimed, workers)
	}
}

// Истёкшая аренда возвращает джобу в очередь, и её берёт другой воркер —
// со СЛЕДУЮЩЕЙ попыткой. Падающий вариант: если бы reclaim не работал, джоба
// осталась бы running навсегда (потеряна); если бы claim не фильтровал по
// state='queued' (выдавал бы и running-строки), второй воркер забрал бы её
// СРАЗУ, независимо от аренды (двойное исполнение) — claimSQL вообще не
// смотрит на leased_until, только на state.
func TestExpiredLeaseIsReclaimed(t *testing.T) {
	ctx := context.Background()
	db := setup(t)
	enqueue(t, db, "report")

	first, _ := Claim(ctx, db, "w1", 300*time.Millisecond)
	if first == nil {
		t.Fatal("первый claim пуст")
	}
	// Пока джоба state=running — она не достаётся никому: claimSQL фильтрует
	// кандидатов по state='queued' и вообще не смотрит на leased_until, поэтому
	// проверка ниже не про "аренда ещё жива" (это отдельно и явно проверяется
	// живым замером в scripts/lease-demo.sh — там второй воркер получает 0 джоб
	// даже при УЖЕ ИСТЁКШЕЙ аренде, пока reclaim не переведёт state обратно).
	if j, _ := Claim(ctx, db, "w2", time.Minute); j != nil {
		t.Fatalf("джоба выдана второму воркеру при живой аренде: id=%d", j.ID)
	}
	time.Sleep(400 * time.Millisecond)

	ids, err := ReclaimExpired(ctx, db)
	if err != nil || len(ids) != 1 {
		t.Fatalf("reclaim: ids=%v err=%v, ожидалась ровно 1 джоба", ids, err)
	}
	second, _ := Claim(ctx, db, "w2", time.Minute)
	if second == nil {
		t.Fatal("после reclaim джоба не досталась второму воркеру")
	}
	if second.Attempt != 2 {
		t.Fatalf("attempt = %d, ожидался 2 (первая попытка сгорела)", second.Attempt)
	}
}

// Heartbeat продлевает аренду, поэтому долгая джоба НЕ отбирается.
// Падающий вариант: без heartbeat ReclaimExpired вернул бы её в очередь.
func TestHeartbeatKeepsLease(t *testing.T) {
	ctx := context.Background()
	db := setup(t)
	enqueue(t, db, "long")

	j, _ := Claim(ctx, db, "w1", 300*time.Millisecond)
	if j == nil {
		t.Fatal("claim пуст")
	}
	time.Sleep(200 * time.Millisecond)
	ok, err := Heartbeat(ctx, db, j.ID, "w1", 300*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("heartbeat: ok=%v err=%v", ok, err)
	}
	time.Sleep(200 * time.Millisecond)

	ids, _ := ReclaimExpired(ctx, db)
	if len(ids) != 0 {
		t.Fatalf("джоба отобрана несмотря на heartbeat: %v", ids)
	}
}

// Heartbeat чужой джобы обязан вернуть false: после потери аренды воркер не
// должен «вернуть себе» джобу, которую уже выполняет другой.
func TestHeartbeatRejectsForeignOwner(t *testing.T) {
	ctx := context.Background()
	db := setup(t)
	enqueue(t, db, "x")

	j, _ := Claim(ctx, db, "w1", time.Minute)
	ok, err := Heartbeat(ctx, db, j.ID, "w2", time.Minute)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if ok {
		t.Fatal("чужой heartbeat принят — два воркера считали бы себя владельцами")
	}
}
