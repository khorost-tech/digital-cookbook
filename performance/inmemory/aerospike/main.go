// Стенд Aerospike: гибридная память RAM/flash. Два namespace на одном
// сервере — "ram" (storage-engine memory, и индекс, и данные в RAM) и
// "flash" (storage-engine device, данные на файле-устройстве). Первичный
// индекс в Aerospike ВСЕГДА резидентен в RAM независимо от storage-engine —
// это архитектурное свойство сервера, не опция конфига; namespace "flash"
// демонстрирует именно эту гибридную модель ("flash" — герой Task 8,
// сценарий "рабочий набор за границей RAM").
//
// Три сценария (-scenario=namespaces|hybrid|index-cost), общий источник
// истины — PostgreSQL (ORIGIN_DSN), общий датасет из dataset/.
//
// ОТКЛОНЕНИЕ ОТ БРИФА (поля info-протокола): бриф ожидал асинфо-поля с
// именами "memory_used_bytes" и "device_used_bytes". Живой прогон
// `asinfo -v "namespace/ram"` / `"namespace/flash"` на 8.1.2.3 показал, что
// таких полей НЕТ (сверено полным дампом всех ~450 полей на namespace,
// grep по "used|bytes" — см. task-5-report.md). Вместо них сервер отдаёт:
//   - index_used_bytes   — RAM, занятая первичным индексом (есть у ОБОИХ
//     namespace, включая "ram" — индекс учитывается отдельно от данных
//     даже когда storage-engine=memory);
//   - data_used_bytes    — данные: для "ram" это данные В ПАМЯТИ
//     (storage-engine memory), для "flash" — данные НА УСТРОЙСТВЕ
//     (storage-engine device, тот самый файл flash.dat).
//
// Отсюда пересчёт публикуемых величин брифа на фактические поля:
//   - "ram" memory_used_bytes   = index_used_bytes + data_used_bytes
//     (оба резидентны в RAM у storage-engine memory);
//   - "flash" memory_used_bytes = index_used_bytes
//     (единственное, что осталось в RAM у storage-engine device — и есть
//     "цена индекса", ключевое число серии);
//   - "flash" device_used_bytes = data_used_bytes
//     (данные физически на файле /opt/aerospike/data/flash.dat).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	as "github.com/aerospike/aerospike-client-go/v8"
	"github.com/jackc/pgx/v5/pgxpool"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---------- подключения ----------

func connectOrigin(ctx context.Context, dsn string) *pgxpool.Pool {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("pgxpool (origin): %v", err)
	}
	return pool
}

func connectAerospike(addr string) *as.Client {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		log.Fatalf("AEROSPIKE_ADDR=%q: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Fatalf("AEROSPIKE_ADDR=%q: некорректный порт: %v", addr, err)
	}
	cl, aerr := as.NewClient(host, port)
	if aerr != nil {
		log.Fatalf("aerospike.NewClient(%s:%d): %v", host, port, aerr)
	}
	return cl
}

// ---------- источник истины (PostgreSQL) ----------

type pgProduct struct {
	ID         int64
	SKU        string
	Title      string
	PriceCents int64
	Category   string
}

