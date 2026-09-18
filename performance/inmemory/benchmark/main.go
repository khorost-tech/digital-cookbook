// Стенд "бенчмарк" — сводит несколько систем серии на одной и той же
// растущей нагрузке. Task 8, сценарий 1: "рабочий набор за границей RAM".
//
// Суть: один и тот же растущий набор (products из общего датасета,
// заливается порциями по chunkSize) при ФИКСИРОВАННОЙ границе памяти на
// каждую систему. Вопрос не "кто быстрее" — абсолютная latency здесь не
// измеряется вовсе (см. README, "Границы метода") — а "что происходит,
// когда данные перестали помещаться": сколько записей остаётся реально
// доступно, вытесняет ли система старое ради нового, или отказывает.
//
// Три системы, все — Go-клиенты, все против ФИКСИРОВАННЫХ лимитов памяти:
//   - Redis (--maxmemory + allkeys-lru): ОБЯЗАН вытеснять — новые записи
//     принимаются, часть старых пропадает без предупреждения.
//   - Aerospike namespace "ram" (storage-engine memory): при лимите ведёт
//     себя как Redis БЕЗ вытеснения — отказывает в записи (данных, что
//     были приняты ДО отказа, это не касается, они остаются, но набор
//     дальше не растёт).
//   - Aerospike namespace "flash" (storage-engine device, индекс в RAM,
//     данные на устройстве): герой сценария — должен принять ВЕСЬ растущий
//     набор без единой потери, потому что в RAM лежит только 64-байтовый
//     индекс на запись (задокументированный факт Task 5, не открытие этой
//     задачи), а данные уезжают на диск.
//
// ЧТО ИЗМЕРЯЕТСЯ (после ревью, см. task-8-report.md, "Фиксы по ревью"):
// АБСОЛЮТНОЕ число записей, на котором каждая система меняет статус, и
// объём занятой памяти — НЕ процент от размера датасета. Процент от
// произвольного знаменателя (сколько всего записей мы решили залить) —
// не свойство системы, а артефакт выбора этого знаменателя: точка отказа
// namespace "ram" зафиксирована конфигом (~512 МиБ data-size,
// stop-writes-used-pct=1%) и НЕ зависит от того, заливаем мы 200 000 или
// 2 000 000 записей. Раздувать датасет исключительно ради того, чтобы
// абсолютная (неизменная) точка отказа пришлась на заранее выбранный
// процент — значит подгонять знаменатель под целевое число, а не измерять
// систему. Датасет здесь — весь реальный products из PostgreSQL (Task 1,
// 200 000 записей), без синтетического наращивания: живые прогоны (см.
// task-8-report.md) показали, что все наблюдавшиеся точки отказа ram
// лежат строго внутри первых 200 000 записей — реального датасета
// достаточно, чтобы показать все три исхода без единой синтетической
// строки.
//
// ОТКЛОНЕНИЕ ОТ БРИФА (Aerospike, лимит "ram" через stop-writes-used-pct,
// а не через data-size напрямую): живой прогон подтвердил жёсткий минимум
// сервера storage-engine.data-size >= 536870912 (512 МиБ) — меньшее
// значение сервер отвергает при старте (CRITICAL cfg.c:1867). Опустить
// data-size до нескольких мегабайт, как в брифе буквально, НЕВОЗМОЖНО —
// пришлось бы либо раздувать датасет в тысячи раз (непропорционально
// демо), либо использовать другой рычаг. Использован
// storage-engine.stop-writes-used-pct (единственный НЕ настраиваемый
// динамически, но настраиваемый В КОНФИГЕ параметр, который двигает точку
// отказа в абсолютных байтах даже при обязательных 512 МиБ data-size) —
// см. aerospike/aerospike-workingset.conf. Ignite сознательно НЕ участвует
// в этом сценарии — см. README, "Что не воспроизвелось", Task 8: с
// дефолтным -Xmx (compose/ignite.yml, не менявшимся) 200k мелких записей
// не создают заметного memory pressure, точки перелома там нет и не
// заявлена; ассерты брифа Ignite не касаются вовсе. Сценарий сравнивает
// Redis и Aerospike (ram/flash) — это НЕ полное сравнение всех систем
// серии, и README прямо это оговаривает.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	as "github.com/aerospike/aerospike-client-go/v8"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/tarantool/go-tarantool/v2"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return def
}

// ---------- источник истины (PostgreSQL) ----------

type pgProduct struct {
	ID         int64
	SKU        string
	Title      string
	PriceCents int64
	Category   string
}

// loadProductsFromPG — та же схема запроса и то же допущение (id 1..N без
// пропусков), что в aerospike/main.go и redis-cache/main.go: датасет
// (dataset/main.go) генерирует ID подряд, это позволяет использовать
// (id-1) как индекс без построения map.
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
			log.Fatalf("АССЕРТ: products.id не подряд с 1 (row %d имеет id=%d) — индексация по (id-1) на это полагается", i, p.ID)
		}
	}
	return out
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

// ---------- запись: Redis ----------

const redisKeyPrefix = "ws:product:"

func redisKey(id int64) string { return redisKeyPrefix + strconv.FormatInt(id, 10) }

type redisValue struct {
	SKU        string `json:"sku"`
	Title      string `json:"title"`
	PriceCents int64  `json:"price_cents"`
	Category   string `json:"category"`
}

// writeProductsRedis — пишет пачку через pipeline (10k SET за один round-trip
// вместо 10k отдельных). Ошибки НЕ фатальны и по отдельности не ожидаются
// (Redis с allkeys-lru всегда находит место, вытесняя старое, а не
// отказывая в записи) — но считаются на всякий случай, а не молча
// игнорируются: если Redis когда-нибудь всё же откажет (например, при
// OOM без сработавшей политики вытеснения), это должно быть видно, а не
// спрятано.
func writeProductsRedis(ctx context.Context, rdb *redis.Client, rows []pgProduct) (attempted, failed int) {
	if len(rows) == 0 {
		return 0, 0
	}
	pipe := rdb.Pipeline()
	cmds := make([]*redis.StatusCmd, len(rows))
	for i, r := range rows {
		val, err := json.Marshal(redisValue{r.SKU, r.Title, r.PriceCents, r.Category})
		if err != nil {
			log.Fatalf("json.Marshal product id=%d: %v", r.ID, err)
		}
		cmds[i] = pipe.Set(ctx, redisKey(r.ID), val, 0)
	}
	_, _ = pipe.Exec(ctx) // ошибка Exec() агрегирует первую — реальные отказы считаем по каждой команде ниже
	for _, c := range cmds {
		if c.Err() != nil {
			failed++
		}
	}
	return len(rows), failed
}

// ---------- запись: Aerospike (толерантная к ошибкам версия) ----------

const aeroSetName = "products"

