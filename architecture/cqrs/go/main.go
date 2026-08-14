// Стенд к статье «CQRS на практике» — https://khorost.tech/architecture/cqrs-in-practice/
//
// Что показывает (домен — заказы):
//   * write-модель — append-only лог событий заказов (order_events), команды только
//     добавляют события; текущее состояние получается сверткой лога;
//   * async-проекция — отдельный проектор строит денормализованную read-таблицу
//     orders_read (заточена под запрос «список заказов пользователя»), применяет лог
//     идемпотентно (UPSERT) и двигает чекпоинт позиции АТОМАРНО с проекцией;
//   * read-your-writes — сначала ПОЛОМКА (сразу после записи читаем из проекции, а она
//     отстаёт → «не вижу свою запись»), затем ДВА приёма обхода: чтение собственной
//     свежей записи с write-стороны и ожидание проекции по токену-позиции;
//   * лаг проекции как метрика — хвост лога минус чекпоинт проектора;
//   * blue-green rebuild — пересобираем orders_read_v2 полным реплеем без остановки
//     чтения и атомарно переключаем таблицы местами.
//
// Инварианты проверяются ассертами (log.Fatalf при расхождении): проекция после
// догона == свёртке write-стороны; приём read-your-writes возвращает свежие данные;
// проекция после rebuild == онлайн-состоянию.
//
// Запуск:
//
//	docker compose -f cqrs/compose/compose.yml up -d   # Postgres 16 на localhost:5453
//	cd cqrs/go && go run .
//	docker compose -f cqrs/compose/compose.yml down -v  # снести
//
// Границы со стендом event-sourcing/: здесь write-сторона намеренно простая (лог как
// источник проекций, без event store со снапшотами) — фокус на READ-стороне CQRS.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://cqrs:cqrs@localhost:5453/cqrs"
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("подключение: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("нет связи с Postgres (%s): %v\nПоднимите стенд: docker compose -f cqrs/compose/compose.yml up -d", dsn, err)
	}

	must(setupWriteModel(ctx, pool), "setup write-модели")
	must(setupReadModel(ctx, pool), "setup read-модели")

	// --- Затравка: у Bob два заказа, у Alice один; проектор их уже применил ---
	fmt.Println("=== (0) затравка: пишем команды и один раз прогоняем проектор ===")
	bob1, _, err := createOrder(ctx, pool, "bob", 100)
	must(err, "createOrder bob1")
	_, _, err = createOrder(ctx, pool, "bob", 250)
	must(err, "createOrder bob2")
	_, err = payOrder(ctx, pool, bob1)
	must(err, "payOrder bob1")
	_, _, err = createOrder(ctx, pool, "alice", 300)
	must(err, "createOrder alice1")

	n, err := projectOnce(ctx, pool)
	must(err, "projectOnce затравка")
	lag, _ := projectionLag(ctx, pool)
	fmt.Printf("применено событий: %d, лаг проекции: %d\n", n, lag)
	assertEq("лаг после догона затравки", lag, 0)

	// --- (1) read-your-writes: ПОЛОМКА ---
	// Alice делает новый заказ и СРАЗУ читает свой список из проекции. Проектор ещё не
	// вызывали → проекция отстаёт → своей только что созданной записи Alice не видит.
	fmt.Println("\n=== (1) read-your-writes: поломка (читаем из отстающей проекции) ===")
	aliceNew, token, err := createOrder(ctx, pool, "alice", 999)
	must(err, "createOrder aliceNew")
	lag, _ = projectionLag(ctx, pool)
	fmt.Printf("создан заказ #%d (token seq=%d), лаг проекции теперь: %d\n", aliceNew, token, lag)

	fromProj, err := readUserOrdersFromProjection(ctx, pool, "alice")
	must(err, "read alice из проекции")
	fmt.Printf("проекция вернула Alice %d заказ(ов): %v\n", len(fromProj), orderIDs(fromProj))
	if containsOrder(fromProj, aliceNew) {
		log.Fatalf("АНСЕРТ: ожидали поломку read-your-writes, но проекция уже содержит #%d", aliceNew)
	}
	fmt.Printf("read-your-writes НАРУШЕН: заказа #%d в проекции нет (лаг=%d)\n", aliceNew, lag)

	// --- (2) read-your-writes: ПРИЁМ №1 — собственную свежую запись читаем с write-стороны ---
	fmt.Println("\n=== (2) приём №1: свои свежие заказы читаем с write-стороны (свёртка лога) ===")
	fromWrite, err := foldUserOrdersWriteSide(ctx, pool, "alice")
	must(err, "fold alice write-side")
	fmt.Printf("write-сторона вернула Alice %d заказ(ов): %v\n", len(fromWrite), orderIDs(fromWrite))
	if !containsOrder(fromWrite, aliceNew) {
		log.Fatalf("АНСЕРТ: приём read-your-writes должен видеть свежую запись #%d, но её нет", aliceNew)
	}
	fmt.Printf("read-your-writes ВОССТАНОВЛЕН: заказ #%d виден сразу (write-сторона без лага)\n", aliceNew)

	// «Чужие» заказы при этом спокойно читаются из проекции (быстро, денормализовано).
	bobProj, err := readUserOrdersFromProjection(ctx, pool, "bob")
	must(err, "read bob из проекции")
	fmt.Printf("заказы Bob (чужие для Alice) — из проекции: %v\n", orderIDs(bobProj))

	// --- (3) read-your-writes: ПРИЁМ №2 — ждём проекцию по токену-позиции ---
	fmt.Println("\n=== (3) приём №2: ждём, пока проекция догонит токен, и читаем из неё ===")
	must(waitForProjection(ctx, pool, token), "waitForProjection")
	lag, _ = projectionLag(ctx, pool)
	afterWait, err := readUserOrdersFromProjection(ctx, pool, "alice")
	must(err, "read alice после ожидания")
	fmt.Printf("после догона (лаг=%d) проекция вернула Alice: %v\n", lag, orderIDs(afterWait))
	if !containsOrder(afterWait, aliceNew) {
		log.Fatalf("АНСЕРТ: после ожидания по токену проекция обязана содержать #%d", aliceNew)
	}

	// --- (4) проекция после догона == свёртке write-стороны ---
	fmt.Println("\n=== (4) инвариант: догнавшая проекция == свёртка write-стороны ===")
	pAlice, _ := readUserOrdersFromProjection(ctx, pool, "alice")
	wAlice, _ := foldUserOrdersWriteSide(ctx, pool, "alice")
	assertOrdersEqual("проекция(alice) == свёртка(alice)", pAlice, wAlice)
	pBob, _ := readUserOrdersFromProjection(ctx, pool, "bob")
	wBob, _ := foldUserOrdersWriteSide(ctx, pool, "bob")
	assertOrdersEqual("проекция(bob) == свёртка(bob)", pBob, wBob)
	fmt.Println("совпадает: проекция догнала лог и равна авторитетной свёртке")

	// --- (5) blue-green rebuild ---
	// Снимок «онлайн»-состояния до пересборки. Пересобираем orders_read_v2 полным
	// реплеем и атомарно переключаем. Новых команд между снимком и switch нет, поэтому
	// после переключения проекция обязана совпасть и со свёрткой, и со снимком «до».
	fmt.Println("\n=== (5) blue-green rebuild: реплей в orders_read_v2 + атомарный switch ===")
	onlineAliceBefore, _ := readUserOrdersFromProjection(ctx, pool, "alice")
	replayed, err := rebuildReadModel(ctx, pool)
	must(err, "rebuildReadModel")
	lag, _ = projectionLag(ctx, pool)
	fmt.Printf("реплейнуто событий в v2: %d, лаг после switch: %d\n", replayed, lag)
	assertEq("лаг после rebuild", lag, 0)

	rebuiltAlice, _ := readUserOrdersFromProjection(ctx, pool, "alice")
	assertOrdersEqual("проекция после rebuild == онлайн до rebuild", rebuiltAlice, onlineAliceBefore)
	wAlice2, _ := foldUserOrdersWriteSide(ctx, pool, "alice")
	assertOrdersEqual("проекция после rebuild == свёртка write-стороны", rebuiltAlice, wAlice2)
	fmt.Println("совпадает: пересобранная проекция идентична онлайн-состоянию и свёртке")

	fmt.Println("\nВСЕ АНСЕРТЫ ПРОЙДЕНЫ ✔")
}