// loadProductsFromPG — читает весь products, отсортированный по id. Датасет
// (dataset/main.go) генерирует ID как 1..N подряд без пропусков — это
// позволяет ниже индексировать rows[id-1] напрямую, без построения map.
func loadProductsFromPG(ctx context.Context, pool *pgxpool.Pool) []pgProduct {
	rows, err := pool.Query(ctx, `SELECT id, sku, title, price_cents, attrs->>'category' AS category FROM products ORDER BY id`)
	if err != nil {
		log.Fatalf("чтение products из PG: %v", err)
	}
	defer rows.Close()
	var out []pgProduct
	for rows.Next() {
		var p pgProduct
		if err := rows.Scan(&p.ID, &p.SKU, &p.Title, &p.PriceCents, &p.Category); err != nil {
			log.Fatalf("scan products (PG): %v", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("products rows (PG): %v", err)
	}
	for i, p := range out {
		if p.ID != int64(i+1) {
			log.Fatalf("АССЕРТ: products.id не подряд с 1 (row %d имеет id=%d) — индексация rows[id-1] в этом стенде на это полагается", i, p.ID)
		}
	}
	return out
}

// ---------- запись в Aerospike ----------

const setName = "products"

// writeProducts — параллельная запись пачки строк в namespace ns. Aerospike
// Go-клиент не даёт батч-INSERT уровня pgx.CopyFrom/многострочного SQL —
// запись идёт по одной записи на Put, поэтому распараллеливаем через
// пул воркеров поверх общего пула соединений клиента.
func writeProducts(cl *as.Client, ns string, rows []pgProduct) {
	if len(rows) == 0 {
		return
	}
	policy := as.NewWritePolicy(0, 0)
	policy.SendKey = false

	const workers = 48
	jobs := make(chan pgProduct, len(rows))
	for _, r := range rows {
		jobs <- r
	}
	close(jobs)

	// errCh буферизован на len(rows), а не на workers: воркер ниже НЕ
	// выходит из цикла после первой ошибки (продолжает разбирать jobs, чтобы
	// не заблокировать остальные воркеры на общем канале и не потерять
	// оставшиеся элементы). При буфере=workers это создавало тихий дедлок —
	// после 48 ошибок ДО того, как все воркеры допишут done, отправка в
	// errCh блокируется навсегда, а горутина никогда не отправит done,
	// поэтому основной цикл ниже висит на <-done бесконечно (без Fatalf,
	// без диагностики). Верхняя граница числа отправок в errCh — ровно
	// len(rows) (максимум одна ошибка на Put), так что буфер такого размера
	// гарантирует: ни одна отправка не заблокируется, все 48 воркеров дойдут
	// до done, ни одна ошибка не будет потеряна молча. Существенно для
	// Task 8 (нагрузка до отказов записи) — этот код там переиспользуется.
	errCh := make(chan error, len(rows))
	done := make(chan struct{})
	var remaining = workers
	for i := 0; i < workers; i++ {
		go func() {
			for p := range jobs {
				key, kerr := as.NewKey(ns, setName, p.ID)
				if kerr != nil {
					errCh <- kerr
					continue
				}
				bins := as.BinMap{
					"sku":         p.SKU,
					"title":       p.Title,
					"price_cents": p.PriceCents,
					"category":    p.Category,
				}
				if perr := cl.Put(policy, key, bins); perr != nil {
					errCh <- fmt.Errorf("Put id=%d ns=%s: %v", p.ID, ns, perr)
				}
			}
			done <- struct{}{}
		}()
	}
	for remaining > 0 {
		<-done
		remaining--
	}
	close(errCh)
	var firstErr error
	nErrs := 0
	for err := range errCh {
		nErrs++
		if firstErr == nil {
			firstErr = err
		}
	}
	if nErrs > 0 {
		log.Fatalf("запись в ns=%s: %d ошибок, первая: %v", ns, nErrs, firstErr)
	}
}

// truncateNamespace — truncate:namespace=ns целиком (весь namespace, а не
// отдельный set — единственный set "products" в обоих namespace стенда).
// Truncate — АСИНХРОННЫЙ вызов (см. doc-комментарий клиента): сервер может
// вернуть управление раньше, чем объекты реально исчезнут из статистики.
// Вызывающий код обязан дождаться objects==0 через waitForObjects ниже,
// прежде чем полагаться на "чистый" namespace.
func truncateNamespace(cl *as.Client, ns string) {
	policy := as.NewInfoPolicy()
	if err := cl.Truncate(policy, ns, "", nil); err != nil {
		log.Fatalf("truncate namespace=%s: %v", ns, err)
	}
}

// ---------- снятие статистики namespace ----------

type nsStats struct {
	Objects      int64
	IndexUsed    int64 // index_used_bytes — RAM под первичный индекс (оба namespace)
	DataUsed     int64 // data_used_bytes — RAM у "ram", устройство у "flash"
	SetIndexUsed int64
	SindexUsed   int64
}

func fetchNodeInfo(cl *as.Client, commands ...string) map[string]string {
	node, err := cl.Cluster().GetRandomNode()
	if err != nil {
		log.Fatalf("GetRandomNode: %v", err)
	}
	res, ierr := node.RequestInfo(as.NewInfoPolicy(), commands...)
	if ierr != nil {
		log.Fatalf("RequestInfo(%v): %v", commands, ierr)
	}
	return res
}

// getStats — снимает "namespace/<ns>" ОДИН раз, без ожидания стабилизации
// (используется stabilizeStats ниже для многократного опроса).
func getStats(cl *as.Client, ns string) nsStats {
	raw := fetchNodeInfo(cl, "namespace/"+ns)
	body, ok := raw["namespace/"+ns]
	if !ok {
		log.Fatalf("namespace/%s: сервер не вернул ответ (ключ отсутствует в %v)", ns, raw)
	}
	fields := map[string]string{}
	for _, kv := range strings.Split(body, ";") {
		if kv == "" {
			continue
		}
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		fields[parts[0]] = parts[1]
	}
	getInt := func(name string) int64 {
		v, ok := fields[name]
		if !ok {
			log.Fatalf("namespace/%s: поле %q отсутствует в ответе (%d полей всего)", ns, name, len(fields))
		}
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil {
			log.Fatalf("namespace/%s: поле %q=%q не парсится как int64: %v", ns, name, v, perr)
		}
		return n
	}
	return nsStats{
		Objects:      getInt("objects"),
		IndexUsed:    getInt("index_used_bytes"),
		DataUsed:     getInt("data_used_bytes"),
		SetIndexUsed: getInt("set_index_used_bytes"),
		SindexUsed:   getInt("sindex_used_bytes"),
	}
}

// stabilizeStats — Task 3 этой же серии (memtx-vinyl, Tarantool) научил:
// метрики памяти могут отдаваться АСИНХРОННО после записи (у Tarantool —
// GC-фибер освобождает slab-память не синхронно с drop()). Aerospike
// формально пишет данные синхронно в рамках одной Put-транзакции, но
// namespace-статистика (objects/index_used_bytes/data_used_bytes)
// агрегируется по потокам записи и обновляется не мгновенно — опрашиваем
// несколько раз подряд и считаем снятие валидным только когда ДВА
// последовательных снимка идентичны по всем полям. Если не стабилизировалось
// за отведённое число попыток — это ЧЕСТНО печатается (не тихо принимается
// последнее значение как будто оно надёжно).
func stabilizeStats(cl *as.Client, ns string) nsStats {
	const maxAttempts = 30
	const pause = 300 * time.Millisecond
	prev := getStats(cl, ns)
	for i := 1; i < maxAttempts; i++ {
		time.Sleep(pause)
		cur := getStats(cl, ns)
		if cur == prev {
			fmt.Printf("  namespace/%s: стабилизировалось за %d опрос(ов) (интервал %s)\n", ns, i+1, pause)
			return cur
		}
		fmt.Printf("  namespace/%s: попытка %d/%d ещё меняется: objects %d->%d index_used_bytes %d->%d data_used_bytes %d->%d\n",
			ns, i, maxAttempts, prev.Objects, cur.Objects, prev.IndexUsed, cur.IndexUsed, prev.DataUsed, cur.DataUsed)
		prev = cur
	}
	fmt.Printf("  namespace/%s: ПРЕДУПРЕЖДЕНИЕ — не стабилизировалось за %d попыток (%s), беру последний снимок как есть\n", ns, maxAttempts, time.Duration(maxAttempts)*pause)
	return prev
}

func waitForObjects(cl *as.Client, ns string, want int64) {
	const maxAttempts = 40
	const pause = 250 * time.Millisecond
	for i := 0; i < maxAttempts; i++ {
		cur := getStats(cl, ns)
		if cur.Objects == want {
			return
		}
		time.Sleep(pause)
	}
	got := getStats(cl, ns).Objects
	log.Fatalf("namespace/%s: objects не дошло до %d за %s (осталось %d) — truncate не завершился или запись не закончилась", ns, want, time.Duration(maxAttempts)*pause, got)
}

// ---------- сценарий namespaces ----------

func scenarioNamespaces(ctx context.Context, origin *pgxpool.Pool, cl *as.Client) {
	rows := loadProductsFromPG(ctx, origin)
	fmt.Printf("namespaces: прочитано из PG products=%d\n", len(rows))

	fmt.Println("namespaces: truncate ram/flash перед заливкой (идемпотентность повторного прогона)")
	truncateNamespace(cl, "ram")
	truncateNamespace(cl, "flash")
	waitForObjects(cl, "ram", 0)
	waitForObjects(cl, "flash", 0)

	fmt.Printf("namespaces: заливаю %d записей в ram и flash\n", len(rows))
	writeProducts(cl, "ram", rows)
	waitForObjects(cl, "ram", int64(len(rows)))
	writeProducts(cl, "flash", rows)
	waitForObjects(cl, "flash", int64(len(rows)))

	fmt.Println("namespaces: снимаю статистику ram (ожидание стабилизации)")
	ramStats := stabilizeStats(cl, "ram")
	fmt.Println("namespaces: снимаю статистику flash (ожидание стабилизации)")
	flashStats := stabilizeStats(cl, "flash")

	ramMemUsed := ramStats.IndexUsed + ramStats.DataUsed // и индекс, и данные — в RAM (storage-engine memory)
	flashMemUsed := flashStats.IndexUsed                 // в RAM у flash остался ТОЛЬКО индекс
	flashDeviceUsed := flashStats.DataUsed               // данные — на устройстве (файл flash.dat)

	fmt.Printf("namespaces: ram   objects=%d index_used_bytes=%d data_used_bytes=%d => memory_used_bytes(RAM)=%d\n",
		ramStats.Objects, ramStats.IndexUsed, ramStats.DataUsed, ramMemUsed)
	fmt.Printf("namespaces: flash objects=%d index_used_bytes=%d data_used_bytes=%d => memory_used_bytes(RAM)=%d device_used_bytes=%d\n",
		flashStats.Objects, flashStats.IndexUsed, flashStats.DataUsed, flashMemUsed, flashDeviceUsed)

	if ramStats.Objects != flashStats.Objects {
		log.Fatalf("АССЕРТ: ram=%d записей, flash=%d — наборы не совпадают", ramStats.Objects, flashStats.Objects)
	}
	if flashMemUsed >= ramMemUsed {
		log.Fatalf("АССЕРТ: flash потребляет RAM %d >= ram %d — гибрид не наблюдается", flashMemUsed, ramMemUsed)
	}

	ratio := float64(flashMemUsed) / float64(ramMemUsed)
	fmt.Printf("namespaces: ПУБЛИКУЕМОЕ — цена индекса в flash против полного хранения в RAM: %d / %d = %.4f (%.1f%%)\n",
		flashMemUsed, ramMemUsed, ratio, ratio*100)
	fmt.Printf("namespaces: индекс в flash экономит %.1f%% RAM относительно полного хранения того же набора в ram-namespace\n", (1-ratio)*100)
}

// ---------- сценарий hybrid ----------

// ensureFlashPopulated — делает сценарий hybrid запускаемым САМОСТОЯТЕЛЬНО
// (без предварительного запуска namespaces): если objects в flash не равно
// ожидаемому N — namespace перезаливается с нуля.
func ensureFlashPopulated(cl *as.Client, rows []pgProduct) {
	cur := getStats(cl, "flash")
	if cur.Objects == int64(len(rows)) {
		fmt.Printf("hybrid: flash уже содержит %d записей — заливка не нужна\n", cur.Objects)
		return
	}
	fmt.Printf("hybrid: flash содержит %d записей, ожидается %d — перезаливаю\n", cur.Objects, len(rows))
	truncateNamespace(cl, "flash")
	waitForObjects(cl, "flash", 0)
	writeProducts(cl, "flash", rows)
	waitForObjects(cl, "flash", int64(len(rows)))
}

func scenarioHybrid(ctx context.Context, origin *pgxpool.Pool, cl *as.Client) {
	rows := loadProductsFromPG(ctx, origin)
	fmt.Printf("hybrid: прочитано из PG products=%d\n", len(rows))
	ensureFlashPopulated(cl, rows)

	const sampleSize = 1000
	const seed = 1000 // фиксирован — воспроизводимая выборка между прогонами
	perm := rand.New(rand.NewSource(seed)).Perm(len(rows))[:sampleSize]
	ids := make([]int, sampleSize)
	copy(ids, perm)
	sort.Ints(ids) // детерминированный порядок вывода, не влияет на выборку

	policy := as.NewPolicy()
	mismatches := 0
	for _, idx := range ids {
		want := rows[idx]
		key, kerr := as.NewKey("flash", setName, want.ID)
		if kerr != nil {
			log.Fatalf("NewKey id=%d: %v", want.ID, kerr)
		}
		rec, gerr := cl.Get(policy, key, "sku", "title", "price_cents", "category")
		if gerr != nil {
			fmt.Printf("  MISMATCH id=%d: Get error: %v\n", want.ID, gerr)
			mismatches++
			continue
		}
		gotSKU, _ := rec.Bins["sku"].(string)
		gotTitle, _ := rec.Bins["title"].(string)
		gotCategory, _ := rec.Bins["category"].(string)
		gotPrice, priceOK := toInt64(rec.Bins["price_cents"])
		if gotSKU != want.SKU || gotTitle != want.Title || gotCategory != want.Category || !priceOK || gotPrice != want.PriceCents {
			fmt.Printf("  MISMATCH id=%d: PG(sku=%s title=%q price=%d cat=%s) != flash(sku=%s title=%q price=%d cat=%s)\n",
				want.ID, want.SKU, want.Title, want.PriceCents, want.Category, gotSKU, gotTitle, gotPrice, gotCategory)
			mismatches++
		}
	}
	fmt.Printf("hybrid: сверено %d ключей (seed=%d) из flash-namespace с PostgreSQL, расхождений=%d\n", sampleSize, seed, mismatches)
	if mismatches != 0 {
		log.Fatalf("АССЕРТ: %d/%d ключей из flash не совпали с PG — flash-namespace не является корректным хранилищем", mismatches, sampleSize)
	}
	fmt.Println("hybrid: все 1000 значений из flash совпали с PostgreSQL — flash-namespace читается корректно, это не деградация")
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

// ---------- сценарий index-cost ----------

func scenarioIndexCost(ctx context.Context, origin *pgxpool.Pool, cl *as.Client) {
	rows := loadProductsFromPG(ctx, origin)
	steps := []int{10_000, 50_000, 100_000, 200_000}
	for i, s := range steps {
		if s > len(rows) {
			steps[i] = len(rows)
		}
	}

	fmt.Println("index-cost: truncate flash перед замером (нужен чистый рост от нуля)")
	truncateNamespace(cl, "flash")
	waitForObjects(cl, "flash", 0)

	type row struct {
		records          int64
		indexUsed        int64
		deltaRecords     int64
		deltaBytes       int64
		bytesPerRecCum   float64
		bytesPerRecDelta float64
	}
	var table []row
	prevCount := 0
	var prevIndexUsed int64
	for _, step := range steps {
		if step <= prevCount {
			continue
		}
		chunk := rows[prevCount:step]
		fmt.Printf("index-cost: заливаю %d новых записей (итого будет %d)\n", len(chunk), step)
		writeProducts(cl, "flash", chunk)
		waitForObjects(cl, "flash", int64(step))
		stats := stabilizeStats(cl, "flash")
		if stats.Objects != int64(step) {
			log.Fatalf("АССЕРТ: после заливки flash.objects=%d, ожидалось %d", stats.Objects, step)
		}
		deltaRecords := int64(step - prevCount)
		deltaBytes := stats.IndexUsed - prevIndexUsed
		table = append(table, row{
			records:          int64(step),
			indexUsed:        stats.IndexUsed,
			deltaRecords:     deltaRecords,
			deltaBytes:       deltaBytes,
			bytesPerRecCum:   float64(stats.IndexUsed) / float64(step),
			bytesPerRecDelta: float64(deltaBytes) / float64(deltaRecords),
		})
		prevCount = step
		prevIndexUsed = stats.IndexUsed
	}

	fmt.Println("index-cost: записей | index_used_bytes(RAM) | байт/запись (накопл.) | Δзаписей | Δbytes | байт/запись (Δ)")
	for _, r := range table {
		fmt.Printf("  %8d | %14d | %8.2f | %8d | %10d | %8.2f\n",
			r.records, r.indexUsed, r.bytesPerRecCum, r.deltaRecords, r.deltaBytes, r.bytesPerRecDelta)
	}

	if len(table) < 2 {
		log.Fatalf("АССЕРТ: недостаточно шагов для демонстрации роста (%d)", len(table))
	}
	first, last := table[0], table[len(table)-1]
	if last.indexUsed <= first.indexUsed {
		log.Fatalf("АССЕРТ: index_used_bytes не вырос между %d и %d записями (%d -> %d) — рост RAM от индекса не наблюдается",
			first.records, last.records, first.indexUsed, last.indexUsed)
	}
	fmt.Printf("index-cost: ПУБЛИКУЕМОЕ — байт RAM на запись (по полному диапазону 0..%d): %.2f\n",
		last.records, float64(last.indexUsed)/float64(last.records))
}

func main() {
	var (
		scenario  = flag.String("scenario", "", "namespaces|hybrid|index-cost")
		originDSN = flag.String("origin-dsn", os.Getenv("ORIGIN_DSN"), "DSN источника истины PostgreSQL")
		addr      = flag.String("aerospike-addr", envOr("AEROSPIKE_ADDR", "127.0.0.1:3000"), "адрес Aerospike (host:port)")
	)
	flag.Parse()

	if *originDSN == "" {
		log.Fatal("нужен -origin-dsn или ORIGIN_DSN")
	}

	ctx := context.Background()
	origin := connectOrigin(ctx, *originDSN)
	defer origin.Close()

	cl := connectAerospike(*addr)
	defer cl.Close()
	if _, werr := cl.WarmUp(64); werr != nil {
		fmt.Printf("WarmUp: %v (не критично, продолжаю)\n", werr)
	}

	start := time.Now()
	switch *scenario {
	case "namespaces":
		scenarioNamespaces(ctx, origin, cl)
	case "hybrid":
		scenarioHybrid(ctx, origin, cl)
	case "index-cost":
		scenarioIndexCost(ctx, origin, cl)
	default:
		log.Fatalf("неизвестный -scenario=%q (ожидается namespaces|hybrid|index-cost)", *scenario)
	}
	fmt.Printf("OK (%s, %s)\n", *scenario, time.Since(start).Round(time.Millisecond))
}
