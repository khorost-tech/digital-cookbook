// Стенд Redis как кэша перед PostgreSQL. Пять сценариев, каждый — отдельное
// утверждение статьи, проверяемое живым прогоном, а не измерением abs-latency
// (см. README, раздел «Границы метода»). Внутренности Redis (персистентность,
// eviction, event loop, Lua, streams, кластер) здесь не изучаются — они разобраны
// в соседнем стенде databases/redis/deep-dive.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const ttl = 5 * time.Minute

// unlockScript — снятие блокировки cache stampede через compare-and-delete:
// удаляет ключ, только если его текущее значение совпадает с токеном ИМЕННО
// этого владельца. Обязательно, а не безусловный DEL: если загрузка данных
// из источника переживёт TTL блокировки (10с), TTL истечёт сам, лок возьмёт
// другая горутина — и безусловный DEL от первой горутины (уже не владеющей
// локом по факту, но всё ещё выполняющей свой defer) удалил бы ЧУЖУЮ, новую
// блокировку победителя. Ровно эта ошибка прямо описана в документации
// Redis по распределённым блокировкам, раздел про безопасное освобождение:
// https://redis.io/docs/latest/develop/clients/patterns/distributed-locks/
// Без уникального per-acquisition токена сравнивать нечего — все владельцы
// писали бы одно и то же значение, и compare-and-delete выродился бы
// обратно в тот же небезопасный безусловный DEL.
var unlockScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("del", KEYS[1])
else
	return 0
end
`)

// randomLockToken — уникальный токен для одного взятия блокировки (см.
// unlockScript выше). 16 случайных байт достаточно для отличимости в
// пределах одного прогона стенда (до нескольких сотен горутин) — токен
// нужен для compare-and-delete, а не для криптостойкости самого лока.
func randomLockToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("crypto/rand: %v", err)
	}
	return hex.EncodeToString(b)
}

type Product struct {
	ID         int64             `json:"id"`
	SKU        string            `json:"sku"`
	Title      string            `json:"title"`
	PriceCents int64             `json:"price_cents"`
	Attrs      map[string]string `json:"attrs"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

