// Стенд Tarantool: spaces, хранимка top_products_by_category (контракт для
// Task 9, сценарий "цена выноса вычислений к данным") и сравнение движков
// memtx/vinyl. Три сценария (-scenario=spaces|storedproc|memtx-vinyl), общий
// источник истины — PostgreSQL (ORIGIN_DSN), общий датасет из dataset/.
//
// Абсолютную latency стенд не измеряет и не публикует (см. README, раздел
// «Границы метода»): Docker Desktop на Windows даёт непредсказуемый оверхед.
// Публикуемые числа — безразмерные отношения и объём памяти/диска.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tarantool/go-tarantool/v2"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ProductTuple — позиционная структура, зеркалящая products:format() в
// init.lua (id, sku, title, price_cents, category, views).
type ProductTuple struct {
	_msgpack   struct{} `msgpack:",asArray"` //nolint:unused
	ID         uint64
	SKU        string
	Title      string
	PriceCents uint64
	Category   string
	Views      uint64
}

// pgProduct — строка результата агрегирующего запроса к PostgreSQL: продукт
// + число просмотров (LEFT JOIN, товары без просмотров получают 0).
type pgProduct struct {
	ID         int64
	SKU        string
	Title      string
	PriceCents int64
	Category   string
	Views      int64
}