// writeProductsTolerant — пачка на namespace ns, много воркеров поверх
// общего пула соединений клиента (тот же паттерн, что в aerospike/main.go
// writeProducts). ОТЛИЧИЕ ОТ ОРИГИНАЛА: там любая ошибка — Fatalf (нагрузка
// туда штатно ниже лимита памяти). ЗДЕСЬ ошибки — ОЖИДАЕМЫЙ, измеряемый
// исход (namespace "ram" должен начать отказывать после точки перелома) —
// поэтому ошибки СЧИТАЮТСЯ, а не валят программу.
//
// Урок Task 5 (README, "Стенд 5"/aerospike/main.go, комментарий над
// writeProducts) применён явно: errCh буферизован на len(rows), НЕ на
// число воркеров. При буфере=workers картина "много ошибок подряд"
// (ИМЕННО то, что нарочно вызывается в этом сценарии, доведение ram до
// отказа) создаёт тихий дедлок — воркер блокируется на отправке в
// переполненный errCh, никогда не отправляет в done, основной цикл висит
// на <-done навсегда без единой диагностики. Верхняя граница числа ошибок
// не может превышать len(rows) (максимум одна ошибка на Put), так что
// буфер такого размера гарантирует: ни одна отправка в errCh никогда не
// блокируется.
// workers=48 — то же значение, что и aerospike/main.go writeProducts
// (Task 5), сохранено ради согласованности стенда, а не выбрано заново под
// эту задачу. Ревью Task 8 (см. task-8-report.md, "Фиксы по ревью",
// Находка 5) проверило: конкретное число воркеров НЕ определяет разброс
// точки отказа ram напрямую — целевой эксперимент (25 живых прогонов,
// nsup-period 1..60с) исключил объяснение через порядок прибытия
// конкурентных Put. Роль конкурентности здесь косвенная: она определяет
// ДОСТИЖИМЫЙ throughput записи, а именно нестабильность throughput под
// Docker Desktop (замерено 10 400-29 300 записей/сек между иначе
// идентичными прогонами) — более вероятный, хотя тоже не доказанный до
// конца, источник разброса точки отказа. Другое число воркеров (24/96)
// сдвинуло бы достижимый throughput, но не устранило бы саму
// нестабильность платформы — поэтому число не вынесено в параметр без
// явной необходимости.
func writeProductsTolerant(cl *as.Client, ns string, rows []pgProduct) (attempted, failed int) {
	if len(rows) == 0 {
		return 0, 0
	}
	policy := as.NewWritePolicy(0, 0)
	policy.SendKey = false

	const workers = 48
	jobs := make(chan pgProduct, len(rows))
	for _, r := range rows {
		jobs <- r
	}
	close(jobs)

	errCh := make(chan error, len(rows))
	done := make(chan struct{})
	remaining := workers
	for i := 0; i < workers; i++ {
		go func() {
			for p := range jobs {
				key, kerr := as.NewKey(ns, aeroSetName, p.ID)
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
					errCh <- perr
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
	for range errCh {
		failed++
	}
	return len(rows), failed
}

func truncateNamespace(cl *as.Client, ns string) {
	policy := as.NewInfoPolicy()
	if err := cl.Truncate(policy, ns, "", nil); err != nil {
		log.Fatalf("truncate namespace=%s: %v", ns, err)
	}
}

type nsStats struct {
	Objects   int64
	IndexUsed int64
	DataUsed  int64
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

func getAeroStats(cl *as.Client, ns string) nsStats {
	raw := fetchNodeInfo(cl, "namespace/"+ns)
	body, ok := raw["namespace/"+ns]
	if !ok {
		log.Fatalf("namespace/%s: сервер не вернул ответ", ns)
	}
	fields := map[string]string{}
	for _, kv := range strings.Split(body, ";") {
		if kv == "" {
			continue
		}
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			fields[parts[0]] = parts[1]
		}
	}
	getInt := func(name string) int64 {
		v, ok := fields[name]
		if !ok {
			log.Fatalf("namespace/%s: поле %q отсутствует", ns, name)
		}
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil {
			log.Fatalf("namespace/%s: поле %q=%q не int64: %v", ns, name, v, perr)
		}
		return n
	}
	return nsStats{
		Objects:   getInt("objects"),
		IndexUsed: getInt("index_used_bytes"),
		DataUsed:  getInt("data_used_bytes"),
	}
}

// stabilizeAeroStats — тот же урок Task 3/5 этой серии: метрики памяти
// могут отдаваться асинхронно относительно завершения записи. Бюджет ниже
// сокращён относительно Task 5 (30 попыток * 300мс) — здесь снятие
// происходит 20 раз за прогон (после каждой из 20 порций) для 3 систем,
// полный бюджет Task 5 умножил бы время прогона неприемлемо; вместо этого
// печатается явное предупреждение при отсутствии стабилизации, а не тихая
// подмена последним значением как надёжным.
func stabilizeAeroStats(cl *as.Client, ns string) nsStats {
	const maxAttempts = 8
	const pause = 200 * time.Millisecond
	prev := getAeroStats(cl, ns)
	for i := 1; i < maxAttempts; i++ {
		time.Sleep(pause)
		cur := getAeroStats(cl, ns)
		if cur == prev {
			return cur
		}
		prev = cur
	}
	fmt.Printf("  namespace/%s: не стабилизировалось за %d попыток (%s) — беру последний снимок как есть\n",
		ns, maxAttempts, time.Duration(maxAttempts)*pause)
	return prev
}

func waitForObjects(cl *as.Client, ns string, want int64) {
	const maxAttempts = 40
	const pause = 250 * time.Millisecond
	for i := 0; i < maxAttempts; i++ {
		cur := getAeroStats(cl, ns)
		if cur.Objects == want {
			return
		}
		time.Sleep(pause)
	}
	got := getAeroStats(cl, ns).Objects
	log.Fatalf("namespace/%s: objects не дошло до %d за %s (осталось %d)", ns, want, time.Duration(maxAttempts)*pause, got)
}

// getStopWrites — "namespace/<ns>" поле stop_writes (true/false), НЕ входит
// в nsStats/getAeroStats выше намеренно: опрашивается отдельно и реже
// (только на старте сценария), не на каждой из 20 порций.
func getStopWrites(cl *as.Client, ns string) bool {
	raw := fetchNodeInfo(cl, "namespace/"+ns)
	body, ok := raw["namespace/"+ns]
	if !ok {
		log.Fatalf("namespace/%s: сервер не вернул ответ", ns)
	}
	for _, kv := range strings.Split(body, ";") {
		if strings.HasPrefix(kv, "stop_writes=") {
			return strings.TrimPrefix(kv, "stop_writes=") == "true"
		}
	}
	log.Fatalf("namespace/%s: поле stop_writes отсутствует в ответе", ns)
	return false
}

// waitStopWritesClear — ЖИВОЙ ПРОГОН (два прогона сценария подряд, см.
// task-8-report.md) вскрыл: после truncate() objects доходит до 0 быстро
// (секунды), но флаг stop_writes остаётся ЗАСТРЯВШИМ в true с ПРЕДЫДУЩЕГО
// прогона до следующего тика namespace supervisor (nsup-period в конфиге,
// см. aerospike-workingset.conf) — при повторном прогоне без паузы это
// означает, что ПЕРВАЯ порция новой заливки начинает с отказов записи,
// даже когда namespace физически пуст (objects=0). Это ТОЧНО тот же класс
// дефекта, что и Task 3 (memtx-vinyl: асинхронное освобождение памяти
// давало заниженную дельту при повторном прогоне без ожидания устоя) —
// разница только в направлении (здесь "устаревшее true", не "устаревшее
// большое число"). waitForObjects(ns, 0) выше НЕ ловит это — он проверяет
// только objects, не stop_writes, они меняются НЕ синхронно.
func waitStopWritesClear(cl *as.Client, ns string) {
	const maxAttempts = 40
	const pause = 500 * time.Millisecond
	for i := 0; i < maxAttempts; i++ {
		if !getStopWrites(cl, ns) {
			return
		}
		time.Sleep(pause)
	}
	fmt.Printf("  ПРЕДУПРЕЖДЕНИЕ: namespace/%s: stop_writes не сбросился в false за %s после truncate — сценарий начнётся с отказов записи с первой же порции (устаревшее состояние с предыдущего прогона), см. nsup-period в конфиге\n",
		ns, time.Duration(maxAttempts)*pause)
}

// ---------- выборочное чтение "сколько ещё отвечает" ----------

// ---------- Redis: memory/stats ----------

func parseInfoField(info, field string) (int64, bool) {
	prefix := field + ":"
	for _, line := range strings.Split(info, "\r\n") {
		if strings.HasPrefix(line, prefix) {
			v := strings.TrimPrefix(line, prefix)
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return 0, false
			}
			return n, true
		}
	}
	return 0, false
}

func redisUsedMemory(ctx context.Context, rdb *redis.Client) int64 {
	info, err := rdb.Info(ctx, "memory").Result()
	if err != nil {
		log.Fatalf("redis INFO memory: %v", err)
	}
	v, ok := parseInfoField(info, "used_memory")
	if !ok {
		log.Fatalf("redis INFO memory: поле used_memory не найдено")
	}
	return v
}

func redisEvictedKeys(ctx context.Context, rdb *redis.Client) int64 {
	info, err := rdb.Info(ctx, "stats").Result()
	if err != nil {
		log.Fatalf("redis INFO stats: %v", err)
	}
	v, ok := parseInfoField(info, "evicted_keys")
	if !ok {
		log.Fatalf("redis INFO stats: поле evicted_keys не найдено")
	}
	return v
}

// ---------- сценарий working-set ----------
//
// ПОСЛЕ РЕВЬЮ (см. task-8-report.md, "Фиксы по ревью", Находка 1/3):
// синтетический хвост датасета (id 200001..500000, генерировавшийся
// детерминированно) УБРАН. Все 6 живых прогонов первой версии сценария
// показали точку отказа ram строго внутри первых 200 000 записей
// (50 000-170 000) — синтетический хвост физически ни разу не был
// затронут ни в одном прогоне, он существовал только чтобы искусственно
// увеличить знаменатель дроби "точка_отказа / размер_набора" до целевого
// значения ~25%. Это ложная точность: абсолютная точка отказа ram задаётся
// конфигом (data-size/stop-writes-used-pct), а не размером набора, и
// процент от него — управляемая, а не измеряемая величина. Реального
// датасета (200 000 products из PostgreSQL, Task 1) достаточно, чтобы
// показать все три исхода без единой синтетической строки.

type systemProgress struct {
	name          string
	fed           int
	failedTotal   int   // Aerospike: суммарный счёт отказов записи
	evictedAtLast int64 // Redis: evicted_keys на момент последнего снятия
	breakAtFed    int   // 0 = перелом ещё не зафиксирован
	lastStatus    string
	lastRAMBytes  int64
}

func statusLabel(broke bool, isRedis bool) string {
	if !broke {
		return "отвечает"
	}
	if isRedis {
		return "вытесняет"
	}
	return "отказ"
}

func scenarioWorkingSet(ctx context.Context, origin *pgxpool.Pool, rdb *redis.Client, aeroCl *as.Client, chunkSize, total int) {
	rows := loadProductsFromPG(ctx, origin)
	realCount := len(rows)
	if total > realCount {
		// После ревью (см. task-8-report.md, "Фиксы по ревью"): синтетический
		// хвост убран намеренно — раздувание датасета сверх реального было
		// нужно только чтобы подогнать процент от знаменателя под целевое
		// значение, а это ложная точность (см. комментарий над
		// generateSyntheticTail в истории git). -total не может превышать
		// реальный размер датасета.
		log.Fatalf("АССЕРТ: -total=%d > реального датасета PostgreSQL (%d записей) — синтетическое наращивание убрано по ревью (task-8-report.md, Находка 1), уменьшите -total/WS_TOTAL до %d или меньше", total, realCount, realCount)
	}
	rows = rows[:total]
	fmt.Printf("working-set: датасет=%d записей products (PostgreSQL, источник истины)\n", total)

	// --- подготовка: чистый старт на всех трёх системах ---
	fmt.Println("working-set: подготовка — FLUSHALL redis, truncate ram/flash, сброс счётчиков")
	if err := rdb.FlushAll(ctx).Err(); err != nil {
		log.Fatalf("redis FLUSHALL: %v", err)
	}
	if err := rdb.ConfigResetStat(ctx).Err(); err != nil {
		log.Fatalf("redis CONFIG RESETSTAT: %v", err)
	}
	truncateNamespace(aeroCl, "ram")
	truncateNamespace(aeroCl, "flash")
	waitForObjects(aeroCl, "ram", 0)
	waitForObjects(aeroCl, "flash", 0)
	// См. комментарий над waitStopWritesClear: objects==0 НЕ гарантирует
	// stop_writes==false — это отдельное, асинхронное относительно truncate
	// состояние. Без этого шага повторный прогон сразу после прогона,
	// доведшего ram до отказа, начинает с отказов на первой же порции.
	waitStopWritesClear(aeroCl, "ram")
	waitStopWritesClear(aeroCl, "flash")

	redisMM, err := rdb.ConfigGet(ctx, "maxmemory").Result()
	if err != nil {
		log.Fatalf("redis CONFIG GET maxmemory: %v", err)
	}
	redisMMPolicy, err := rdb.ConfigGet(ctx, "maxmemory-policy").Result()
	if err != nil {
		log.Fatalf("redis CONFIG GET maxmemory-policy: %v", err)
	}
	fmt.Printf("working-set: redis maxmemory=%s maxmemory-policy=%s (обязаны быть выставлены заранее скриптом оркестровки)\n",
		redisMM["maxmemory"], redisMMPolicy["maxmemory-policy"])
	if redisMM["maxmemory"] == "0" {
		log.Fatalf("АССЕРТ: redis maxmemory=0 (без границы) — сценарий недоказателен без фиксированного лимита")
	}
	if redisMMPolicy["maxmemory-policy"] != "allkeys-lru" {
		log.Fatalf("АССЕРТ: redis maxmemory-policy=%q, ожидается allkeys-lru", redisMMPolicy["maxmemory-policy"])
	}

	sys := map[string]*systemProgress{
		"redis":           {name: "redis"},
		"aerospike-ram":   {name: "aerospike-ram"},
		"aerospike-flash": {name: "aerospike-flash"},
	}

	fmt.Println()
	fmt.Println("портция | залито(куммл) | redis:отказ/вытесн | ram:отказ | flash:отказ | redis_mem | ram_mem(idx+data) | flash_mem(idx)")
	prevTotal := 0
	redisSetOK := 0 // накопительный счётчик успешных SET — нужен для ассерта DBSIZE ниже
	for step := chunkSize; ; step += chunkSize {
		if step > total {
			step = total
		}
		chunk := rows[prevTotal:step]

		_, redisFailed := writeProductsRedis(ctx, rdb, chunk)
		redisSetOK += len(chunk) - redisFailed
		if redisFailed > 0 {
			// НЕ ожидается (allkeys-lru всегда находит место, вытесняя, а
			// не отказывая), но если когда-нибудь случится — печатаем
			// явно, а не молчим.
			fmt.Printf("  ПРЕДУПРЕЖДЕНИЕ: redis SET вернул ошибку на %d/%d записях этой порции\n", redisFailed, len(chunk))
		}
		_, ramFailed := writeProductsTolerant(aeroCl, "ram", chunk)
		_, flashFailed := writeProductsTolerant(aeroCl, "flash", chunk)

		sys["redis"].fed = step
		sys["aerospike-ram"].fed = step
		sys["aerospike-flash"].fed = step
		sys["aerospike-ram"].failedTotal += ramFailed
		sys["aerospike-flash"].failedTotal += flashFailed
		if sys["aerospike-ram"].failedTotal > 0 && sys["aerospike-ram"].breakAtFed == 0 {
			sys["aerospike-ram"].breakAtFed = step
		}
		if sys["aerospike-flash"].failedTotal > 0 && sys["aerospike-flash"].breakAtFed == 0 {
			sys["aerospike-flash"].breakAtFed = step
		}

		evicted := redisEvictedKeys(ctx, rdb)
		sys["redis"].evictedAtLast = evicted
		if evicted > 0 && sys["redis"].breakAtFed == 0 {
			sys["redis"].breakAtFed = step
		}
		redisMem := redisUsedMemory(ctx, rdb)
		ramStats := getAeroStats(aeroCl, "ram")
		flashStats := getAeroStats(aeroCl, "flash")
		sys["redis"].lastRAMBytes = redisMem
		sys["aerospike-ram"].lastRAMBytes = ramStats.IndexUsed + ramStats.DataUsed
		sys["aerospike-flash"].lastRAMBytes = flashStats.IndexUsed

		// ТОЧНЫЕ счётчики, не выборка (см. АССЕРТ и комментарий ниже, после
		// цикла, "Находка ревью-3"): redis — DBSIZE, aerospike — поле
		// "objects" из namespace-info (уже снималось выше для ramStats/
		// flashStats, здесь просто читается то же значение).
		redisDBSize, err := rdb.DBSize(ctx).Result()
		if err != nil {
			log.Fatalf("redis DBSIZE: %v", err)
		}

		fmt.Printf("%7d | %13d | redis_evicted=%-6d(доступно_DBSIZE=%d) | ram_отказов=%-6d(доступно_objects=%d) | flash_отказов=%-6d(доступно_objects=%d) | %10d | %10d | %10d\n",
			step, step, evicted, redisDBSize, ramFailed, ramStats.Objects, flashFailed, flashStats.Objects,
			redisMem, sys["aerospike-ram"].lastRAMBytes, sys["aerospike-flash"].lastRAMBytes)

		prevTotal = step
		if step >= total {
			break
		}
	}
	fmt.Println()

	fmt.Println("working-set: финальная стабилизация метрик памяти (Task 3/5 — асинхронность отдачи статистики)")
	ramStatsFinal := stabilizeAeroStats(aeroCl, "ram")
	flashStatsFinal := stabilizeAeroStats(aeroCl, "flash")
	redisMemFinal := redisUsedMemory(ctx, rdb)
	redisEvictedFinal := redisEvictedKeys(ctx, rdb)
	redisDBSizeFinal, err := rdb.DBSize(ctx).Result()
	if err != nil {
		log.Fatalf("redis DBSIZE: %v", err)
	}

	// АССЕРТ-ИНВАРИАНТ (третий раунд внешнего ревью, "публикуемое число
	// арифметически невозможно"): раньше здесь печаталась ОЦЕНКА ПО ВЫБОРКЕ
	// (повторные GET по случайным id через sampleRedisAvailable). У Redis с
	// allkeys-lru каждый такой GET обновляет LRU-позицию прочитанного ключа —
	// то есть само измерение вмешивалось в измеряемое состояние: читаемые
	// выборкой ключи искусственно переживали последующие вытеснения, а
	// результат сверху ещё печатался как точное число, а не оценка. DBSIZE —
	// точный подсчёт ключей на сервере, выборка Redis здесь не нужна вовсе.
	// Инвариант ниже проверяет ЭТО ЖЕ на арифметике: раз ни один ключ не
	// перезаписывается (id уникальны, TTL не используется — единственный
	// способ ключу исчезнуть — вытеснение), DBSIZE обязан ТОЧНО равняться
	// (успешных SET − evicted_keys). Если не сходится — прогон падает с
	// диагностикой, а не публикует красивое, но невозможное число.
	redisExpectedAvail := int64(redisSetOK) - redisEvictedFinal
	if redisDBSizeFinal != redisExpectedAvail {
		log.Fatalf("АССЕРТ: redis DBSIZE=%d расходится с ожидаемым (успешных_SET=%d − evicted_keys=%d)=%d — арифметика не сходится, дальнейшая публикация числа недостоверна",
			redisDBSizeFinal, redisSetOK, redisEvictedFinal, redisExpectedAvail)
	}

	redisAvail := redisDBSizeFinal
	// Aerospike: ТОЧНЫЙ подсчёт через "objects" из namespace-info (то же
	// поле, что уже используется truncate-ожиданием waitForObjects выше в
	// этом файле, — фактически проверено, что оно существует и отдаётся
	// сервером). Точного аналога DBSIZE Redis у Aerospike нет, но "objects"
	// namespace-info даёт ровно то же самое (число хранимых записей),
	// поэтому выборка не нужна и здесь тоже — ни для ram, ни для flash.
	ramAvail := ramStatsFinal.Objects
	flashAvail := flashStatsFinal.Objects

	redisStatus := statusLabel(redisEvictedFinal > 0, true)
	ramStatus := statusLabel(sys["aerospike-ram"].failedTotal > 0, false)
	flashStatus := statusLabel(sys["aerospike-flash"].failedTotal > 0, false)

	breakStr := func(v int) string {
		if v == 0 {
			return "нет"
		}
		return strconv.Itoa(v)
	}

	fmt.Println("working-set: ИТОГОВАЯ ТАБЛИЦА (система -> залито -> доступно(ТОЧНО: redis=DBSIZE, aerospike=objects из namespace-info) -> RAM(байт) -> статус -> точка_перелома)")
	fmt.Printf("СИСТЕМА redis           залито=%d доступно=%d RAM_bytes=%d статус=%s перелом_на_записи=%s\n",
		total, redisAvail, redisMemFinal, redisStatus, breakStr(sys["redis"].breakAtFed))
	fmt.Printf("СИСТЕМА aerospike-ram   залито=%d доступно=%d RAM_bytes=%d статус=%s перелом_на_записи=%s\n",
		total, ramAvail, ramStatsFinal.IndexUsed+ramStatsFinal.DataUsed, ramStatus, breakStr(sys["aerospike-ram"].breakAtFed))
	fmt.Printf("СИСТЕМА aerospike-flash залито=%d доступно=%d RAM_bytes=%d статус=%s перелом_на_записи=%s\n",
		total, flashAvail, flashStatsFinal.IndexUsed, flashStatus, breakStr(sys["aerospike-flash"].breakAtFed))

	fmt.Println()
	fmt.Printf("working-set: redis успешных SET(суммарно)=%d, отказов записи=0 (LRU всегда находит место), evicted_keys(суммарно)=%d, DBSIZE=%d\n",
		redisSetOK, redisEvictedFinal, redisDBSizeFinal)
	fmt.Printf("working-set: aerospike ram отказов записи(суммарно)=%d, flash отказов записи(суммарно)=%d\n",
		sys["aerospike-ram"].failedTotal, sys["aerospike-flash"].failedTotal)

	// ---------- ассерты брифа (дословно) ----------
	redisEvicted := redisEvictedFinal
	aerospikeFlashLost := int64(sys["aerospike-flash"].failedTotal)
	aerospikeRamLost := int64(sys["aerospike-ram"].failedTotal)

	// Redis с allkeys-lru обязан ВЫТЕСНЯТЬ: записи продолжают приниматься, но
	// часть старых пропадает. Если пропаж нет — набор не превысил границу,
	// и весь сценарий недоказателен.
	if redisEvicted == 0 {
		log.Fatalf("АССЕРТ: Redis ничего не вытеснил — maxmemory не достигнут, сценарий недоказателен")
	}
	// Aerospike flash обязан сохранить ВСЕ записи: данные ушли на устройство.
	if aerospikeFlashLost > 0 {
		log.Fatalf("АССЕРТ: flash-namespace потерял %d записей — гибрид не работает как заявлено", aerospikeFlashLost)
	}
	// Aerospike ram при том же лимите обязан повести себя иначе, чем flash —
	// в этом контрасте весь смысл сценария.
	if aerospikeRamLost == 0 && aerospikeFlashLost == 0 {
		log.Fatalf("АССЕРТ: ram и flash повели себя одинаково — лимит памяти не достигнут")
	}

	fmt.Println()
	fmt.Println("working-set: все ассерты пройдены — точка перелома достигнута для redis и aerospike-ram, flash сохранил всё")
}

// ============================================================================
// Сценарий 2 бенчмарка: compute-locality (Task 9)
//
// "Цена выноса вычислений к данным": одна и та же агрегация (топ-10 товаров
// категории по просмотрам) двумя путями — НАИВНО (вычитать всю категорию в
// приложение, отсортировать в Go/Java) против ЛОКАЛЬНО (вызвать хранимку
// Tarantool / affinity-задачу Ignite, получить только топ). Оверхед платформы
// (Docker Desktop) здесь работает НА честность: каждый лишний round-trip
// платит полную цену этого оверхеда, поэтому разница видна ИМЕННО потому,
// что сеть дорогая — см. README, "Границы метода".
//
// Потребляет ДВА УЖЕ ЗАФИКСИРОВАННЫХ контракта, ни один не меняется здесь:
//   - top_products_by_category(category, limit) — tarantool/init.lua (Task 3)
//   - TopByCategoryTask (affinity-задача)        — ignite/.../IgniteStand.java (Task 6)
//
// ОТКЛОНЕНИЕ ОТ БРИФА №1 (файл вне списка Task 9, но необходимое): для
// измерения отношения времени наивно/локально и для защиты от смещения
// порядка (Step 2 брифа) IgniteStand.java получил параметр
// -order=naive-first|local-first и печать раздельных nanoTime()-замеров
// вокруг УЖЕ существовавших блоков ScanQuery/affinityCall (Стенд 6, Task 6).
// TopByCategoryTask, CategoryFilter и тай-брейк TOP_ORDER НЕ тронуты — это
// чистая инструментация поверх готового и уже отревьюированного кода.
//
// ОТКЛОНЕНИЕ ОТ БРИФА №2 (архитектурное, задокументировано уже в Task 6):
// Ignite участвует в этом сценарии ЧЕРЕЗ ПОДПРОЦЕСС (`docker run` того же
// ignite-stand.jar, тем же способом, каким его запускает README «Как
// воспроизвести»), а НЕ через нативный Go-клиент. У тонкого Ignite-клиента
// 2.18.0 нет Affinity API/affinityCall (см. шапку IgniteStand.java, javap
// подтверждён Task 6), а толстая клиент-нода — JVM-специфичный участник
// discovery-кольца, которого у Go нет. Это тот же класс решения, что и
// Java/Go разделение во всей серии (Ignite — единственный Java-стенд):
// benchmark/main.go остаётся Go-программой и ОРКЕСТРИРУЕТ Java-подпроцесс,
// а не пытается заново реализовать протокол Ignite thick-client в Go.
//
// Тай-брейки двух хранилищ РАЗНЫЕ и НАМЕРЕННО не унифицированы (оба —
// зафиксированные контракты Task 3/Task 6): Tarantool сортирует по PK
// DESCENDING внутри равных views (артефакт REQ-итератора по вторичному
// индексу), Ignite — по views DESC, затем id ASC. Вместо правки контрактов
// сценарий ДОКАЗЫВАЕТ экспериментом (см. crossCheckTieBreak ниже), что при
// limit=10 в категории tools единственная ничья (views=93, id=84 и id=85)
// НЕ влияет на состав топа ни в одном из двух правил — обе стороны
// сравнения корректны, несмотря на разные тай-брейки.
// ============================================================================

const (
	clProbeCategory = "tools"
	// clProbeLimit фиксирован в 10 — это TOP_LIMIT в IgniteStand.java
	// (Стенд 6, не параметризован там), поэтому здесь limit подстроен под
	// Ignite, а не наоборот: единая проба на обеих системах обязательна для
	// сравнения (см. README, "Контракт для Task 9").
	clProbeLimit = 10
)

// ---------- источник истины для compute-locality (products + views) ----------

// clPgProduct — то же самое чтение, что и tarantool/main.go loadProductsFromPG
// и ignite IgniteStand.java loadProductsFromPG: продукт + агрегированное
// число просмотров. Отдельный тип от pgProduct выше (working-set) намеренно —
// та структура без Views, смешивать некорректно.
type clPgProduct struct {
	ID         int64
	SKU        string
	Title      string
	PriceCents int64
	Category   string
	Views      int64
}

func clLoadProductsFromPG(ctx context.Context, pool *pgxpool.Pool) []clPgProduct {
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
		log.Fatalf("compute-locality: чтение products из PG: %v", err)
	}
	defer rows.Close()
	var out []clPgProduct
	for rows.Next() {
		var p clPgProduct
		if err := rows.Scan(&p.ID, &p.SKU, &p.Title, &p.PriceCents, &p.Category, &p.Views); err != nil {
			log.Fatalf("compute-locality: scan products (PG): %v", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("compute-locality: products rows (PG): %v", err)
	}
	if len(out) == 0 {
		log.Fatalf("АССЕРТ: PostgreSQL вернул 0 products — датасет не залит (dataset/ -load)")
	}
	return out
}

// ---------- Tarantool: наивно (SELECT всей категории) против хранимки ----------

// clTarantoolTuple зеркалит products:format() в tarantool/init.lua — тот же
// позиционный формат, что и tarantool/main.go ProductTuple, продублирован
// здесь: пакеты `main` в Go не импортируются друг из друга, а контракт
// (схема space) один и тот же для обоих файлов.
type clTarantoolTuple struct {
	_msgpack   struct{} `msgpack:",asArray"` //nolint:unused
	ID         uint64
	SKU        string
	Title      string
	PriceCents uint64
	Category   string
	Views      uint64
}

// clTopEntry зеркалит {id=.., title=.., views=..} хранимки top_products_by_category
// (msgpack-теги) — тот же формат, что и на стороне Java TopEntry.toString(),
// откуда его же разбирает clParseIgniteTop (см. ниже), позволяя сравнивать
// топы Tarantool и Ignite напрямую как значения одного Go-типа.
type clTopEntry struct {
	ID    uint64 `msgpack:"id"`
	Title string `msgpack:"title"`
	Views uint64 `msgpack:"views"`
}

// ---------- сокетный счётчик байт для Tarantool (L2, внешнее ревью 18.07) ----------
//
// Внешнее ревью серии потребовало НАСТОЯЩИЕ байты по сети для
// compute-locality, а не число объектов (33 276 не равно байтам, пока
// байты не измерены отдельно). go-tarantool/v2 позволяет подставить свой
// Dialer (см. tarantool.Dialer, tarantool.NetDialer.Dial как образец в
// dial.go библиотеки) — здесь собран ТОТ ЖЕ стек оборачивателей
// (GreetingDialer -> ProtocolDialer -> AuthDialer), что и NetDialer.Dial,
// но вместо приватного netDialer библиотеки подставлен свой, оборачивающий
// net.Conn атомарным счётчиком прочитанных/записанных байт. Это и есть
// настоящий сокетный трафик — с IPROTO-заголовком фрейма и msgpack-
// конвертом ответа, а не только размер полезной нагрузки внутри него.
type clByteCounter struct {
	read    atomic.Int64
	written atomic.Int64
}

func (c *clByteCounter) snapshot() (readB, writtenB int64) {
	return c.read.Load(), c.written.Load()
}

// clCountingConn оборачивает net.Conn, считая байты, реально прошедшие
// через Read/Write — то есть байты фактических сисколов на сокет, ДО
// любой буферизации bufio на стороне клиента.
type clCountingConn struct {
	net.Conn
	counter *clByteCounter
}

func (c *clCountingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.counter.read.Add(int64(n))
	}
	return n, err
}

func (c *clCountingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.counter.written.Add(int64(n))
	}
	return n, err
}

// clDeadlineIO — минимальный аналог приватного deadlineIO библиотеки
// (dial.go): применяет per-op таймаут поверх net.Conn. Продублирован (не
// импортирован — тип библиотеки неэкспортирован), поведение идентично
// тому, что делает NetDialer для штатного пути.
type clDeadlineIO struct {
	to time.Duration
	c  net.Conn
}

func (d *clDeadlineIO) Write(b []byte) (int, error) {
	if d.to > 0 {
		_ = d.c.SetWriteDeadline(time.Now().Add(d.to))
	}
	return d.c.Write(b)
}

func (d *clDeadlineIO) Read(b []byte) (int, error) {
	if d.to > 0 {
		_ = d.c.SetReadDeadline(time.Now().Add(d.to))
	}
	return d.c.Read(b)
}

const clDialBufSize = 128 * 1024

// clCountingTarantoolConn реализует tarantool.Conn (Read/Write/Flush/
// Close/Greeting/ProtocolInfo/Addr) поверх буферизованного clDeadlineIO —
// тот же паттерн, что и приватный tntConn библиотеки (dial.go), с
// добавленным снизу счётчиком байт (clCountingConn).
type clCountingTarantoolConn struct {
	raw    *clCountingConn
	reader io.Reader
	writer *bufio.Writer
}

func (c *clCountingTarantoolConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *clCountingTarantoolConn) Write(p []byte) (int, error) { return c.writer.Write(p) }
func (c *clCountingTarantoolConn) Flush() error                { return c.writer.Flush() }
func (c *clCountingTarantoolConn) Close() error                { return c.raw.Conn.Close() }
func (c *clCountingTarantoolConn) Greeting() tarantool.Greeting {
	return tarantool.Greeting{}
}
func (c *clCountingTarantoolConn) ProtocolInfo() tarantool.ProtocolInfo {
	return tarantool.ProtocolInfo{}
}
func (c *clCountingTarantoolConn) Addr() net.Addr { return c.raw.Conn.RemoteAddr() }

// clCountingDialer — низкоуровневый Dialer (см. tarantool.Dialer в
// dial.go): открывает TCP-соединение сам (не через приватный netDialer
// библиотеки), оборачивает его в clCountingConn со счётчиком байт.
type clCountingDialer struct {
	address string
	counter *clByteCounter
}

func (d clCountingDialer) Dial(ctx context.Context, opts tarantool.DialOpts) (tarantool.Conn, error) {
	nd := net.Dialer{}
	nc, err := nd.DialContext(ctx, "tcp", d.address)
	if err != nil {
		return nil, fmt.Errorf("compute-locality: dial tarantool (%s): %w", d.address, err)
	}
	cc := &clCountingConn{Conn: nc, counter: d.counter}
	dio := &clDeadlineIO{to: opts.IoTimeout, c: cc}
	conn := &clCountingTarantoolConn{
		raw:    cc,
		reader: bufio.NewReaderSize(dio, clDialBufSize),
		writer: bufio.NewWriterSize(dio, clDialBufSize),
	}
	return conn, nil
}

// clConnectTarantool — то же самое подключение (адрес/логин/пароль/
// таймауты), что и раньше через tarantool.NetDialer, но нижний слой Dial
// заменён на clCountingDialer — стек AuthDialer(ProtocolDialer(
// GreetingDialer(...))) собран вручную, потому что NetDialer сам собирает
// именно этот стек вокруг СВОЕГО приватного netDialer (см. dial.go,
// NetDialer.Dial), а подставить туда чужой нижний слой библиотека не
// позволяет. Возвращает и соединение, и счётчик байт — снимается до/после
// каждой операции в clTarantoolNaiveTop/clCallStoredProc.
func clConnectTarantool(ctx context.Context, addr string) (*tarantool.Connection, *clByteCounter) {
	counter := &clByteCounter{}
	dialer := tarantool.AuthDialer{
		Dialer: tarantool.ProtocolDialer{
			Dialer: tarantool.GreetingDialer{
				Dialer: clCountingDialer{address: addr, counter: counter},
			},
		},
		Auth:     tarantool.ChapSha1Auth,
		Username: "app",
		Password: "app",
	}
	opts := tarantool.Opts{Timeout: 10 * time.Second}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := tarantool.Connect(cctx, dialer, opts)
	if err != nil {
		log.Fatalf("tarantool connect (%s): %v", addr, err)
	}
	return conn, counter
}

// clEnsureTarantoolLoaded — идемпотентная загрузка products в Tarantool: тот
// же паттерн, что tarantool/main.go loadIntoSpace (продублирован по той же
// причине — раздельные main-пакеты). Пропускает загрузку, если число
// кортежей в space уже совпадает с PG (повторный прогон сценария не должен
// перезаливать 200 000 записей заново).
func clEnsureTarantoolLoaded(conn *tarantool.Connection, rows []clPgProduct) {
	var lenOut []uint64
	if err := conn.Do(tarantool.NewEvalRequest("return box.space.products:len()")).GetTyped(&lenOut); err != nil {
		log.Fatalf("compute-locality: box.space.products:len(): %v", err)
	}
	if len(lenOut) > 0 && lenOut[0] == uint64(len(rows)) {
		fmt.Printf("compute-locality: tarantool products уже загружено (%d кортежей) — пропускаю заливку\n", lenOut[0])
		return
	}
	fmt.Printf("compute-locality: tarantool products — заливаю %d строк из PG\n", len(rows))
	const batch = 2000
	for i := 0; i < len(rows); i += batch {
		end := i + batch
		if end > len(rows) {
			end = len(rows)
		}
		futures := make([]*tarantool.Future, 0, end-i)
		for _, p := range rows[i:end] {
			tuple := []interface{}{uint64(p.ID), p.SKU, p.Title, uint64(p.PriceCents), p.Category, uint64(p.Views)}
			req := tarantool.NewReplaceRequest("products").Tuple(tuple)
			futures = append(futures, conn.Do(req))
		}
		for _, f := range futures {
			if _, err := f.Get(); err != nil {
				log.Fatalf("compute-locality: replace в products: %v", err)
			}
		}
	}
}

// clNetBytes — сокетные байты ОДНОЙ операции (снято clByteCounter.snapshot()
// до и после), раздельно по направлениям: read — то, что клиент прочитал из
// сокета (ответ сервера, включая IPROTO-заголовок фрейма и msgpack-конверт),
// written — то, что клиент записал в сокет (запрос). total() — то, что
// сравнивается со счётом объектов (33 276 против 10) как настоящий трафик.
type clNetBytes struct {
	read    int64
	written int64
}

func (b clNetBytes) total() int64 { return b.read + b.written }

// clCallStoredProc — ЛОКАЛЬНЫЙ путь: вызывает контрактную хранимку
// top_products_by_category (Task 3, tarantool/init.lua), не изменяет её.
// Возвращает топ, время ОДНОГО round-trip CALL и сокетные байты этого же
// round-trip (снято clByteCounter до/после — см. clConnectTarantool, L2
// ревью 18.07: раньше "объём по сети" в статье опирался только на число
// объектов, не на измеренные байты).
func clCallStoredProc(conn *tarantool.Connection, counter *clByteCounter, category string, limit int) ([]clTopEntry, time.Duration, clNetBytes) {
	r0, w0 := counter.snapshot()
	start := time.Now()
	var procResult [][]clTopEntry
	err := conn.Do(tarantool.NewCallRequest("top_products_by_category").
		Args([]interface{}{category, limit}),
	).GetTyped(&procResult)
	elapsed := time.Since(start)
	r1, w1 := counter.snapshot()
	if err != nil {
		log.Fatalf("compute-locality: CALL top_products_by_category(%q, %d): %v", category, limit, err)
	}
	if len(procResult) == 0 {
		log.Fatalf("АССЕРТ: пустой ответ CALL top_products_by_category(%q, %d)", category, limit)
	}
	return procResult[0], elapsed, clNetBytes{read: r1 - r0, written: w1 - w0}
}

// clTarantoolNaiveTop — НАИВНЫЙ путь: один SELECT вычитывает ВСЮ категорию по
// сети, топ считается здесь, в Go (см. брифа Step 1: "вычитать все товары
// категории в приложение, отсортировать в Go/Java" — дословно то же самое,
// что и tarantool/main.go clientTopByCategory, продублировано по той же
// причине раздельных main-пакетов). Тай-брейк — PK DESCENDING, ЗЕРКАЛЬНО
// повторяет то, что реально делает REQ-итератор хранимки на равных views
// (задокументировано в tarantool/main.go, комментарий над
// clientTopByCategory, и task-3-report.md) — без этого сравнение топов на
// границе ничьей (limit=9) расходилось бы по МНОЖЕСТВУ, не только по
// порядку. При limit=10 (проба этого сценария) сама ничья не влияет на
// состав топа (см. crossCheckTieBreak) — тай-брейк здесь сохранён дословно
// ради того, чтобы naiveTop оставался ПОБИТОВО идентичен localTop, а не
// только "тем же набором id".
// ПОСЛЕ РЕВЬЮ (P1, см. task-9-report.md, "Фикс таймера наивного пути"):
// раньше elapsed фиксировался СРАЗУ после GetTyped, а сортировка (и отбор
// top-N) шли уже ЗА пределами замеряемого участка — наивный путь получался
// недомерен: он не платил за работу, которая ему объективно нужна, чтобы
// получить тот же результат (топ-N), что и хранимка. Локальный путь
// (clCallStoredProc) уже возвращает готовый топ end-to-end, его трогать не
// нужно. Здесь elapsed теперь остановлен ПОСЛЕ сортировки и построения top —
// наивный путь end-to-end, как и заявлено в статье.
// Возвращает, дополнительно к прежнему (топ/число объектов/время), сокетные
// байты ЭТОГО SELECT (clNetBytes — см. clCallStoredProc, тот же приём).
func clTarantoolNaiveTop(conn *tarantool.Connection, counter *clByteCounter, category string, limit int) ([]clTopEntry, int, time.Duration, clNetBytes) {
	r0, w0 := counter.snapshot()
	start := time.Now()
	var wholeCategory []clTarantoolTuple
	err := conn.Do(tarantool.NewSelectRequest("products").
		Index("category").
		Iterator(tarantool.IterEq).
		Key([]interface{}{category}).
		Limit(0xFFFFFFFF),
	).GetTyped(&wholeCategory)
	if err != nil {
		log.Fatalf("compute-locality: select всей категории %q: %v", category, err)
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
	top := make([]clTopEntry, 0, limit)
	for _, t := range wholeCategory[:limit] {
		top = append(top, clTopEntry{ID: t.ID, Title: t.Title, Views: t.Views})
	}
	elapsed := time.Since(start)
	r1, w1 := counter.snapshot()
	return top, len(wholeCategory), elapsed, clNetBytes{read: r1 - r0, written: w1 - w0}
}

// clTimingReps — живой прогон вскрыл (не выдумано): первая версия измеряла
// локальный путь (CALL хранимки, ~0.5мс) ОДИН раз за проход — медианы не
// было вообще. Единичный замер настолько короткого пути оказался таким,
// что джиттер Docker Desktop (та же задокументированная нестабильность,
// что и "Причина разброса" в разделе про Стенд 8/working-set этого README)
// целиком доминирует над сигналом. Диагностика (throwaway-проба, protokoll
// — task-9-report.md): 20 интерливинг-повторов показали, что CALL хранимки
// распределён БИМОДАЛЬНО — примерно поровну между ~520мкс и ~1000мкс
// (вероятно, две ветки TCP-стека WSL2/Docker Desktop — с задержкой ACK и
// без), а не гладким шумом вокруг одного значения. При ОДНОМ замере
// результат произвольно попадает в ту или иную моду в зависимости от того,
// какая из двух мод "выпала" в этот раз — это и объясняло
// ratio(naive-first)=17.71 против ratio(local-first)=38.83 на ОДНОМ и том
// же соединении: дважды подряд срабатывал АССЕРТ Step 2, но причина НЕ
// системная зависимость от порядка, а недостаточная выборка (N=1)
// бимодального сигнала.
//
// ВАЖНО о силе этого утверждения. Две моды наблюдались в ОТДЕЛЬНОЙ
// диагностической пробе, которая НЕ сохранена в репозитории, а сам код
// печатает только медиану, не сохраняя 51 отсчёт. Поэтому ни две моды, ни
// их близкие веса читатель проверить не может — это наблюдение, а не
// опубликованный факт.
//
// N=51 подобрано эмпирически: на трёх пробах разброс между порядками был
// <3% (ratio 33.16 vs 33.94, 33.55 vs 33.21, 31.53 vs 31.82). Но это НЕ
// означает, что медиана "сходится": при двух модах с близкими весами она
// сама переключается между ними от прогона к прогону. Больший N лишь
// уменьшает чувствительность к ОДНОМУ случайному отсчёту — платформенную
// нестабильность он не устраняет, и опубликованный разброс отношения
// (53,26-122,98 у Tarantool) это прямо показывает. Нечётное число — чтобы
// медиана была однозначной, без усреднения соседних. См. clMedianDuration.
const clTimingReps = 51

func clMedianDuration(durs []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), durs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// clMedianInt64/clMinMaxInt64 — тот же приём медианы (clTimingReps
// повторов, см. выше), применённый к байтам вместо длительности: короткий
// путь может дать джиттер и по байтам (TCP-сегментация, ретрансмиты на
// платформе — см. README "Границы метода"), поэтому байты тоже берутся не
// по одному замеру, а по clTimingReps повторам внутри прохода.
func clMedianInt64(vals []int64) int64 {
	sorted := append([]int64(nil), vals...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

func clMinMaxInt64(vals []int64) (lo, hi int64) {
	lo, hi = vals[0], vals[0]
	for _, v := range vals[1:] {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	return lo, hi
}

// clTarantoolPass — один прогон обоих путей Tarantool в заданном порядке.
// naiveFirst=true — наивный путь измеряется первым (порядок кода "как есть"
// в остальной серии); naiveFirst=false — локальный путь первым (Step 2
// брифа, защита от смещения порядка на прогретых кэшах). Каждый путь внутри
// прохода замеряется clTimingReps раз, публикуется МЕДИАНА (см.
// clTimingReps) — топ/число объектов по сети берутся с ПОСЛЕДНЕГО повтора
// (детерминированы, от повтора к повтору не меняются). Байты (naiveBytes*/
// localBytes*, L2 ревью 18.07) собраны ПО ТОЙ ЖЕ схеме: медиана + диапазон
// (min/max) из тех же clTimingReps повторов, снятые clByteCounter вокруг
// каждого отдельного вызова (см. clCallStoredProc/clTarantoolNaiveTop).
type clTarantoolPass struct {
	order        string
	naiveTop     []clTopEntry
	localTop     []clTopEntry
	naiveObjects int
	naiveDur     time.Duration
	localDur     time.Duration

	naiveBytesMedian int64 // read+written, медиана
	naiveBytesLo     int64
	naiveBytesHi     int64
	localBytesMedian int64
	localBytesLo     int64
	localBytesHi     int64
}

func clRunTarantoolPass(conn *tarantool.Connection, counter *clByteCounter, category string, limit int, naiveFirst bool) clTarantoolPass {
	var p clTarantoolPass
	naiveDurs := make([]time.Duration, clTimingReps)
	localDurs := make([]time.Duration, clTimingReps)
	naiveBytes := make([]int64, clTimingReps)
	localBytes := make([]int64, clTimingReps)
	if naiveFirst {
		p.order = "naive-first"
		for i := 0; i < clTimingReps; i++ {
			var nb clNetBytes
			p.naiveTop, p.naiveObjects, naiveDurs[i], nb = clTarantoolNaiveTop(conn, counter, category, limit)
			naiveBytes[i] = nb.total()
		}
		for i := 0; i < clTimingReps; i++ {
			var lb clNetBytes
			p.localTop, localDurs[i], lb = clCallStoredProc(conn, counter, category, limit)
			localBytes[i] = lb.total()
		}
	} else {
		p.order = "local-first"
		for i := 0; i < clTimingReps; i++ {
			var lb clNetBytes
			p.localTop, localDurs[i], lb = clCallStoredProc(conn, counter, category, limit)
			localBytes[i] = lb.total()
		}
		for i := 0; i < clTimingReps; i++ {
			var nb clNetBytes
			p.naiveTop, p.naiveObjects, naiveDurs[i], nb = clTarantoolNaiveTop(conn, counter, category, limit)
			naiveBytes[i] = nb.total()
		}
	}
	p.naiveDur = clMedianDuration(naiveDurs)
	p.localDur = clMedianDuration(localDurs)
	p.naiveBytesMedian = clMedianInt64(naiveBytes)
	p.naiveBytesLo, p.naiveBytesHi = clMinMaxInt64(naiveBytes)
	p.localBytesMedian = clMedianInt64(localBytes)
	p.localBytesLo, p.localBytesHi = clMinMaxInt64(localBytes)
	return p
}

// ---------- Ignite: подпроцесс ignite-stand.jar -scenario compute ----------

// clIgniteJOpts — официальный набор add-opens Ignite для JDK15+
// (apacheignite/ignite:2.18.0, bin/include/jvmdefaults.sh), тот же, что в
// README "Как воспроизвести" — не выдуман здесь заново.
const clIgniteJOpts = "--add-opens=java.base/jdk.internal.access=ALL-UNNAMED --add-opens=java.base/jdk.internal.misc=ALL-UNNAMED --add-opens=java.base/sun.nio.ch=ALL-UNNAMED --add-opens=java.base/sun.util.calendar=ALL-UNNAMED --add-opens=java.management/com.sun.jmx.mbeanserver=ALL-UNNAMED --add-opens=jdk.internal.jvmstat/sun.jvmstat.monitor=ALL-UNNAMED --add-opens=java.base/sun.reflect.generics.reflectiveObjects=ALL-UNNAMED --add-opens=jdk.management/com.sun.management.internal=ALL-UNNAMED --add-opens=java.base/java.io=ALL-UNNAMED --add-opens=java.base/java.nio=ALL-UNNAMED --add-opens=java.base/java.net=ALL-UNNAMED --add-opens=java.base/java.util=ALL-UNNAMED --add-opens=java.base/java.util.concurrent=ALL-UNNAMED --add-opens=java.base/java.util.concurrent.locks=ALL-UNNAMED --add-opens=java.base/java.util.concurrent.atomic=ALL-UNNAMED --add-opens=java.base/java.lang=ALL-UNNAMED --add-opens=java.base/java.lang.invoke=ALL-UNNAMED --add-opens=java.base/java.math=ALL-UNNAMED --add-opens=java.sql/java.sql=ALL-UNNAMED --add-opens=java.base/java.lang.reflect=ALL-UNNAMED --add-opens=java.base/java.time=ALL-UNNAMED --add-opens=java.base/java.text=ALL-UNNAMED --add-opens=java.management/sun.management=ALL-UNNAMED"

type clIgniteResult struct {
	order              string
	categorySizePG     int
	naiveOverNetwork   int
	computeOverNetwork int
	naiveMs            float64
	computeMs          float64
	naiveTop           []clTopEntry
	computeTop         []clTopEntry

	// Байты comm SPI (L2, внешнее ревью 18.07) — см. IgniteStand.java,
	// commSpiFinal. commSpiAvailable=false — TcpCommunicationSpi не
	// резолвился в этом прогоне (честно зафиксировано, не подменено нулём).
	commSpiAvailable     bool
	naiveSentBytes       int64
	naiveReceivedBytes   int64
	computeSentBytes     int64
	computeReceivedBytes int64
}

var (
	clReCategorySize = regexp.MustCompile(`compute: товаров в категории \(PostgreSQL, источник истины\)=(\d+)`)
	clReOverNetwork  = regexp.MustCompile(`compute: объектов уехало по сети — наивно=(\d+), compute=(\d+)`)
	clReTimes        = regexp.MustCompile(`compute: время_наивно_мс=([\d.]+) время_compute_мс=([\d.]+)`)
	clReTopLine      = regexp.MustCompile(`^\s*#\d+\s+id=(\d+)\s+title=(.+)\s+views=(\d+)\s*$`)
	clReCommBytes    = regexp.MustCompile(`compute: байты_comm_spi_медиана наивно_sent=(\d+) наивно_received=(\d+) compute_sent=(\d+) compute_received=(\d+)`)
)

// clRunIgniteCompute — запускает ТОТ ЖЕ ignite-stand.jar (Стенд 6, Task 6),
// тем же способом, что и README "Как воспроизвести" (docker run --network
// inmemory-net ... eclipse-temurin:21 java $JOPTS -jar target/ignite-stand.jar
// -scenario compute), плюс новый -order (см. заголовок файла, "Отклонение от
// брифа №1"). Парсит stdout сценария вместо повторной реализации протокола
// Ignite в Go — см. "Отклонение от брифа №2" в заголовке файла.
func clRunIgniteCompute(igniteDirAbs, order, originDSNInNet, igniteDiscoveryAddr string) clIgniteResult {
	args := []string{
		"run", "--rm", "--network", "inmemory-net",
		"-e", "ORIGIN_DSN=" + originDSNInNet,
		"-e", "IGNITE_DISCOVERY_ADDR=" + igniteDiscoveryAddr,
		"-v", igniteDirAbs + ":/app",
		"-w", "/app",
		"eclipse-temurin:21",
		"java",
	}
	args = append(args, strings.Fields(clIgniteJOpts)...)
	args = append(args, "-DIGNITE_QUIET=true", "-jar", "target/ignite-stand.jar", "-scenario", "compute", "-order", order)

	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("compute-locality: docker run ignite-stand.jar -scenario compute -order %s: %v\n--- вывод ---\n%s", order, err, string(out))
	}
	return clParseIgniteOutput(order, string(out))
}

func clParseIgniteOutput(order, out string) clIgniteResult {
	res := clIgniteResult{order: order}
	m := clReCategorySize.FindStringSubmatch(out)
	if m == nil {
		log.Fatalf("compute-locality: не найдена строка 'товаров в категории (PostgreSQL...)' в выводе ignite-stand.jar (order=%s):\n%s", order, out)
	}
	res.categorySizePG, _ = strconv.Atoi(m[1])

	m = clReOverNetwork.FindStringSubmatch(out)
	if m == nil {
		log.Fatalf("compute-locality: не найдена строка 'объектов уехало по сети' в выводе ignite-stand.jar (order=%s):\n%s", order, out)
	}
	res.naiveOverNetwork, _ = strconv.Atoi(m[1])
	res.computeOverNetwork, _ = strconv.Atoi(m[2])

	m = clReTimes.FindStringSubmatch(out)
	if m == nil {
		log.Fatalf("compute-locality: не найдена строка 'время_наивно_мс' в выводе ignite-stand.jar (order=%s):\n%s", order, out)
	}
	res.naiveMs, _ = strconv.ParseFloat(m[1], 64)
	res.computeMs, _ = strconv.ParseFloat(m[2], 64)

	res.naiveTop = clParseIgniteTop(out, "compute: топ клиента (naive)")
	res.computeTop = clParseIgniteTop(out, "compute: топ affinity-задачи (TopByCategoryTask)")

	// Байты comm SPI (L2, внешнее ревью 18.07) — см. IgniteStand.java,
	// scenarioCompute/commSpiFinal. Если TcpCommunicationSpi не резолвился
	// (напечатано явное ПРЕДУПРЕЖДЕНИЕ на java-стороне), строка с байтами
	// отсутствует — это НЕ ошибка парсинга, честно фиксируем
	// commSpiAvailable=false и продолжаем (объекты/время всё равно измерены).
	if m = clReCommBytes.FindStringSubmatch(out); m != nil {
		res.commSpiAvailable = true
		res.naiveSentBytes, _ = strconv.ParseInt(m[1], 10, 64)
		res.naiveReceivedBytes, _ = strconv.ParseInt(m[2], 10, 64)
		res.computeSentBytes, _ = strconv.ParseInt(m[3], 10, 64)
		res.computeReceivedBytes, _ = strconv.ParseInt(m[4], 10, 64)
	} else if !strings.Contains(out, "ПРЕДУПРЕЖДЕНИЕ — TcpCommunicationSpi недоступен") {
		log.Fatalf("compute-locality: ни строка байт comm SPI, ни явное предупреждение о недоступности TcpCommunicationSpi не найдены в выводе ignite-stand.jar (order=%s) — формат вывода разошёлся с regex clReCommBytes:\n%s", order, out)
	}

	if !strings.Contains(out, "OK: сценарий compute завершён без ошибок") {
		log.Fatalf("compute-locality: ignite-stand.jar -scenario compute -order %s не напечатал финальный OK (сценарий мог упасть на ассерте java-стороны):\n%s", order, out)
	}
	return res
}

// clParseIgniteTop разбирает блок вида
//
//	compute: топ клиента (naive)
//	  #1 id=84 title=... views=93
//	  ...
//
// из stdout ignite-stand.jar (формат печати — IgniteStand.TopEntry.toString()).
func clParseIgniteTop(out, header string) []clTopEntry {
	var top []clTopEntry
	inSection := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, header) {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		m := clReTopLine.FindStringSubmatch(line)
		if m == nil {
			break
		}
		id, _ := strconv.ParseUint(m[1], 10, 64)
		views, _ := strconv.ParseUint(m[3], 10, 64)
		top = append(top, clTopEntry{ID: id, Title: strings.TrimSpace(m[2]), Views: views})
	}
	return top
}

// ---------- сверка id (множество, не порядок) ----------

func clSortedIDs(top []clTopEntry) []uint64 {
	ids := make([]uint64, len(top))
	for i, t := range top {
		ids[i] = t.ID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// ---------- сценарий compute-locality ----------

func scenarioComputeLocality(ctx context.Context, originDSN, originDSNInNet, tarantoolAddr, igniteDir, igniteDiscoveryAddr string, swapOrder bool) {
	fmt.Printf("compute-locality: category=%q limit=%d — Task 9, \"цена выноса вычислений к данным\"\n", clProbeCategory, clProbeLimit)
	fmt.Printf("compute-locality: swap-order=%v (порядок исполнения двух проходов внутри этого запуска)\n", swapOrder)

	pool := connectOrigin(ctx, originDSN)
	defer pool.Close()
	rows := clLoadProductsFromPG(ctx, pool)
	var categorySizeGo int
	for _, r := range rows {
		if r.Category == clProbeCategory {
			categorySizeGo++
		}
	}
	fmt.Printf("compute-locality: PostgreSQL products=%d, категория %q=%d (источник истины для обеих систем)\n",
		len(rows), clProbeCategory, categorySizeGo)

	// ---------- Tarantool: два прохода, порядки наивно-первым/локально-первым ----------
	conn, tBytesCounter := clConnectTarantool(ctx, tarantoolAddr)
	defer conn.Close()
	clEnsureTarantoolLoaded(conn, rows)

	// Прогрев (живой прогон вскрыл это, не выдумано): первый вызов СРАЗУ
	// после массовой заливки (или после свежего TCP-коннекта) систематически
	// медленнее любого следующего — не интринзик naive-vs-local, а холодный
	// старт соединения/страничного кеша. Живой прогон без прогрева дал
	// ratio(naive-first)=24.35 против ratio(local-first)=48.88 на ОДНОМ и том
	// же соединении сразу после загрузки 200 000 строк — сработал ассерт
	// Step 2 (смещение >50%), и справедливо: без прогрева порядок реально
	// решал результат. Один непомерянный проход в КАЖДОМ порядке (наивно,
	// затем локально) устраняет холодный старт перед двумя ИЗМЕРЯЕМЫМИ
	// проходами ниже — сам холодный старт НЕ публикуется, только используется
	// для стабилизации.
	fmt.Println("compute-locality: tarantool — прогрев (1 непомеренный проход в каждом порядке, гасит холодный старт после заливки)")
	_, _, _, _ = clTarantoolNaiveTop(conn, tBytesCounter, clProbeCategory, clProbeLimit)
	_, _, _ = clCallStoredProc(conn, tBytesCounter, clProbeCategory, clProbeLimit)

	firstNaiveFirst := !swapOrder
	tPassA := clRunTarantoolPass(conn, tBytesCounter, clProbeCategory, clProbeLimit, firstNaiveFirst)
	tPassB := clRunTarantoolPass(conn, tBytesCounter, clProbeCategory, clProbeLimit, !firstNaiveFirst)

	for _, p := range []clTarantoolPass{tPassA, tPassB} {
		// Оба пути обязаны дать одинаковый топ — иначе сравниваются разные
		// вычисления, и любое отношение бессмысленно (брифа Step 1).
		if !reflect.DeepEqual(p.naiveTop, p.localTop) {
			log.Fatalf("АССЕРТ: tarantool (%s) топ наивный != топ локальный:\n naive=%v\n local=%v", p.order, p.naiveTop, p.localTop)
		}
		// Локальный путь обязан тащить по сети существенно меньше.
		if len(p.localTop) >= p.naiveObjects {
			log.Fatalf("АССЕРТ: tarantool (%s) локально уехало %d объектов, наивно %d — локальности нет", p.order, len(p.localTop), p.naiveObjects)
		}
	}
	tRatioA := tPassA.naiveDur.Seconds() / tPassA.localDur.Seconds()
	tRatioB := tPassB.naiveDur.Seconds() / tPassB.localDur.Seconds()
	// Отношение обязано сохраниться при смене порядка (брифа Step 2).
	if math.Abs(tRatioA-tRatioB)/tRatioA > 0.5 {
		log.Fatalf("АССЕРТ: tarantool отношение зависит от порядка (%s=%.2f против %s=%.2f) — смещение прогрева", tPassA.order, tRatioA, tPassB.order, tRatioB)
	}

	fmt.Printf("compute-locality: tarantool объектов по сети — наивно=%d, локально(хранимка)=%d (в %.1fx меньше)\n",
		tPassA.naiveObjects, len(tPassA.localTop), float64(tPassA.naiveObjects)/float64(len(tPassA.localTop)))
	fmt.Printf("compute-locality: tarantool round-trip — наивно=1 (один SELECT всей категории), локально=1 (один CALL хранимки); различие НЕ в числе round-trip, а в объёме одного round-trip\n")
	fmt.Printf("ОТНОШЕНИЕ tarantool order=%s naive_ms=%.3f local_ms=%.3f ratio=%.2f\n",
		tPassA.order, tPassA.naiveDur.Seconds()*1000, tPassA.localDur.Seconds()*1000, tRatioA)
	fmt.Printf("ОТНОШЕНИЕ tarantool order=%s naive_ms=%.3f local_ms=%.3f ratio=%.2f\n",
		tPassB.order, tPassB.naiveDur.Seconds()*1000, tPassB.localDur.Seconds()*1000, tRatioB)

	// БАЙТЫ ПО СЕТИ (L2, внешнее ревью 18.07) — настоящий сокетный трафик
	// (см. clByteCounter/clCountingDialer выше), не число объектов. Медиана
	// + диапазон (min/max) из clTimingReps повторов КАЖДОГО прохода —
	// см. clRunTarantoolPass.
	tBytesRatioA := float64(tPassA.naiveBytesMedian) / float64(tPassA.localBytesMedian)
	tBytesRatioB := float64(tPassB.naiveBytesMedian) / float64(tPassB.localBytesMedian)
	fmt.Printf("compute-locality: tarantool БАЙТЫ по сети (сокет, read+written, медиана из %d повторов) order=%s: наивно=%d [%d..%d], локально=%d [%d..%d], ratio_bytes=%.1f (для сравнения ratio_объектов=%.1f)\n",
		clTimingReps, tPassA.order,
		tPassA.naiveBytesMedian, tPassA.naiveBytesLo, tPassA.naiveBytesHi,
		tPassA.localBytesMedian, tPassA.localBytesLo, tPassA.localBytesHi,
		tBytesRatioA, float64(tPassA.naiveObjects)/float64(len(tPassA.localTop)))
	fmt.Printf("compute-locality: tarantool БАЙТЫ по сети (сокет, read+written, медиана из %d повторов) order=%s: наивно=%d [%d..%d], локально=%d [%d..%d], ratio_bytes=%.1f\n",
		clTimingReps, tPassB.order,
		tPassB.naiveBytesMedian, tPassB.naiveBytesLo, tPassB.naiveBytesHi,
		tPassB.localBytesMedian, tPassB.localBytesLo, tPassB.localBytesHi,
		tBytesRatioB)

	// ---------- Ignite: два прохода через подпроцесс ignite-stand.jar ----------
	igniteDirAbs, err := filepath.Abs(igniteDir)
	if err != nil {
		log.Fatalf("compute-locality: filepath.Abs(%q): %v", igniteDir, err)
	}
	igOrderA, igOrderB := "naive-first", "local-first"
	if swapOrder {
		igOrderA, igOrderB = "local-first", "naive-first"
	}
	igA := clRunIgniteCompute(igniteDirAbs, igOrderA, originDSNInNet, igniteDiscoveryAddr)
	igB := clRunIgniteCompute(igniteDirAbs, igOrderB, originDSNInNet, igniteDiscoveryAddr)

	for _, r := range []clIgniteResult{igA, igB} {
		if !reflect.DeepEqual(r.naiveTop, r.computeTop) {
			log.Fatalf("АССЕРТ: ignite (%s) топ наивный != топ локальный:\n naive=%v\n local=%v", r.order, r.naiveTop, r.computeTop)
		}
		if r.computeOverNetwork >= r.naiveOverNetwork {
			log.Fatalf("АССЕРТ: ignite (%s) локально уехало %d объектов, наивно %d — локальности нет", r.order, r.computeOverNetwork, r.naiveOverNetwork)
		}
		if r.categorySizePG != categorySizeGo {
			log.Fatalf("АССЕРТ: ignite (%s) видит категорию %q размером %d, Go насчитал %d по тому же PostgreSQL", r.order, clProbeCategory, r.categorySizePG, categorySizeGo)
		}
	}
	igRatioA := igA.naiveMs / igA.computeMs
	igRatioB := igB.naiveMs / igB.computeMs
	if math.Abs(igRatioA-igRatioB)/igRatioA > 0.5 {
		log.Fatalf("АССЕРТ: ignite отношение зависит от порядка (%s=%.2f против %s=%.2f) — смещение прогрева", igA.order, igRatioA, igB.order, igRatioB)
	}

	fmt.Printf("compute-locality: ignite объектов по сети — наивно=%d, локально(compute)=%d (в %.1fx меньше)\n",
		igA.naiveOverNetwork, igA.computeOverNetwork, float64(igA.naiveOverNetwork)/float64(igA.computeOverNetwork))
	fmt.Printf("compute-locality: ignite round-trip — наивно=1 (один ScanQuery), локально=1 (один affinityCall); различие НЕ в числе round-trip, а в объёме одного round-trip\n")
	fmt.Printf("ОТНОШЕНИЕ ignite order=%s naive_ms=%.3f compute_ms=%.3f ratio=%.2f\n", igA.order, igA.naiveMs, igA.computeMs, igRatioA)
	fmt.Printf("ОТНОШЕНИЕ ignite order=%s naive_ms=%.3f compute_ms=%.3f ratio=%.2f\n", igB.order, igB.naiveMs, igB.computeMs, igRatioB)

	// БАЙТЫ ПО СЕТИ ignite (L2, внешнее ревью 18.07) — метрики
	// TcpCommunicationSpi classic client-node (см. IgniteStand.java,
	// commSpiFinal), медиана из TIMING_REPS=51 повторов на java-стороне (тот
	// же приём, что и Tarantool выше). commSpiAvailable=false — SPI не
	// резолвился в этом прогоне, байты честно не публикуются (не нулём).
	if igA.commSpiAvailable {
		igNaiveTotalA := igA.naiveSentBytes + igA.naiveReceivedBytes
		igLocalTotalA := igA.computeSentBytes + igA.computeReceivedBytes
		igBytesRatioA := float64(igNaiveTotalA) / float64(igLocalTotalA)
		fmt.Printf("compute-locality: ignite БАЙТЫ по сети (TcpCommunicationSpi, sent+received, медиана из 51 повтора) order=%s: наивно=%d (sent=%d received=%d), локально=%d (sent=%d received=%d), ratio_bytes=%.1f (для сравнения ratio_объектов=%.1f)\n",
			igA.order, igNaiveTotalA, igA.naiveSentBytes, igA.naiveReceivedBytes,
			igLocalTotalA, igA.computeSentBytes, igA.computeReceivedBytes,
			igBytesRatioA, float64(igA.naiveOverNetwork)/float64(igA.computeOverNetwork))
	}
	if igB.commSpiAvailable {
		igNaiveTotalB := igB.naiveSentBytes + igB.naiveReceivedBytes
		igLocalTotalB := igB.computeSentBytes + igB.computeReceivedBytes
		igBytesRatioB := float64(igNaiveTotalB) / float64(igLocalTotalB)
		fmt.Printf("compute-locality: ignite БАЙТЫ по сети (TcpCommunicationSpi, sent+received, медиана из 51 повтора) order=%s: наивно=%d (sent=%d received=%d), локально=%d (sent=%d received=%d), ratio_bytes=%.1f\n",
			igB.order, igNaiveTotalB, igB.naiveSentBytes, igB.naiveReceivedBytes,
			igLocalTotalB, igB.computeSentBytes, igB.computeReceivedBytes,
			igBytesRatioB)
	}
	if !igA.commSpiAvailable || !igB.commSpiAvailable {
		fmt.Println("compute-locality: ПРЕДУПРЕЖДЕНИЕ — TcpCommunicationSpi недоступен на ignite-стороне минимум в одном из проходов, байты по сети для ignite в этом прогоне НЕ измерены")
	}

	// ---------- главное публикуемое число ----------
	fmt.Println()
	fmt.Printf("compute-locality: ГЛАВНОЕ ЧИСЛО — объектов по сети (наивно/локально): tarantool=%d/%d, ignite=%d/%d\n",
		tPassA.naiveObjects, len(tPassA.localTop), igA.naiveOverNetwork, igA.computeOverNetwork)
	if tPassA.naiveObjects != igA.naiveOverNetwork {
		fmt.Printf("compute-locality: ПРЕДУПРЕЖДЕНИЕ — размер категории разошёлся между Tarantool (%d) и Ignite (%d) при одном PostgreSQL-источнике\n",
			tPassA.naiveObjects, igA.naiveOverNetwork)
	}
	// БАЙТЫ ПО СЕТИ — итоговое сравнение отношения байт с отношением
	// объектов (3327,6×, см. FIXTURES) для обеих систем разом. Это и есть
	// содержательный результат L2 (внешнее ревью 18.07): число объектов
	// (33 276/10) НЕ равно числу байт, пока байты не измерены отдельно —
	// у сериализации есть накладные расходы, разные у наивного/локального
	// ответа.
	fmt.Printf("compute-locality: ГЛАВНОЕ ЧИСЛО (байты) — tarantool: наивно=%d локально=%d ratio_bytes=%.1f (ratio_объектов=%.1f); ignite: %s\n",
		tPassA.naiveBytesMedian, tPassA.localBytesMedian, tBytesRatioA,
		float64(tPassA.naiveObjects)/float64(len(tPassA.localTop)),
		func() string {
			if !igA.commSpiAvailable {
				return "байты НЕ измерены (TcpCommunicationSpi недоступен)"
			}
			igNaiveTotal := igA.naiveSentBytes + igA.naiveReceivedBytes
			igLocalTotal := igA.computeSentBytes + igA.computeReceivedBytes
			return fmt.Sprintf("наивно=%d локально=%d ratio_bytes=%.1f", igNaiveTotal, igLocalTotal, float64(igNaiveTotal)/float64(igLocalTotal))
		}())

	// ---------- сведение тай-брейков (Task 9, см. заголовок файла) ----------
	tIDs := clSortedIDs(tPassA.localTop)
	iIDs := clSortedIDs(igA.computeTop)
	if reflect.DeepEqual(tIDs, iIDs) {
		fmt.Printf("compute-locality: ТАЙ-БРЕЙКИ — множество id топ-%d СОВПАЛО между Tarantool (PK DESC на равных views) и Ignite (views DESC, затем id ASC): %v — разные правила тай-брейка не меняют состав топа на этой пробе\n",
			clProbeLimit, tIDs)
	} else {
		fmt.Printf("compute-locality: ТАЙ-БРЕЙКИ — РАСХОЖДЕНИЕ множества id между Tarantool %v и Ignite %v — правила тай-брейка ЗДЕСЬ не эквивалентны, см. README\n",
			tIDs, iIDs)
	}
	fmt.Println("compute-locality: все ассерты пройдены (топ naive==local на обеих системах, локальность строго меньше наивного, отношение устойчиво к смене порядка)")
}

// ---------- сценарий etcd-misuse (Task 10, "бенчмарк" сценарий 3) ----------
//
// Суть: подать на etcd ТОТ ЖЕ горячий профиль записи, что Redis держит
// штатно — не 50k РАЗНЫХ ключей единожды, а 50k ОПЕРАЦИЙ записи ПО
// ГОРЯЧЕМУ НАБОРУ из hotKeys ключей (по умолчанию 5000, т.е. каждый ключ
// перезаписывается ~10 раз), 1 КБ на значение. Это стандартный паттерн
// кэша/сессии/счётчика: одни и те же ключи обновляются раз за разом.
//
// Для Redis (SET перезаписывает значение НА МЕСТЕ) это НИЧЕГО не стоит —
// used_memory остаётся порядка hotKeys*1КБ независимо от числа перезаписей.
// Для etcd КАЖДАЯ перезапись — НОВАЯ MVCC-ревизия, и все старые ревизии
// физически хранятся в backend (bbolt-файле) до явной компакции (это
// задокументированное поведение etcd, не открытие этой задачи — см. Стенд 7,
// секция "revisions": там ЖЕ показано, что DbSizeInUse падает после
// Compact, а DbSize (физический файл) — только после Defragment). На
// перезаписываемом горячем профиле это означает, что размер базы растёт
// БЕЗ ГРАНИЦ, а не остаётся константным, как у Redis — и при квоте
// backend (--quota-backend-bytes, по умолчанию 2 ГиБ, здесь ИСКУССТВЕННО
// уменьшена до 64 МиБ через compose/etcd-misuse.yml, отдельный от
// compose/etcd.yml Стенда 7 контейнер) запись рано или поздно упирается в
// error "etcdserver: mvcc: database space exceeded" и весь кластер
// перестаёт принимать записи (поднимается alarm NOSPACE) — до explicит
// восстановления: Compact -> Defragment -> AlarmDisarm.
//
// ЭТО НЕ "etcd плохой" — это "etcd для другого": etcd проектировался для
// НЕБОЛЬШОГО, редко меняющегося координационного состояния (Стенд 7 —
// watch/lease/lock), а не для профиля с частыми перезаписями горячих
// ключей, для которого создан Redis.

const (
	misuseRedisKeyPrefix = "misuse:hot:"
	misuseEtcdKeyPrefix  = "/inmemory/misuse/hot/"
)

func misuseRedisKey(idx int) string { return misuseRedisKeyPrefix + strconv.Itoa(idx) }
func misuseEtcdKey(idx int) string  { return misuseEtcdKeyPrefix + strconv.Itoa(idx) }

// misuseValue — детерминированное псевдослучайное значение размера n,
// зависящее от ГЛОБАЛЬНОГО индекса операции op (не от индекса ключа в
// горячем наборе) — каждая перезапись одного и того же ключа получает
// РАЗНЫЙ контент, как в реальном профиле обновления счётчика/сессии, а не
// no-op повтор одного и того же среза байт.
func misuseValue(op, n int) []byte {
	r := rand.New(rand.NewSource(int64(op)))
	b := make([]byte, n)
	_, _ = r.Read(b) // math/rand.Rand.Read всегда возвращает nil error и заполняет весь срез
	return b
}

func connectEtcdMisuse(endpoint string) *clientv3.Client {
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{endpoint},
		DialTimeout: 5 * time.Second,
		// Logger: zap.NewNop() — этот сценарий НАРОЧНО доводит запись до
		// потока отказов (квота 64 МиБ, тысячи Put после первого NOSPACE),
		// клиентский grpc-интерсептор ретраев по умолчанию печатает
		// WARN-строку в stderr на КАЖДУЮ такую попытку — на живом прогоне
		// (50000 попыток) это тысячи строк шума поверх содержательного
		// вывода сценария (который сам считает и печатает попытки/ошибки).
		Logger: zap.NewNop(),
	})
	if err != nil {
		log.Fatalf("clientv3.New(%q): %v", endpoint, err)
	}
	return cli
}

func etcdStatusOnce(ctx context.Context, cli *clientv3.Client, endpoint string) (dbSize, dbSizeInUse int64) {
	st, err := cli.Status(ctx, endpoint)
	if err != nil {
		log.Fatalf("etcd Status(%s): %v", endpoint, err)
	}
	return st.DbSize, st.DbSizeInUse
}

// etcdStatusStable — тот же паттерн, что statusStable в etcd-coord/main.go
// (Стенд 7): dbSize/dbSizeInUse отдаются сервером не синхронно
// относительно завершения записи/компакции/дефрагментации, снимаем до
// устоя (3 одинаковых чтения подряд) вместо единичного снимка.
func etcdStatusStable(ctx context.Context, cli *clientv3.Client, endpoint, label string) (dbSize, dbSizeInUse int64) {
	const settleReads = 3
	const pollInterval = 300 * time.Millisecond
	const budget = 10 * time.Second

	deadline := time.Now().Add(budget)
	var lastSize, lastInUse int64 = -1, -1
	stableCount := 0
	for {
		size, inUse := etcdStatusOnce(ctx, cli, endpoint)
		if size == lastSize && inUse == lastInUse {
			stableCount++
		} else {
			stableCount = 1
		}
		lastSize, lastInUse = size, inUse
		if stableCount >= settleReads {
			return lastSize, lastInUse
		}
		if time.Now().After(deadline) {
			fmt.Printf("etcd-misuse: ПРЕДУПРЕЖДЕНИЕ — [%s] размер базы не стабилизировался за %s (последнее: dbSize=%d dbSizeInUse=%d)\n",
				label, budget, lastSize, lastInUse)
			return lastSize, lastInUse
		}
		time.Sleep(pollInterval)
	}
}

// writeMisuseRedis — пачка через pipeline (тот же приём, что
// writeProductsRedis), ключ = i % hotKeys (горячий набор перезаписывается
// по кругу), значение = misuseValue(i, valueSize).
func writeMisuseRedis(ctx context.Context, rdb *redis.Client, from, to, hotKeys, valueSize int) (attempted, failed int) {
	n := to - from
	if n <= 0 {
		return 0, 0
	}
	pipe := rdb.Pipeline()
	cmds := make([]*redis.StatusCmd, n)
	for j := 0; j < n; j++ {
		i := from + j
		cmds[j] = pipe.Set(ctx, misuseRedisKey(i%hotKeys), misuseValue(i, valueSize), 0)
	}
	_, _ = pipe.Exec(ctx) // ошибка Exec() агрегирует первую — реальные отказы считаем по каждой команде ниже
	for _, c := range cmds {
		if c.Err() != nil {
			failed++
		}
	}
	return n, failed
}

type etcdWriteResult struct {
	attempted     int
	failed        int
	firstFailIdx  int // -1, если отказов не было (глобальный индекс операции)
	firstFailErr  string
	firstFailSize int64 // dbSize В МОМЕНТ первого отказа
	alarmsAtFail  string
}

// writeMisuseEtcd — ПОСЛЕДОВАТЕЛЬНЫЕ Put, по одному за раз, СОЗНАТЕЛЬНО
// не пачкой/транзакцией: иначе "на какой записи наступил отказ" (бриф
// Step 2) перестаёт быть однозначным порядковым номером. Как только квота
// исчерпана и alarm NOSPACE поднят, КАЖДЫЙ следующий Put отказывает
// немедленно на стороне сервера без обращения к диску — цикл не
// замедляется после первого отказа, несмотря на последовательность.
func writeMisuseEtcd(ctx context.Context, cli *clientv3.Client, endpoint string, writeOps, hotKeys, valueSize int) etcdWriteResult {
	res := etcdWriteResult{firstFailIdx: -1}
	for i := 0; i < writeOps; i++ {
		res.attempted++
		key := misuseEtcdKey(i % hotKeys)
		val := misuseValue(i, valueSize)
		_, err := cli.Put(ctx, key, string(val))
		if err != nil {
			res.failed++
			if res.firstFailIdx == -1 {
				res.firstFailIdx = i
				res.firstFailErr = err.Error()
				size, _ := etcdStatusOnce(ctx, cli, endpoint)
				res.firstFailSize = size
				if alarms, aerr := cli.AlarmList(ctx); aerr == nil {
					var parts []string
					for _, a := range alarms.Alarms {
						parts = append(parts, fmt.Sprintf("member=%d alarm=%s", a.MemberID, a.Alarm))
					}
					res.alarmsAtFail = strings.Join(parts, ", ")
				}
			}
		}
	}
	return res
}

// misuseAlarmDisarmAll — снимает ВСЕ активные алярмы, вернувшиеся от
// AlarmList (а не наугад один тип/MemberID=0) — так же поступает `etcdctl
// alarm disarm` под капотом: перечисляет активные алярмы и снимает КАЖДЫЙ
// именно с тем MemberID, на котором он был поднят.
func misuseAlarmDisarmAll(ctx context.Context, cli *clientv3.Client) []string {
	list, err := cli.AlarmList(ctx)
	if err != nil {
		log.Fatalf("etcd AlarmList: %v", err)
	}
	var disarmed []string
	for _, a := range list.Alarms {
		if _, err := cli.AlarmDisarm(ctx, &clientv3.AlarmMember{MemberID: a.MemberID, Alarm: a.Alarm}); err != nil {
			log.Fatalf("etcd AlarmDisarm(member=%d, alarm=%s): %v", a.MemberID, a.Alarm, err)
		}
		disarmed = append(disarmed, fmt.Sprintf("member=%d alarm=%s", a.MemberID, a.Alarm))
	}
	return disarmed
}

func scenarioEtcdMisuse(ctx context.Context, redisAddr, etcdAddr string, hotKeys, writeOps, valueSize int) {
	rounds := (writeOps + hotKeys - 1) / hotKeys
	fmt.Printf("etcd-misuse: hot_keys=%d write_ops=%d value_size=%d байт (~%d перезаписей каждого ключа горячего набора) — Task 10, \"etcd под несвойственной нагрузкой\"\n",
		hotKeys, writeOps, valueSize, rounds)

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis PING (%s): %v", redisAddr, err)
	}

	cli := connectEtcdMisuse(etcdAddr)
	defer cli.Close()

	// Измеряем ДЕЛЬТУ used_memory Redis, не абсолютное число: контейнер
	// (compose/redis.yml) общий с другими сценариями этой серии, в нём уже
	// могут лежать чужие ключи с прошлых прогонов.
	redisMemBefore := redisUsedMemory(ctx, rdb)
	dbSizeBefore, dbSizeInUseBefore := etcdStatusStable(ctx, cli, etcdAddr, "etcd до записи")
	fmt.Printf("etcd-misuse: ДО записи — redis used_memory=%d, etcd dbSize=%d dbSizeInUse=%d\n", redisMemBefore, dbSizeBefore, dbSizeInUseBefore)

	// ---------- Step 1: тот же профиль на Redis (эталон "правильного инструмента") ----------
	const redisBatch = 1000
	var redisAttempted, redisErrors int
	for from := 0; from < writeOps; from += redisBatch {
		to := from + redisBatch
		if to > writeOps {
			to = writeOps
		}
		a, f := writeMisuseRedis(ctx, rdb, from, to, hotKeys, valueSize)
		redisAttempted += a
		redisErrors += f
	}
	redisMemAfter := redisUsedMemory(ctx, rdb)
	redisMemUsed := redisMemAfter - redisMemBefore
	fmt.Printf("etcd-misuse: redis — записано попыток=%d ошибок=%d, used_memory %d -> %d (Δ%+d байт)\n",
		redisAttempted, redisErrors, redisMemBefore, redisMemAfter, redisMemUsed)

	// Redis обязан принять всю нагрузку без ошибок — он для этого и есть.
	if redisErrors > 0 {
		log.Fatalf("АССЕРТ: Redis дал %d ошибок на профиле, который обязан держать", redisErrors)
	}

	// ---------- Step 1/2: тот же профиль на etcd (квота 64 МиБ) ----------
	wr := writeMisuseEtcd(ctx, cli, etcdAddr, writeOps, hotKeys, valueSize)
	fmt.Printf("etcd-misuse: etcd — попыток=%d ошибок=%d\n", wr.attempted, wr.failed)
	quotaHit := wr.firstFailIdx >= 0 && strings.Contains(wr.firstFailErr, "database space exceeded")
	if wr.firstFailIdx >= 0 {
		fmt.Printf("etcd-misuse: ПЕРВЫЙ ОТКАЗ на записи #%d из %d (ключ %s, раунд %d из ~%d по горячему набору), dbSize в момент отказа=%d\n",
			wr.firstFailIdx, writeOps, misuseEtcdKey(wr.firstFailIdx%hotKeys), wr.firstFailIdx/hotKeys, rounds, wr.firstFailSize)
		fmt.Printf("etcd-misuse: ТЕКСТ ОШИБКИ: %s\n", wr.firstFailErr)
		fmt.Printf("etcd-misuse: alarm list в момент отказа: %s\n", wr.alarmsAtFail)
	} else {
		fmt.Println("etcd-misuse: за весь профиль ни одной ошибки записи — квота этим профилем не достигнута")
	}

	dbSizeAfter, dbSizeInUseAfter := etcdStatusStable(ctx, cli, etcdAddr, "etcd после записи, ДО восстановления")
	fmt.Printf("etcd-misuse: ПОСЛЕ записи (до восстановления) — etcd dbSize=%d dbSizeInUse=%d\n", dbSizeAfter, dbSizeInUseAfter)
	ratio := math.Inf(1)
	if redisMemUsed > 0 {
		ratio = float64(dbSizeAfter) / float64(redisMemUsed)
	}
	fmt.Printf("etcd-misuse: РАЗНИЦА — redis Δused_memory=%d байт, etcd dbSize=%d байт (в %.1fx больше), etcd ошибок=%d\n",
		redisMemUsed, dbSizeAfter, ratio, wr.failed)

	// etcd на том же профиле обязан показать РАЗНИЦУ: либо ошибки, либо
	// многократно больший размер базы. Если разницы нет — профиль слишком
	// лёгкий, сценарий ничего не демонстрирует.
	if wr.failed == 0 && float64(dbSizeAfter) < 2*float64(redisMemUsed) {
		log.Fatalf("АССЕРТ: etcd не показал разницы (ошибок 0, база %d против %d у Redis) — профиль слишком лёгкий, сценарий недоказателен", dbSizeAfter, redisMemUsed)
	}

	// Квота обязана сработать — иначе мы не показали главное.
	if !quotaHit {
		log.Fatalf("АССЕРТ: квота 64 МБ не достигнута за %d записей (ошибок=%d) — поднять объём (-misuse-write-ops/MISUSE_WRITE_OPS)", writeOps, wr.failed)
	}

	// ---------- Step 3: восстановление — compact -> defrag -> alarm disarm ----------
	fmt.Println()
	fmt.Println("etcd-misuse: восстановление — Compact -> Defragment -> AlarmDisarm")

	statusResp, err := cli.Status(ctx, etcdAddr)
	if err != nil {
		log.Fatalf("etcd Status(%s) перед Compact: %v", etcdAddr, err)
	}
	latestRev := statusResp.Header.Revision
	if _, err := cli.Compact(ctx, latestRev); err != nil {
		log.Fatalf("etcd Compact(%d): %v (Compact — обслуживающий вызов, обязан работать даже при поднятом alarm NOSPACE, это часть штатной процедуры восстановления)", latestRev, err)
	}
	fmt.Printf("etcd-misuse: Compact выполнен до ревизии %d\n", latestRev)
	dbSizeAfterCompact, dbSizeInUseAfterCompact := etcdStatusStable(ctx, cli, etcdAddr, "после Compact, до Defragment")
	fmt.Printf("etcd-misuse: после Compact, ДО Defragment — dbSize=%d dbSizeInUse=%d\n", dbSizeAfterCompact, dbSizeInUseAfterCompact)

	if _, err := cli.Defragment(ctx, etcdAddr); err != nil {
		log.Fatalf("etcd Defragment(%s): %v", etcdAddr, err)
	}
	fmt.Println("etcd-misuse: Defragment выполнен")
	dbSizeAfterDefrag, dbSizeInUseAfterDefrag := etcdStatusStable(ctx, cli, etcdAddr, "после Defragment")
	fmt.Printf("etcd-misuse: после Defragment — dbSize=%d dbSizeInUse=%d\n", dbSizeAfterDefrag, dbSizeInUseAfterDefrag)
	if dbSizeAfterDefrag < dbSizeAfter {
		fmt.Printf("etcd-misuse: физический размер базы сократился после Compact+Defragment: %d -> %d (-%d байт, -%.1f%%)\n",
			dbSizeAfter, dbSizeAfterDefrag, dbSizeAfter-dbSizeAfterDefrag, 100*float64(dbSizeAfter-dbSizeAfterDefrag)/float64(dbSizeAfter))
	} else {
		fmt.Printf("etcd-misuse: ПРЕДУПРЕЖДЕНИЕ — физический размер базы НЕ сократился после Compact+Defragment (%d -> %d)\n", dbSizeAfter, dbSizeAfterDefrag)
	}

	disarmed := misuseAlarmDisarmAll(ctx, cli)
	fmt.Printf("etcd-misuse: alarm disarm — снято: %v\n", disarmed)
	remaining, err := cli.AlarmList(ctx)
	if err != nil {
		log.Fatalf("etcd AlarmList после disarm: %v", err)
	}
	fmt.Printf("etcd-misuse: alarm list после disarm: %d активных алярмов\n", len(remaining.Alarms))

	// Канарейка: продолжение той же горячей записи ПОСЛЕ восстановления —
	// обязана пройти без ошибки.
	canaryOp := writeOps
	canaryKey := misuseEtcdKey(canaryOp % hotKeys)
	_, canaryErr := cli.Put(ctx, canaryKey, string(misuseValue(canaryOp, valueSize)))
	writeWorksAfterRecovery := canaryErr == nil
	if writeWorksAfterRecovery {
		fmt.Printf("etcd-misuse: канареечная запись %s ПОСЛЕ восстановления — успех\n", canaryKey)
	} else {
		fmt.Printf("etcd-misuse: канареечная запись %s ПОСЛЕ восстановления — ОШИБКА: %v\n", canaryKey, canaryErr)
	}

	// После процедуры восстановления запись обязана пойти. Это практическая
	// ценность сценария: не «etcd сломался», а «вот как чинить».
	if !writeWorksAfterRecovery {
		log.Fatalf("АССЕРТ: после compact+defrag+alarm disarm запись не восстановилась: %v", canaryErr)
	}

	fmt.Println()
	fmt.Printf("etcd-misuse: ГЛАВНОЕ ЧИСЛО — redis принял весь профиль (Δmem=%d байт, 0 ошибок из %d), etcd отказал на записи #%d из %d с alarm NOSPACE (dbSize=%d байт против квоты 64 МиБ), восстановление сработало (dbSize после Compact+Defragment=%d, запись снова идёт)\n",
		redisMemUsed, redisAttempted, wr.firstFailIdx, writeOps, dbSizeAfter, dbSizeAfterDefrag)
	fmt.Println("etcd-misuse: все ассерты пройдены (redis без ошибок, etcd показал разницу и упёрся в квоту, восстановление вернуло запись)")
}

func main() {
	var (
		scenario  = flag.String("scenario", "working-set", "working-set|compute-locality|etcd-misuse")
		originDSN = flag.String("origin-dsn", os.Getenv("ORIGIN_DSN"), "DSN источника истины PostgreSQL (со стороны хоста; нужен working-set/compute-locality, НЕ нужен etcd-misuse)")
		redisAddr = flag.String("redis-addr", envOr("REDIS_ADDR", "127.0.0.1:6381"), "адрес Redis")
		aeroAddr  = flag.String("aerospike-addr", envOr("AEROSPIKE_ADDR", "127.0.0.1:3010"), "адрес Aerospike (стенд working-set, НЕ Task 5)")
		chunkSize = flag.Int("chunk", envInt("WS_CHUNK", 10_000), "размер порции заливки")
		total     = flag.Int("total", envInt("WS_TOTAL", 200_000), "сколько записей залить всего (реальный датасет PostgreSQL, не больше 200000 — синтетическое наращивание убрано по ревью, см. task-8-report.md)")

		// --- флаги сценария compute-locality (Task 9) ---
		tarantoolAddr  = flag.String("tarantool-addr", envOr("TARANTOOL_ADDR", "127.0.0.1:3301"), "адрес Tarantool (compute-locality)")
		originDSNInNet = flag.String("origin-dsn-innet", envOr("ORIGIN_DSN_INNET", "postgres://inmemory:inmemory@inmemory-origin:5432/catalog?sslmode=disable"),
			"DSN PostgreSQL КАК ВИДНО ИЗ docker-сети inmemory-net (для подпроцесса ignite-stand.jar, compute-locality)")
		igniteDir           = flag.String("ignite-dir", envOr("IGNITE_DIR", "../ignite"), "путь к каталогу ignite/ (там лежит target/ignite-stand.jar, compute-locality)")
		igniteDiscoveryAddr = flag.String("ignite-discovery-addr", envOr("IGNITE_DISCOVERY_ADDR", "ignite-1:47500..47509,ignite-2:47500..47509"),
			"адрес discovery Ignite (передаётся подпроцессу ignite-stand.jar, compute-locality)")
		swapOrder = flag.Bool("swap-order", false, "поменять местами порядок исполнения двух проходов (защита от смещения порядка, Task 9 Step 2)")

		// --- флаги сценария etcd-misuse (Task 10) ---
		etcdMisuseAddr = flag.String("etcd-addr", envOr("ETCD_MISUSE_ADDR", "127.0.0.1:2382"), "адрес etcd с искусственно малой квотой (compose/etcd-misuse.yml, НЕ compose/etcd.yml Стенда 7)")
		misuseHotKeys  = flag.Int("misuse-hot-keys", envInt("MISUSE_HOT_KEYS", 5_000), "размер горячего набора ключей, перезаписываемого по кругу (etcd-misuse)")
		misuseWriteOps = flag.Int("misuse-write-ops", envInt("MISUSE_WRITE_OPS", 50_000), "сколько операций записи всего (бриф: 50k по 1 КБ; ~write-ops/hot-keys перезаписей на ключ)")
	)
	flag.Parse()

	ctx := context.Background()
	start := time.Now()

	switch *scenario {
	case "working-set":
		if *originDSN == "" {
			log.Fatal("нужен -origin-dsn или ORIGIN_DSN (working-set)")
		}
		origin := connectOrigin(ctx, *originDSN)
		defer origin.Close()

		rdb := redis.NewClient(&redis.Options{Addr: *redisAddr})
		defer rdb.Close()
		if err := rdb.Ping(ctx).Err(); err != nil {
			log.Fatalf("redis PING (%s): %v", *redisAddr, err)
		}

		aeroCl := connectAerospike(*aeroAddr)
		defer aeroCl.Close()
		if _, werr := aeroCl.WarmUp(64); werr != nil {
			fmt.Printf("aerospike WarmUp: %v (не критично, продолжаю)\n", werr)
		}

		scenarioWorkingSet(ctx, origin, rdb, aeroCl, *chunkSize, *total)

	case "compute-locality":
		if *originDSN == "" {
			log.Fatal("нужен -origin-dsn или ORIGIN_DSN (compute-locality)")
		}
		scenarioComputeLocality(ctx, *originDSN, *originDSNInNet, *tarantoolAddr, *igniteDir, *igniteDiscoveryAddr, *swapOrder)

	case "etcd-misuse":
		// Датасет/PostgreSQL этому сценарию не нужен (см. заголовок
		// scenarioEtcdMisuse) — профиль синтетический, 1 КБ на значение.
		const misuseValueSize = 1024
		scenarioEtcdMisuse(ctx, *redisAddr, *etcdMisuseAddr, *misuseHotKeys, *misuseWriteOps, misuseValueSize)

	default:
		log.Fatalf("неизвестный -scenario=%q (ожидается working-set|compute-locality|etcd-misuse)", *scenario)
	}

	fmt.Printf("OK (%s, %s)\n", *scenario, time.Since(start).Round(time.Second))
}