// --- служебное ---

func must(err error, what string) {
	if err != nil {
		log.Fatalf("%s: %v", what, err)
	}
}

func assertEq(label string, got, want int64) {
	if got != want {
		log.Fatalf("АНСЕРТ [%s]: получили %d, ожидали %d", label, got, want)
	}
}

// assertOrdersEqual сравнивает списки заказов по бизнес-полям (order_id/user_id/
// status/amount). updated_seq не сравниваем: это техническая позиция применения.
func assertOrdersEqual(label string, got, want []Order) {
	if len(got) != len(want) {
		log.Fatalf("АНСЕРТ [%s]: разное число заказов: %d vs %d\n  got=%v\n  want=%v",
			label, len(got), len(want), orderIDs(got), orderIDs(want))
	}
	for i := range got {
		g, w := got[i], want[i]
		if g.OrderID != w.OrderID || g.UserID != w.UserID || g.Status != w.Status || g.Amount != w.Amount {
			log.Fatalf("АНСЕРТ [%s]: расхождение в позиции %d: %+v vs %+v", label, i, g, w)
		}
	}
}

func containsOrder(os []Order, id int64) bool {
	for _, o := range os {
		if o.OrderID == id {
			return true
		}
	}
	return false
}

func orderIDs(os []Order) []string {
	out := make([]string, 0, len(os))
	for _, o := range os {
		out = append(out, fmt.Sprintf("#%d(%s,%d)", o.OrderID, o.Status, o.Amount))
	}
	return out
}