// loadProductsFromPG — единственная точка чтения исходных данных: продукты
// PostgreSQL с агрегированным числом просмотров. Категория лежит в
// attrs->>'category' (см. дизайн дампа, dataset/main.go).
func loadProductsFromPG(ctx context.Context, pool *pgxpool.Pool) ([]pgProduct, error) {
	rows, err := pool.Query(ctx, `
		SELECT p.id, p.sku, p.title, p.price_cents,
		       p.attrs->>'category' AS category,
		       COALESCE(v.views, 0) AS views
		FROM products p
		LEFT JOIN (
			SELECT product_id, count(*) AS views FROM views GROUP BY product_id
		) v ON v.product_id = p.id
		ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []pgProduct
	for rows.Next() {
		var p pgProduct
		if err := rows.Scan(&p.ID, &p.SKU, &p.Title, &p.PriceCents, &p.Category, &p.Views); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func connectTarantool(ctx context.Context, addr string) (*tarantool.Connection, error) {
	dialer := tarantool.NetDialer{Address: addr, User: "app", Password: "app"}
	opts := tarantool.Opts{Timeout: 10 * time.Second}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return tarantool.Connect(cctx, dialer, opts)
}

// loadIntoSpace — заливает продукты в указанный space пачками, асинхронно
// (Do() не блокирует, ставит запрос в очередь поверх одного TCP-соединения;
// Get() у всей пачки дожидается ответов). Это единственный практичный способ
// залить 200k кортежей без отдельной bulk-хранимки — сама контрактная
// хранимка (top_products_by_category) при этом не трогается.
func loadIntoSpace(conn *tarantool.Connection, space string, rows []pgProduct) error {
	const batch = 2000
	for i := 0; i < len(rows); i += batch {
		end := i + batch
		if end > len(rows) {
			end = len(rows)
		}
		futures := make([]*tarantool.Future, 0, end-i)
		for _, p := range rows[i:end] {
			tuple := []interface{}{
				uint64(p.ID), p.SKU, p.Title, uint64(p.PriceCents), p.Category, uint64(p.Views),
			}
			req := tarantool.NewReplaceRequest(space).Tuple(tuple)
			futures = append(futures, conn.Do(req))
		}
		for _, f := range futures {
			if _, err := f.Get(); err != nil {
				return fmt.Errorf("replace в %s: %w", space, err)
			}
		}
	}
	return nil
}

func spaceLen(conn *tarantool.Connection, space string) (uint64, error) {
	var out []uint64
	err := conn.Do(tarantool.NewEvalRequest(fmt.Sprintf("return box.space.%s:len()", space))).GetTyped(&out)
	if err != nil {
		return 0, err
	}
	if len(out) == 0 {
		return 0, fmt.Errorf("пустой ответ len() для %s", space)
	}
	return out[0], nil
}

// SlabInfo — числовые поля box.slab.info() (строковые *_ratio поля
// пропускаются декодером msgpack как неизвестные ключи).
type SlabInfo struct {
	ItemsUsed uint64 `msgpack:"items_used"`
	ItemsSize uint64 `msgpack:"items_size"`
	ArenaUsed uint64 `msgpack:"arena_used"`
	ArenaSize uint64 `msgpack:"arena_size"`
	QuotaUsed uint64 `msgpack:"quota_used"`
	QuotaSize uint64 `msgpack:"quota_size"`
}

func slabInfo(conn *tarantool.Connection) (SlabInfo, error) {
	var out []SlabInfo
	err := conn.Do(tarantool.NewEvalRequest("return box.slab.info()")).GetTyped(&out)
	if err != nil {
		return SlabInfo{}, err
	}
	if len(out) == 0 {
		return SlabInfo{}, fmt.Errorf("пустой ответ box.slab.info()")
	}
	return out[0], nil
}

type VinylDisk struct {
	Data          uint64 `msgpack:"data"`
	DataCompacted uint64 `msgpack:"data_compacted"`
	Index         uint64 `msgpack:"index"`
}

type VinylMemory struct {
	Tuple       uint64 `msgpack:"tuple"`
	Level0      uint64 `msgpack:"level0"`
	TupleCache  uint64 `msgpack:"tuple_cache"`
	Tx          uint64 `msgpack:"tx"`
	PageIndex   uint64 `msgpack:"page_index"`
	BloomFilter uint64 `msgpack:"bloom_filter"`
}

type VinylStat struct {
	Disk   VinylDisk   `msgpack:"disk"`
	Memory VinylMemory `msgpack:"memory"`
}

// waitMemtxSlabSettled — box.slab.info() у memtx освобождает память
// drop()-нутого space АСИНХРОННО (фоновая GC-фибра тюплов), не в момент
// вызова drop(). Живой прогон (docker exec, net.box, опрос каждые 100мс)
// показал: сразу после drop() items_used падает лишь частично (14 652 184
// вместо ожидаемого ~46 000), настоящий baseline достигается уже к первому
// опросу спустя 100мс и дальше не меняется 3 секунды подряд — т.е. окно
// нестабильности короткое, но НЕНУЛЕВОЕ, и без ожидания повторный вызов
// сценария на том же инстансе меряет дельту от "ещё не освобождённого"
// baseline и молча занижает её (воспроизведено вживую: 0.700 → 0.153 →
// 0.135 → 0.097 на четырёх прогонах подряд без ожидания). Опрашивает
// items_used, считает состояние устоявшимся при settleRounds одинаковых
// показаний подряд; возвращает финальный SlabInfo, число опросов и признак
// того, уложились ли в budget (false — не устоялось, вызывающий код обязан
// напечатать предупреждение, а не молча продолжить).
func waitMemtxSlabSettled(conn *tarantool.Connection, budget time.Duration) (SlabInfo, int, bool) {
	const poll = 100 * time.Millisecond
	const settleRounds = 3

	last, err := slabInfo(conn)
	if err != nil {
		log.Fatalf("box.slab.info() при ожидании стабилизации: %v", err)
	}
	polls := 1
	stable := 0
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		time.Sleep(poll)
		cur, err := slabInfo(conn)
		if err != nil {
			log.Fatalf("box.slab.info() при ожидании стабилизации: %v", err)
		}
		polls++
		if cur.ItemsUsed == last.ItemsUsed {
			stable++
			if stable >= settleRounds {
				return cur, polls, true
			}
		} else {
			stable = 0
		}
		last = cur
	}
	return last, polls, false
}

// vinylMemoryTotal — вся резидентная RAM движка vinyl: Tuple (несброшенные
// тюплы в памяти) + Level0 (L0-буфер, оба — только ДО дампа) + PageIndex +
// BloomFilter + TupleCache (служебные структуры на смонтированные с диска
// run'ы — резидентны В ЛЮБОЙ момент, в т.ч. ПОСЛЕ дампа). Прежняя версия
// считала только Tuple+Level0 и поэтому получала "RAM после дампа = 0" —
// фактически неверно: живой прогон продампленного products_vinyl снял
// page_index=107996 bloom_filter=180363 байт, реально резидентных в RAM
// (см. task-3-report.md, «Фиксы по ревью»).
func vinylMemoryTotal(m VinylMemory) uint64 {
	return m.Tuple + m.Level0 + m.PageIndex + m.BloomFilter + m.TupleCache
}

func vinylStat(conn *tarantool.Connection) (VinylStat, error) {
	var out []VinylStat
	err := conn.Do(tarantool.NewEvalRequest("return box.stat.vinyl()")).GetTyped(&out)
	if err != nil {
		return VinylStat{}, err
	}
	if len(out) == 0 {
		return VinylStat{}, fmt.Errorf("пустой ответ box.stat.vinyl()")
	}
	return out[0], nil
}

func mustEval(conn *tarantool.Connection, expr string) {
	if _, err := conn.Do(tarantool.NewEvalRequest(expr)).Get(); err != nil {
		log.Fatalf("eval %q: %v", expr, err)
	}
}

func main() {
	var (
		scenario = flag.String("scenario", "", "spaces|storedproc|memtx-vinyl")
		dsn      = flag.String("dsn", os.Getenv("ORIGIN_DSN"), "DSN PostgreSQL")
		addr     = flag.String("tarantool-addr", envOr("TARANTOOL_ADDR", "127.0.0.1:3301"), "адрес Tarantool")
	)
	flag.Parse()

	if *dsn == "" {
		log.Fatal("нужен -dsn или ORIGIN_DSN")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	conn, err := connectTarantool(ctx, *addr)
	if err != nil {
		log.Fatalf("tarantool connect: %v", err)
	}
	defer conn.Close()

	switch *scenario {
	case "spaces":
		scenarioSpaces(ctx, pool, conn)
	case "storedproc":
		scenarioStoredProc(ctx, pool, conn)
	case "memtx-vinyl":
		scenarioMemtxVinyl(ctx, pool, conn)
	default:
		log.Fatalf("неизвестный -scenario=%q (ожидается spaces|storedproc|memtx-vinyl)", *scenario)
	}
	fmt.Println("OK")
}

// scenarioSpaces — читает products из PG (с агрегированными просмотрами),
// заливает в space 'products', проверяет число кортежей и чтение по
// первичному и вторичному индексам. Печатает box.slab.info() — публикуемое
// число: фактическая RAM на 200k кортежей.
func scenarioSpaces(ctx context.Context, pool *pgxpool.Pool, conn *tarantool.Connection) {
	rows, err := loadProductsFromPG(ctx, pool)
	if err != nil {
		log.Fatalf("чтение из PG: %v", err)
	}
	var pgCount int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM products`).Scan(&pgCount); err != nil {
		log.Fatalf("count(*) products: %v", err)
	}
	fmt.Printf("spaces: прочитано из PG products=%d (count(*)=%d)\n", len(rows), pgCount)

	// Идемпотентность: пустой space перед загрузкой, чтобы повторный
	// прогон не оставлял мусор от предыдущей попытки с другим набором id.
	mustEval(conn, "box.space.products:truncate()")

	if err := loadIntoSpace(conn, "products", rows); err != nil {
		log.Fatalf("загрузка в space products: %v", err)
	}

	spaceCount, err := spaceLen(conn, "products")
	if err != nil {
		log.Fatalf("box.space.products:len(): %v", err)
	}
	fmt.Printf("spaces: в space products кортежей=%d\n", spaceCount)

	// Число кортежей в space обязано совпасть с числом строк в PG.
	if int64(spaceCount) != pgCount {
		log.Fatalf("АССЕРТ: в space %d кортежей, в PG %d строк", spaceCount, pgCount)
	}

	// Чтение по первичному индексу.
	probe := rows[0]
	var byPK []ProductTuple
	err = conn.Do(tarantool.NewSelectRequest("products").
		Index("primary").
		Iterator(tarantool.IterEq).
		Key([]interface{}{uint64(probe.ID)}),
	).GetTyped(&byPK)
	if err != nil {
		log.Fatalf("select по primary: %v", err)
	}
	if len(byPK) != 1 || byPK[0].ID != uint64(probe.ID) || byPK[0].SKU != probe.SKU {
		log.Fatalf("АССЕРТ: чтение по primary для id=%d вернуло %+v, ожидался sku=%s", probe.ID, byPK, probe.SKU)
	}
	fmt.Printf("spaces: чтение по primary(id=%d) ок, sku=%s\n", probe.ID, byPK[0].SKU)

	// Чтение по вторичному индексу (category, views).
	const probeCategory = "tools"
	var byCategory []ProductTuple
	err = conn.Do(tarantool.NewSelectRequest("products").
		Index("category").
		Iterator(tarantool.IterEq).
		Key([]interface{}{probeCategory}).
		Limit(5),
	).GetTyped(&byCategory)
	if err != nil {
		log.Fatalf("select по category: %v", err)
	}
	if len(byCategory) == 0 {
		log.Fatalf("АССЕРТ: select по вторичному индексу category=%q вернул 0 строк", probeCategory)
	}
	for _, t := range byCategory {
		if t.Category != probeCategory {
			log.Fatalf("АССЕРТ: select по category=%q вернул кортеж с category=%q (id=%d)", probeCategory, t.Category, t.ID)
		}
	}
	fmt.Printf("spaces: чтение по вторичному индексу category=%q ок, получено %d кортежей (limit 5)\n",
		probeCategory, len(byCategory))

	// Публикуемое число: фактическая RAM на 200k кортежей.
	slab, err := slabInfo(conn)
	if err != nil {
		log.Fatalf("box.slab.info(): %v", err)
	}
	fmt.Printf("spaces: box.slab.info() items_used=%d items_size=%d arena_used=%d arena_size=%d quota_used=%d quota_size=%d\n",
		slab.ItemsUsed, slab.ItemsSize, slab.ArenaUsed, slab.ArenaSize, slab.QuotaUsed, slab.QuotaSize)
	fmt.Printf("spaces: RAM на %d кортежей ~= %.2f МБ (items_used), %.2f МБ (arena_used)\n",
		spaceCount, float64(slab.ItemsUsed)/1024/1024, float64(slab.ArenaUsed)/1024/1024)
}