func cacheKey(id int64) string { return "product:" + strconv.FormatInt(id, 10) }

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// queryOriginProduct — единственная точка чтения товара из PostgreSQL.
// counter инкрементируется атомарно: и cache-aside, и stampede считают через
// него реальное число запросов, дошедших до origin.
func queryOriginProduct(ctx context.Context, pool *pgxpool.Pool, id int64, counter *int64) (Product, error) {
	if counter != nil {
		atomic.AddInt64(counter, 1)
	}
	var p Product
	var attrsJSON []byte
	err := pool.QueryRow(ctx,
		`SELECT id, sku, title, price_cents, attrs, updated_at FROM products WHERE id=$1`, id,
	).Scan(&p.ID, &p.SKU, &p.Title, &p.PriceCents, &attrsJSON, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(attrsJSON, &p.Attrs); err != nil {
		return p, err
	}
	return p, nil
}

func main() {
	var (
		scenario  = flag.String("scenario", "", "cache-aside|write-through|stampede|leaderboard|hll")
		dsn       = flag.String("dsn", os.Getenv("ORIGIN_DSN"), "DSN PostgreSQL")
		redisAddr = flag.String("redis-addr", envOr("REDIS_ADDR", "127.0.0.1:6381"), "адрес Redis")
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

	rdb := redis.NewClient(&redis.Options{Addr: *redisAddr})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping: %v", err)
	}

	switch *scenario {
	case "cache-aside":
		scenarioCacheAside(ctx, pool, rdb)
	case "write-through":
		scenarioWriteThrough(ctx, pool, rdb)
	case "stampede":
		scenarioStampede(ctx, pool, rdb)
	case "leaderboard":
		scenarioLeaderboard(ctx, pool, rdb)
	case "hll":
		scenarioHLL(ctx, pool, rdb)
	default:
		log.Fatalf("неизвестный -scenario=%q (ожидается cache-aside|write-through|stampede|leaderboard|hll)", *scenario)
	}
	fmt.Println("OK")
}

// scenarioCacheAside — прогоняет реальную последовательность просмотров
// (зипфово распределённую, из views) через классический cache-aside:
// GET, на промахе — SELECT в PG + SET с TTL. Считает hits/misses/dbsize.
func scenarioCacheAside(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client) {
	must(rdb.FlushDB(ctx).Err())

	rows, err := pool.Query(ctx, `SELECT product_id FROM views ORDER BY ts, user_id, product_id`)
	if err != nil {
		log.Fatalf("SELECT views: %v", err)
	}
	var sequence []int64
	for rows.Next() {
		var pid int64
		if err := rows.Scan(&pid); err != nil {
			log.Fatalf("scan: %v", err)
		}
		sequence = append(sequence, pid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Fatalf("rows: %v", err)
	}

	var hits, misses, originQueries int64
	unique := make(map[int64]struct{})
	for _, pid := range sequence {
		unique[pid] = struct{}{}
		key := cacheKey(pid)
		val, err := rdb.Get(ctx, key).Result()
		switch {
		case err == redis.Nil:
			misses++
			p, qerr := queryOriginProduct(ctx, pool, pid, &originQueries)
			if qerr != nil {
				log.Fatalf("origin query product_id=%d: %v", pid, qerr)
			}
			data, _ := json.Marshal(p)
			if err := rdb.Set(ctx, key, data, ttl).Err(); err != nil {
				log.Fatalf("SET %s: %v", key, err)
			}
		case err != nil:
			log.Fatalf("GET %s: %v", key, err)
		default:
			hits++
			_ = val
		}
	}

	total := hits + misses
	hitRate := float64(hits) / float64(total)
	dbsize, err := rdb.DBSize(ctx).Result()
	if err != nil {
		log.Fatalf("DBSIZE: %v", err)
	}
	uniqueRequested := len(unique)

	fmt.Printf("cache-aside: total_requests=%d hits=%d misses=%d hit_rate=%.4f dbsize=%d unique_requested=%d origin_queries=%d\n",
		total, hits, misses, hitRate, dbsize, uniqueRequested, originQueries)
	fmt.Printf("cache-aside: ratio_pg_queries_no_cache_over_with_cache = %d/%d = %.3f\n",
		total, originQueries, float64(total)/float64(originQueries))

	// Зипфово распределение → hit rate должен быть высоким: горячие товары
	// перечитываются. Порог 0.80 — не подгонка под результат, а граница
	// осмысленности кэша: ниже неё cache-aside не окупает лишний round-trip.
	if hitRate < 0.80 {
		log.Fatalf("АССЕРТ: hit rate %.3f < 0.80 — кэш не работает как ожидается", hitRate)
	}
	// Число уникальных ключей в Redis не может превышать число уникальных
	// запрошенных товаров.
	if dbsize > int64(uniqueRequested) {
		log.Fatalf("АССЕРТ: dbsize=%d > уникальных товаров=%d", dbsize, uniqueRequested)
	}
}

// scenarioWriteThrough — три части:
//
//	A. write-through: цена пишется в PG и в кэш одной операцией, кэш обязан
//	   немедленно отдавать новое значение.
//	B. обратный порядок (инвалидация ДО записи в PG) — форсированная явной
//	   синхронизацией демонстрация окна рассогласования: конкурентный
//	   читатель успевает заполнить кэш старым значением, и оно там и
//	   остаётся даже после того, как в PG уже новая цена.
//	C. правильный порядок (запись в PG, ЗАТЕМ инвалидация/DEL) — кэш после
//	   операции пуст, следующее чтение гарантированно промахнётся и возьмёт
//	   свежее значение: рассогласование не переживает операцию.
func scenarioWriteThrough(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client) {
	must(rdb.FlushDB(ctx).Err())
	var dummy int64

	// --- A: write-through ---
	const idA = int64(1)
	var oldPrice int64
	if err := pool.QueryRow(ctx, `SELECT price_cents FROM products WHERE id=$1`, idA).Scan(&oldPrice); err != nil {
		log.Fatalf("select price idA: %v", err)
	}
	newPrice := oldPrice + 100
	if _, err := pool.Exec(ctx, `UPDATE products SET price_cents=$1, updated_at=now() WHERE id=$2`, newPrice, idA); err != nil {
		log.Fatalf("update price idA: %v", err)
	}
	full, err := queryOriginProduct(ctx, pool, idA, &dummy)
	if err != nil {
		log.Fatalf("reload idA: %v", err)
	}
	data, _ := json.Marshal(full)
	if err := rdb.Set(ctx, cacheKey(idA), data, ttl).Err(); err != nil {
		log.Fatalf("SET idA: %v", err)
	}
	val, err := rdb.Get(ctx, cacheKey(idA)).Result()
	if err != nil {
		log.Fatalf("GET idA: %v", err)
	}
	var cached Product
	must(json.Unmarshal([]byte(val), &cached))
	fmt.Printf("write-through A: product_id=%d old_price=%d new_price=%d cached_price=%d\n",
		idA, oldPrice, newPrice, cached.PriceCents)
	// После write-through чтение из кэша обязано вернуть новую цену.
	if cached.PriceCents != newPrice {
		log.Fatalf("АССЕРТ: кэш отдал %d, ожидалась %d", cached.PriceCents, newPrice)
	}

	// --- B: обратный порядок (invalidate-before-write) — окно рассогласования ---
	const idB = int64(2)
	var priceBefore int64
	if err := pool.QueryRow(ctx, `SELECT price_cents FROM products WHERE id=$1`, idB).Scan(&priceBefore); err != nil {
		log.Fatalf("select price idB: %v", err)
	}
	primeB, err := queryOriginProduct(ctx, pool, idB, &dummy)
	if err != nil {
		log.Fatalf("prime idB: %v", err)
	}
	primeData, _ := json.Marshal(primeB)
	must(rdb.Set(ctx, cacheKey(idB), primeData, ttl).Err())

	priceAfter := priceBefore + 500
	writerInvalidated := make(chan struct{})
	readerDone := make(chan struct{})
	// Читатель стартует ровно в момент, когда писатель уже удалил ключ, но
	// ЕЩЁ НЕ записал новую цену в PG — это и есть окно неправильного порядка.
	go func() {
		defer close(readerDone)
		<-writerInvalidated
		p, err := queryOriginProduct(ctx, pool, idB, &dummy)
		if err != nil {
			log.Fatalf("reader reload idB: %v", err)
		}
		d, _ := json.Marshal(p)
		must(rdb.Set(ctx, cacheKey(idB), d, ttl).Err())
	}()

	// Писатель, ОШИБОЧНЫЙ порядок: сначала инвалидация, потом запись в PG.
	must(rdb.Del(ctx, cacheKey(idB)).Err())
	close(writerInvalidated)
	// Даём читателю гарантированно завершить re-populate до того, как мы
	// зафиксируем новую цену в PG — форсированный, а не случайный интервал,
	// потому что демонстрируем факт рассогласования, а не время окна.
	time.Sleep(50 * time.Millisecond)
	if _, err := pool.Exec(ctx, `UPDATE products SET price_cents=$1, updated_at=now() WHERE id=$2`, priceAfter, idB); err != nil {
		log.Fatalf("update price idB: %v", err)
	}
	<-readerDone

	finalVal, err := rdb.Get(ctx, cacheKey(idB)).Result()
	if err != nil {
		log.Fatalf("GET idB финал: %v", err)
	}
	var finalCached Product
	must(json.Unmarshal([]byte(finalVal), &finalCached))
	staleObserved := 0
	if finalCached.PriceCents != priceAfter {
		staleObserved = 1
	}
	fmt.Printf("write-through B (обратный порядок): product_id=%d price_before=%d price_after_pg=%d cache_final=%d stale_observed=%d\n",
		idB, priceBefore, priceAfter, finalCached.PriceCents, staleObserved)
	if staleObserved == 0 {
		fmt.Println("ВНИМАНИЕ: рассогласование не наблюдалось в этом прогоне — записать честно")
	} else {
		fmt.Println("write-through B: рассогласование подтверждено — кэш держит устаревшую цену, PG уже с новой")
	}

	// --- C: правильный порядок (write PG, затем DEL) — без остаточной лжи ---
	const idC = int64(3)
	var priceC1 int64
	if err := pool.QueryRow(ctx, `SELECT price_cents FROM products WHERE id=$1`, idC).Scan(&priceC1); err != nil {
		log.Fatalf("select price idC: %v", err)
	}
	primeC, err := queryOriginProduct(ctx, pool, idC, &dummy)
	if err != nil {
		log.Fatalf("prime idC: %v", err)
	}
	primeCData, _ := json.Marshal(primeC)
	must(rdb.Set(ctx, cacheKey(idC), primeCData, ttl).Err())

	priceC2 := priceC1 + 500
	if _, err := pool.Exec(ctx, `UPDATE products SET price_cents=$1, updated_at=now() WHERE id=$2`, priceC2, idC); err != nil {
		log.Fatalf("update price idC: %v", err)
	}
	must(rdb.Del(ctx, cacheKey(idC)).Err())

	exists, err := rdb.Exists(ctx, cacheKey(idC)).Result()
	if err != nil {
		log.Fatalf("EXISTS idC: %v", err)
	}
	fmt.Printf("write-through C (правильный порядок, cache-aside + инвалидация): product_id=%d price_before=%d price_after_pg=%d cache_exists_after=%d\n",
		idC, priceC1, priceC2, exists)
	if exists != 0 {
		log.Fatalf("АССЕРТ: правильный порядок должен оставлять кэш пустым (принудительный промах на следующем чтении), а ключ всё ещё существует")
	}
	// Следующее чтение — честный cache-aside промах, обязан вернуть свежую цену.
	refetched, err := queryOriginProduct(ctx, pool, idC, &dummy)
	if err != nil {
		log.Fatalf("refetch idC: %v", err)
	}
	fmt.Printf("write-through C: повторное чтение после промаха вернуло price=%d (ожидалось %d)\n", refetched.PriceCents, priceC2)
	if refetched.PriceCents != priceC2 {
		log.Fatalf("АССЕРТ: повторное чтение вернуло %d, ожидалась %d", refetched.PriceCents, priceC2)
	}
}

// scenarioStampede — N=200 горутин одновременно читают один и тот же холодный
// ключ. Без защиты каждая горутина, увидев промах, сама идёт в PG. С защитой
// (SET NX как распределённая блокировка на промахе) в PG должен уйти ровно
// один запрос — остальные ждут, пока победитель заполнит кэш.
func scenarioStampede(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client) {
	const n = 200

	// --- без защиты ---
	must(rdb.FlushDB(ctx).Err())
	const coldIDUnprotected = int64(50_001)
	var unprotectedQueries int64
	{
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(n)
		for i := 0; i < n; i++ {
			go func() {
				defer wg.Done()
				<-start
				key := cacheKey(coldIDUnprotected)
				_, err := rdb.Get(ctx, key).Result()
				if err == redis.Nil {
					p, qerr := queryOriginProduct(ctx, pool, coldIDUnprotected, &unprotectedQueries)
					if qerr != nil {
						log.Printf("unprotected origin query error: %v", qerr)
						return
					}
					data, _ := json.Marshal(p)
					_ = rdb.Set(ctx, key, data, ttl).Err()
				} else if err != nil {
					log.Printf("unprotected GET error: %v", err)
				}
			}()
		}
		close(start)
		wg.Wait()
	}
	fmt.Printf("stampede unprotected: N=%d origin_queries=%d\n", n, unprotectedQueries)
	// Без защиты одновременные читатели ломятся в PG все разом.
	if unprotectedQueries < 2 {
		log.Fatalf("АССЕРТ: stampede не воспроизвёлся (запросов в PG: %d) — сценарий недоказателен", unprotectedQueries)
	}

	// --- с защитой (SET NX лок на промахе) ---
	must(rdb.FlushDB(ctx).Err())
	const coldIDProtected = int64(50_002)
	var protectedQueries int64
	{
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(n)
		for i := 0; i < n; i++ {
			go func() {
				defer wg.Done()
				<-start
				getOrLoadProtected(ctx, pool, rdb, coldIDProtected, &protectedQueries)
			}()
		}
		close(start)
		wg.Wait()
	}
	fmt.Printf("stampede protected: N=%d origin_queries=%d\n", n, protectedQueries)
	// С защитой в PG должен уйти ровно один запрос.
	if protectedQueries != 1 {
		log.Fatalf("АССЕРТ: с защитой запросов в PG %d, ожидался 1", protectedQueries)
	}

	fmt.Printf("stampede: ratio_unprotected_over_protected = %d/%d = %.1f\n",
		unprotectedQueries, protectedQueries, float64(unprotectedQueries)/float64(protectedQueries))
}

// getOrLoadProtected — cache-aside с блокировкой на промахе: победитель гонки
// за SET NX идёт в PG и заполняет кэш, проигравшие ждут появления значения.
func getOrLoadProtected(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, id int64, counter *int64) {
	key := cacheKey(id)
	if _, err := rdb.Get(ctx, key).Result(); err == nil {
		return
	} else if err != redis.Nil {
		log.Printf("protected GET error: %v", err)
		return
	}

	lockKey := "lock:" + key
	token := randomLockToken()
	ok, err := rdb.SetNX(ctx, lockKey, token, 10*time.Second).Result()
	if err != nil {
		log.Printf("protected SETNX error: %v", err)
		return
	}
	if ok {
		// Освобождение — ТОЛЬКО через compare-and-delete со своим токеном
		// (см. unlockScript выше), не безусловный DEL: иначе эта горутина
		// рискует удалить чужую блокировку, взятую после истечения нашего TTL.
		defer func() {
			if _, err := unlockScript.Run(ctx, rdb, []string{lockKey}, token).Result(); err != nil {
				log.Printf("protected unlock error: %v", err)
			}
		}()
		// Двойная проверка: между начальным Get этой горутины (miss) и
		// получением лока настоящий победитель мог успеть отработать целиком
		// (запрос в PG → SET → return → defer Del лока) и уже снять лок —
		// тогда следующая горутина в очереди на SetNX тоже получает ok=true
		// на уже свободный лок и без этой проверки сходила бы в PG повторно.
		// Живым прогоном (12 повторов stampede) это воспроизводилось в ~50%
		// прогонов, иногда с 2-3 «победителями» — не гипотетический случай.
		if _, err := rdb.Get(ctx, key).Result(); err == nil {
			return
		}
		p, qerr := queryOriginProduct(ctx, pool, id, counter)
		if qerr != nil {
			log.Printf("protected origin query error: %v", qerr)
			return
		}
		data, _ := json.Marshal(p)
		_ = rdb.Set(ctx, key, data, ttl).Err()
		return
	}

	// Проиграли гонку за лок — ждём, пока победитель заполнит кэш.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := rdb.Get(ctx, key).Result(); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Не должно случаться при нормальной задержке PG; fallback — не оставлять
	// горутину висеть без ответа.
	log.Printf("protected: таймаут ожидания ключа %s, fallback на прямой запрос", key)
	p, qerr := queryOriginProduct(ctx, pool, id, counter)
	if qerr != nil {
		log.Printf("protected fallback origin query error: %v", qerr)
		return
	}
	data, _ := json.Marshal(p)
	_ = rdb.Set(ctx, key, data, ttl).Err()
}

// scenarioLeaderboard — ZINCRBY по каждому просмотру, ZREVRANGE топ-10.
// Проверка корректности: топ из Redis обязан совпадать с топом,
// посчитанным SQL-агрегатом в PG.
func scenarioLeaderboard(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client) {
	must(rdb.FlushDB(ctx).Err())
	const key = "leaderboard"

	rows, err := pool.Query(ctx, `SELECT product_id FROM views`)
	if err != nil {
		log.Fatalf("SELECT views: %v", err)
	}
	pipe := rdb.Pipeline()
	const batchSize = 1000
	buffered := 0
	for rows.Next() {
		var pid int64
		if err := rows.Scan(&pid); err != nil {
			log.Fatalf("scan: %v", err)
		}
		pipe.ZIncrBy(ctx, key, 1, strconv.FormatInt(pid, 10))
		buffered++
		if buffered >= batchSize {
			if _, err := pipe.Exec(ctx); err != nil {
				log.Fatalf("pipeline exec: %v", err)
			}
			buffered = 0
		}
	}
	if buffered > 0 {
		if _, err := pipe.Exec(ctx); err != nil {
			log.Fatalf("pipeline exec (tail): %v", err)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Fatalf("rows: %v", err)
	}

	redisTop, err := rdb.ZRevRangeWithScores(ctx, key, 0, 9).Result()
	if err != nil {
		log.Fatalf("ZREVRANGE: %v", err)
	}
	fmt.Println("leaderboard: топ-10 из Redis (ZREVRANGE)")
	redisScore := make(map[string]int64, len(redisTop))
	for i, z := range redisTop {
		member := fmt.Sprint(z.Member)
		redisScore[member] = int64(z.Score)
		fmt.Printf("  #%d product_id=%s views=%.0f\n", i+1, member, z.Score)
	}

	sqlRows, err := pool.Query(ctx, `SELECT product_id, count(*) c FROM views GROUP BY product_id ORDER BY c DESC LIMIT 10`)
	if err != nil {
		log.Fatalf("SELECT SQL top10: %v", err)
	}
	type pc struct {
		ID    int64
		Count int64
	}
	var sqlTop []pc
	for sqlRows.Next() {
		var r pc
		if err := sqlRows.Scan(&r.ID, &r.Count); err != nil {
			log.Fatalf("scan sql top10: %v", err)
		}
		sqlTop = append(sqlTop, r)
	}
	sqlRows.Close()
	if err := sqlRows.Err(); err != nil {
		log.Fatalf("rows sql: %v", err)
	}
	fmt.Println("leaderboard: топ-10 из SQL (GROUP BY ... ORDER BY count DESC)")
	for i, r := range sqlTop {
		fmt.Printf("  #%d product_id=%d views=%d\n", i+1, r.ID, r.Count)
	}

	match := true
	for _, r := range sqlTop {
		rv, ok := redisScore[strconv.FormatInt(r.ID, 10)]
		if !ok || rv != r.Count {
			match = false
			fmt.Printf("  РАСХОЖДЕНИЕ: product_id=%d sql_count=%d redis_score=%v(ok=%v)\n", r.ID, r.Count, rv, ok)
		}
	}
	fmt.Printf("leaderboard: match_redis_vs_sql=%v\n", match)
	if !match {
		log.Fatalf("АССЕРТ: топ Redis не совпадает с топом, посчитанным SQL в PG")
	}
}

// scenarioHLL — для топ-10 товаров (по числу просмотров) считает точное
// множество уникальных пользователей (SADD/SCARD) и приближённое через
// HyperLogLog (PFADD/PFCOUNT), сравнивает относительную погрешность и
// занимаемую память (MEMORY USAGE) обеих структур.
func scenarioHLL(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client) {
	must(rdb.FlushDB(ctx).Err())

	rows, err := pool.Query(ctx, `SELECT product_id, count(*) c FROM views GROUP BY product_id ORDER BY c DESC LIMIT 10`)
	if err != nil {
		log.Fatalf("SELECT top10: %v", err)
	}
	type pc struct {
		ID    int64
		Views int64
	}
	var top []pc
	for rows.Next() {
		var r pc
		if err := rows.Scan(&r.ID, &r.Views); err != nil {
			log.Fatalf("scan top10: %v", err)
		}
		top = append(top, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Fatalf("rows: %v", err)
	}

	var maxRelErr float64
	for _, t := range top {
		hllKey := fmt.Sprintf("hll:product:%d", t.ID)
		exactKey := fmt.Sprintf("exact:product:%d", t.ID)

		urows, err := pool.Query(ctx, `SELECT DISTINCT user_id FROM views WHERE product_id=$1`, t.ID)
		if err != nil {
			log.Fatalf("SELECT DISTINCT user_id: %v", err)
		}
		pipe := rdb.Pipeline()
		buffered := 0
		const batchSize = 500
		for urows.Next() {
			var uid int64
			if err := urows.Scan(&uid); err != nil {
				log.Fatalf("scan uid: %v", err)
			}
			us := strconv.FormatInt(uid, 10)
			pipe.PFAdd(ctx, hllKey, us)
			pipe.SAdd(ctx, exactKey, us)
			buffered++
			if buffered >= batchSize {
				if _, err := pipe.Exec(ctx); err != nil {
					log.Fatalf("pipeline exec: %v", err)
				}
				buffered = 0
			}
		}
		if buffered > 0 {
			if _, err := pipe.Exec(ctx); err != nil {
				log.Fatalf("pipeline exec (tail): %v", err)
			}
		}
		urows.Close()
		if err := urows.Err(); err != nil {
			log.Fatalf("urows: %v", err)
		}

		exact, err := rdb.SCard(ctx, exactKey).Result()
		if err != nil {
			log.Fatalf("SCARD: %v", err)
		}
		approx, err := rdb.PFCount(ctx, hllKey).Result()
		if err != nil {
			log.Fatalf("PFCOUNT: %v", err)
		}
		hllMem, err := rdb.MemoryUsage(ctx, hllKey).Result()
		if err != nil {
			log.Fatalf("MEMORY USAGE hll: %v", err)
		}
		exactMem, err := rdb.MemoryUsage(ctx, exactKey).Result()
		if err != nil {
			log.Fatalf("MEMORY USAGE exact: %v", err)
		}

		relErr := math.Abs(float64(approx)-float64(exact)) / float64(exact)
		if relErr > maxRelErr {
			maxRelErr = relErr
		}
		fmt.Printf("hll: product_id=%d views=%d exact_unique_users=%d hll_approx=%d rel_err=%.4f hll_mem_bytes=%d exact_mem_bytes=%d ratio_hll_over_exact_mem=%.4f\n",
			t.ID, t.Views, exact, approx, relErr, hllMem, exactMem, float64(hllMem)/float64(exactMem))

		// Заявленная погрешность HLL — 0.81%. Проверяем на своих данных с запасом:
		// на малых мощностях HLL точен, на больших держит границу.
		if relErr > 0.02 {
			log.Fatalf("АССЕРТ: погрешность HLL %.4f > 0.02 (approx=%d exact=%d, product_id=%d)", relErr, approx, exact, t.ID)
		}
	}
	fmt.Printf("hll: max_rel_err_across_top10=%.4f\n", maxRelErr)
}

func must(err error) {
	if err != nil {
		log.Fatalf("must: %v", err)
	}
}