// TopEntry — элемент результата top_products_by_category (см. init.lua):
// {id=.., title=.., views=..}.
type TopEntry struct {
	ID    uint64 `msgpack:"id"`
	Title string `msgpack:"title"`
	Views uint64 `msgpack:"views"`
}

// callStoredProcTop — вызывает контрактную хранимку top_products_by_category.
func callStoredProcTop(conn *tarantool.Connection, category string, limit int) []TopEntry {
	var procResult [][]TopEntry
	err := conn.Do(tarantool.NewCallRequest("top_products_by_category").
		Args([]interface{}{category, limit}),
	).GetTyped(&procResult)
	if err != nil {
		log.Fatalf("call top_products_by_category(%q, %d): %v", category, limit, err)
	}
	if len(procResult) == 0 {
		log.Fatalf("АССЕРТ: пустой ответ CALL top_products_by_category(%q, %d)", category, limit)
	}
	return procResult[0]
}

// clientTopByCategory — наивный клиентский вариант: вся категория уезжает по
// сети, топ считается в Go (ОДИН SELECT, не N round-trip: разница с хранимкой
// не в числе обращений, а в объёме ответа и месте отбора — именно с ним
// хранимка сравнивается в сценарии 2 бенчмарка, Task 9).
//
// Тай-брейк для равных views — ЯВНО по id DESCENDING, а не "как получится".
// Хранимка идёт по вторичному индексу (category, views) итератором REQ
// (обратный порядок); внутри равных views неуникальный индекс использует PK
// как неявный тай-брейк, и REQ разворачивает ВЕСЬ обход целиком, включая
// порядок PK для равных ключей, — т.е. для связки views REQ отдаёт кортежи
// по PK DESCENDING (проверено живьём: category=tools, limit=9, id=84 и
// id=85 оба views=93 — хранимка на этой границе возвращает id=85). Прежняя
// версия сортировала клиента только по Views (sort.SliceStable без явного
// тай-брейка) — для равных views стабильная сортировка сохраняла порядок
// ФОРВАРД-обхода индекса (PK ASCENDING), т.е. противоположный хранимке.
// Для зонда category=tools/limit=5 в датасете этого прогона совпадающих
// views в топ-5 нет, поэтому старая версия проходила случайно — на
// category=tools/limit=9 (граница с id=84/85) она даст РАСХОЖДЕНИЕ
// МНОЖЕСТВА (не только порядка): старый клиент возьмёт id=84, хранимка —
// id=85. Явный тай-брейк по PK DESC в клиенте убирает это расхождение по
// построению, а не только "визуально" переупорядочивает.
// Возвращает топ длины limit и полное число товаров в категории (сколько
// кортежей реально уехало по сети клиенту — публикуемое число сценария).
func clientTopByCategory(conn *tarantool.Connection, category string, limit int) ([]TopEntry, int) {
	var wholeCategory []ProductTuple
	err := conn.Do(tarantool.NewSelectRequest("products").
		Index("category").
		Iterator(tarantool.IterEq).
		Key([]interface{}{category}).
		Limit(0xFFFFFFFF),
	).GetTyped(&wholeCategory)
	if err != nil {
		log.Fatalf("select всей категории %q: %v", category, err)
	}
	sort.SliceStable(wholeCategory, func(i, j int) bool {
		if wholeCategory[i].Views != wholeCategory[j].Views {
			return wholeCategory[i].Views > wholeCategory[j].Views
		}
		return wholeCategory[i].ID > wholeCategory[j].ID
	})
	if len(wholeCategory) < limit {
		log.Fatalf("АССЕРТ: категория %q содержит %d товаров, меньше limit=%d", category, len(wholeCategory), limit)
	}
	clientTop := make([]TopEntry, 0, limit)
	for _, t := range wholeCategory[:limit] {
		clientTop = append(clientTop, TopEntry{ID: t.ID, Title: t.Title, Views: t.Views})
	}
	return clientTop, len(wholeCategory)
}

// scenarioStoredProc — сверяет контрактную хранимку top_products_by_category
// с наивным клиентским расчётом на двух зондах: основной (контракт Task 9,
// без ничьих в топе — числа стабильны) и граничный (намеренно выбранная
// категория/limit с реальной ничьей на границе топа — регрессионный тест
// на устойчивость сравнения к ничьим, см. clientTopByCategory).
func scenarioStoredProc(ctx context.Context, pool *pgxpool.Pool, conn *tarantool.Connection) {
	const category = "tools"
	const limit = 5

	procTop := callStoredProcTop(conn, category, limit)
	clientTop, wholeCategoryLen := clientTopByCategory(conn, category, limit)

	fmt.Printf("storedproc: category=%q limit=%d\n", category, limit)
	fmt.Println("storedproc: топ из хранимки:")
	for i, e := range procTop {
		fmt.Printf("  #%d id=%d title=%q views=%d\n", i+1, e.ID, e.Title, e.Views)
	}
	fmt.Println("storedproc: топ, посчитанный в Go после вычитывания всей категории:")
	for i, e := range clientTop {
		fmt.Printf("  #%d id=%d title=%q views=%d\n", i+1, e.ID, e.Title, e.Views)
	}

	// Хранимка и клиентский расчёт обязаны дать одинаковый топ — иначе
	// сравнение в сценарии 2 бенчмарка сравнивало бы разные вещи.
	if !reflect.DeepEqual(procTop, clientTop) {
		log.Fatalf("АССЕРТ: топ из хранимки != топ из клиента:\n proc=%v\n client=%v", procTop, clientTop)
	}
	fmt.Println("storedproc: топ хранимки совпал с топом клиентского расчёта")

	// Граничный регрессионный тест: category=tools, limit=9 — на этой
	// границе реально есть ничья (id=84 и id=85, оба views=93, живой прогон
	// PG). Без явного тай-брейка по PK в клиенте это расхождение по МНОЖЕСТВУ
	// (не только порядку) — старый клиент брал id=84, хранимка — id=85.
	const tieCategory = "tools"
	const tieLimit = 9
	tieProcTop := callStoredProcTop(conn, tieCategory, tieLimit)
	tieClientTop, _ := clientTopByCategory(conn, tieCategory, tieLimit)
	fmt.Printf("storedproc: граничный тест ничьих category=%q limit=%d (известная ничья views=93 на id=84/85)\n",
		tieCategory, tieLimit)
	fmt.Println("storedproc: топ из хранимки (граничный):")
	for i, e := range tieProcTop {
		fmt.Printf("  #%d id=%d title=%q views=%d\n", i+1, e.ID, e.Title, e.Views)
	}
	fmt.Println("storedproc: топ клиента (граничный, с тай-брейком по PK DESC):")
	for i, e := range tieClientTop {
		fmt.Printf("  #%d id=%d title=%q views=%d\n", i+1, e.ID, e.Title, e.Views)
	}
	if !reflect.DeepEqual(tieProcTop, tieClientTop) {
		log.Fatalf("АССЕРТ: граничный тест ничьих — топ хранимки != топ клиента:\n proc=%v\n client=%v", tieProcTop, tieClientTop)
	}
	fmt.Println("storedproc: граничный тест ничьих — топ хранимки совпал с топом клиента (тай-брейк по PK работает одинаково в обеих ветках)")

	// Публикуемое число: сколько кортежей уехало по сети.
	clientTuples := wholeCategoryLen
	procTuples := len(procTop)
	fmt.Printf("storedproc: кортежей по сети (клиент, вся категория)=%d, кортежей по сети (хранимка)=%d\n",
		clientTuples, procTuples)
	fmt.Printf("storedproc: ratio_client_over_storedproc = %d/%d = %.1f\n",
		clientTuples, procTuples, float64(clientTuples)/float64(procTuples))
}

// scenarioMemtxVinyl — грузит ОДИНАКОВЫЙ набор в два новых space (memtx и
// vinyl), сравнивает RAM и факт наличия данных на диске. Время доступа
// намеренно не измеряется (см. README, «Границы метода»).
func scenarioMemtxVinyl(ctx context.Context, pool *pgxpool.Pool, conn *tarantool.Connection) {
	rows, err := loadProductsFromPG(ctx, pool)
	if err != nil {
		log.Fatalf("чтение из PG: %v", err)
	}
	fmt.Printf("memtx-vinyl: набор для сравнения — products=%d (тот же датасет, что в spaces)\n", len(rows))

	const schemaLua = `
local s = box.schema.space.create('%s', { engine = '%s', if_not_exists = true })
s:format({
    { name = 'id',          type = 'unsigned' },
    { name = 'sku',         type = 'string'   },
    { name = 'title',       type = 'string'   },
    { name = 'price_cents', type = 'unsigned' },
    { name = 'category',    type = 'string'   },
    { name = 'views',       type = 'unsigned' },
})
s:create_index('primary', { parts = { 'id' }, if_not_exists = true })
return true`

	// Дроп-и-пересоздание — идемпотентность повторного прогона и чистая
	// база для delta-измерения памяти (иначе повторный запуск занижал бы
	// прирост, если space уже содержит данные от прошлого прогона).
	mustEval(conn, "if box.space.products_memtx then box.space.products_memtx:drop() end return true")
	mustEval(conn, "if box.space.products_vinyl then box.space.products_vinyl:drop() end return true")

	// memtx освобождает память дропнутого space АСИНХРОННО (см.
	// waitMemtxSlabSettled) — без ожидания baseline "до загрузки" мерил бы
	// ещё не освободившуюся память предыдущего прогона и делал бы дельту
	// невоспроизводимой между повторными запусками сценария на одном и том
	// же инстансе (живой прогон без этого ожидания: 0.700 → 0.153 → 0.135 →
	// 0.097 на четырёх прогонах подряд).
	slabSettled, polls, settled := waitMemtxSlabSettled(conn, 5*time.Second)
	fmt.Printf("memtx-vinyl: box.slab.info() устоялся за %d опрос(ов), items_used=%d байт (baseline после drop)\n",
		polls, slabSettled.ItemsUsed)
	if !settled {
		fmt.Printf("memtx-vinyl: ПРЕДУПРЕЖДЕНИЕ — box.slab.info() НЕ стабилизировался за budget, RAM delta ниже может быть занижена (неосвобождённая память предыдущего прогона всё ещё не собрана GC-фиброй)\n")
	}

	mustEval(conn, fmt.Sprintf(schemaLua, "products_memtx", "memtx"))
	mustEval(conn, fmt.Sprintf(schemaLua, "products_vinyl", "vinyl"))

	slabBefore, err := slabInfo(conn)
	if err != nil {
		log.Fatalf("box.slab.info() до загрузки memtx: %v", err)
	}
	if err := loadIntoSpace(conn, "products_memtx", rows); err != nil {
		log.Fatalf("загрузка products_memtx: %v", err)
	}
	memtxLen, err := spaceLen(conn, "products_memtx")
	if err != nil {
		log.Fatalf("len products_memtx: %v", err)
	}
	slabAfter, err := slabInfo(conn)
	if err != nil {
		log.Fatalf("box.slab.info() после загрузки memtx: %v", err)
	}
	if memtxLen != uint64(len(rows)) {
		log.Fatalf("АССЕРТ: products_memtx содержит %d кортежей, ожидалось %d", memtxLen, len(rows))
	}
	memtxItemsUsedDelta := slabAfter.ItemsUsed - slabBefore.ItemsUsed
	memtxArenaUsedDelta := slabAfter.ArenaUsed - slabBefore.ArenaUsed
	fmt.Printf("memtx-vinyl: products_memtx кортежей=%d, RAM delta items_used=%d байт (%.2f МБ), arena_used=%d байт (%.2f МБ)\n",
		memtxLen, memtxItemsUsedDelta, float64(memtxItemsUsedDelta)/1024/1024,
		memtxArenaUsedDelta, float64(memtxArenaUsedDelta)/1024/1024)

	if err := loadIntoSpace(conn, "products_vinyl", rows); err != nil {
		log.Fatalf("загрузка products_vinyl: %v", err)
	}
	vinylLen, err := spaceLen(conn, "products_vinyl")
	if err != nil {
		log.Fatalf("len products_vinyl: %v", err)
	}
	if vinylLen != uint64(len(rows)) {
		log.Fatalf("АССЕРТ: products_vinyl содержит %d кортежей, ожидалось %d", vinylLen, len(rows))
	}

	// Замер ДО box.snapshot(): все только что записанные тюплы ещё лежат в
	// L0-буфере vinyl в RAM (движок ещё не сбросил их на диск сам —
	// регулятор дампит по порогу vinyl_memory/по таймеру, не сразу после
	// записи). Это ПИКОВАЯ, ПЕРЕХОДНАЯ величина — сопоставимая по порядку с
	// memtx, но недолговечная (в проде дамп случится по vinyl_memory/
	// checkpoint_interval и не будет держаться вечно). Не заголовочное число.
	vstatBefore, err := vinylStat(conn)
	if err != nil {
		log.Fatalf("box.stat.vinyl() до snapshot: %v", err)
	}
	vinylRAMPeak := vinylMemoryTotal(vstatBefore.Memory)
	fmt.Printf("memtx-vinyl: products_vinyl кортежей=%d, ПИК RAM до box.snapshot() tuple=%d level0=%d page_index=%d bloom_filter=%d tuple_cache=%d (итого=%d байт, %.2f МБ)\n",
		vinylLen, vstatBefore.Memory.Tuple, vstatBefore.Memory.Level0, vstatBefore.Memory.PageIndex,
		vstatBefore.Memory.BloomFilter, vstatBefore.Memory.TupleCache, vinylRAMPeak, float64(vinylRAMPeak)/1024/1024)
	if vinylRAMPeak == 0 {
		log.Fatalf("АССЕРТ: vinyl RAM (пик, до snapshot) равен 0 — нечего сравнивать с memtx")
	}

	// Форсируем дамп vinyl L0-буфера на диск (в проде это происходит по
	// порогу vinyl_memory или по checkpoint-таймеру, здесь — сразу, чтобы
	// увидеть фактический disk-footprint и устойчивое состояние RAM в рамках
	// одного прогона, а не ждать регулятор).
	mustEval(conn, "box.snapshot()")

	vstatAfter, err := vinylStat(conn)
	if err != nil {
		log.Fatalf("box.stat.vinyl() после snapshot: %v", err)
	}
	// УСТОЙЧИВОЕ состояние: tuple и level0 уходят в 0 (L0-буфер сброшен), но
	// page_index/bloom_filter/tuple_cache — служебные структуры на сами
	// смонтированные run'ы на диске — остаются РЕЗИДЕНТНЫМИ в RAM постоянно,
	// не только сразу после записи. Прежняя версия считала здесь только
	// tuple+level0 и получала 0 — фактически неверно (см. task-3-report.md,
	// «Фиксы по ревью», находка 2): это и есть заголовочное число серии —
	// честная демонстрация экономии RAM у дискового движка.
	vinylRAMSteady := vinylMemoryTotal(vstatAfter.Memory)
	vinylDisk := vstatAfter.Disk.Data + vstatAfter.Disk.Index
	fmt.Printf("memtx-vinyl: products_vinyl УСТОЙЧИВОЕ RAM после box.snapshot() tuple=%d level0=%d page_index=%d bloom_filter=%d tuple_cache=%d (итого=%d байт, %.2f МБ) — L0 сброшен, служебные структуры резидентны\n",
		vstatAfter.Memory.Tuple, vstatAfter.Memory.Level0, vstatAfter.Memory.PageIndex,
		vstatAfter.Memory.BloomFilter, vstatAfter.Memory.TupleCache, vinylRAMSteady, float64(vinylRAMSteady)/1024/1024)
	fmt.Printf("memtx-vinyl: products_vinyl box.stat.vinyl().disk data=%d data_compacted=%d index=%d (на диске=%d байт, %.2f МБ)\n",
		vstatAfter.Disk.Data, vstatAfter.Disk.DataCompacted, vstatAfter.Disk.Index, vinylDisk, float64(vinylDisk)/1024/1024)

	// На диске у vinyl должны реально появиться байты — это демонстрация
	// факта "vinyl держит данные на диске", а не абсолютное число для
	// сравнения между СУБД.
	if vinylDisk == 0 {
		log.Fatalf("АССЕРТ: у vinyl после box.snapshot() на диске 0 байт — дамп не произошёл")
	}
	// АССЕРТ на находку 2: устойчивая RAM vinyl обязана быть НЕНУЛЕВОЙ — это
	// заголовочное число серии, скрытый ноль здесь означал бы, что структуры
	// на диске (page_index/bloom_filter) не строятся, а это неправда.
	if vinylRAMSteady == 0 {
		log.Fatalf("АССЕРТ: устойчивая RAM vinyl (page_index+bloom_filter+tuple_cache) после snapshot равна 0")
	}

	// Оба отношения — на items_used (единственная метрика memtx, использованная
	// как числитель в обеих фазах — симметрично, без примеси "рыхлого"
	// arena_used, см. task-3-report.md, находка 5).
	fmt.Printf("memtx-vinyl: ratio_memtx_ram_over_vinyl_ram_peak (items_used/пик, ДО snapshot) = %d/%d = %.3f\n",
		memtxItemsUsedDelta, vinylRAMPeak, float64(memtxItemsUsedDelta)/float64(vinylRAMPeak))
	fmt.Printf("memtx-vinyl: ratio_memtx_ram_over_vinyl_ram_steady (items_used/устойчивое, ПОСЛЕ snapshot) = %d/%d = %.1fx\n",
		memtxItemsUsedDelta, vinylRAMSteady, float64(memtxItemsUsedDelta)/float64(vinylRAMSteady))
	fmt.Printf("memtx-vinyl: справочно, arena_used (более рыхлая метрика memtx, не используется в ratio) delta=%d байт (%.2f МБ)\n",
		memtxArenaUsedDelta, float64(memtxArenaUsedDelta)/1024/1024)
	fmt.Printf("memtx-vinyl: memtx на диске в норме держит 0 байт данных между checkpoint'ами (persist только в snapshot), vinyl держит %d байт (%.2f МБ) постоянно — LSM-хранилище на диске, не только в RAM\n",
		vinylDisk, float64(vinylDisk)/1024/1024)
}
