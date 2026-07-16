// Стенд #7: эксплуатация — размер пула соединений под конкурентной
// нагрузкой и живые операционные сигналы (SLOWLOG/LATENCY/CLIENT LIST).
// Backup/restore — отдельный оркестрирующий скрипт ops/ops-demo.sh (redis-cli
// --rdb + docker volume), этот бинарник в цикле backup/restore не участвует.
//
// Два сценария (-scenario):
//   - pool-sizing: фиксированные -concurrency горутин (по заданию — 50)
//     одновременно пишут в Redis через клиент с PoolSize/MaxActiveConns
//     5/20/50 (-pool-sizes) — throughput, p50/p95/p99 латентности успешных
//     операций и число ошибок "connection pool timeout" на каждый размер.
//   - monitoring: живой снимок операционных сигналов — INFO clients,
//     CLIENT LIST, SLOWLOG GET, LATENCY HISTORY/LATEST — после гарантированно
//     медленной РЕАЛЬНОЙ команды (KEYS над 300000 засеянными ключами; DEBUG
//     SLEEP не подошёл — заблокирован на непривилегированных инстансах, см.
//     monitoring() ниже), чтобы SLOWLOG/LATENCY были не пустыми.
//
// Адрес Redis/Valkey — REDIS_ADDR, по умолчанию 127.0.0.1:6379 (тот же
// контракт, что и в остальных стендах, см. structures/main.go).
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

func addrFromEnv() string {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	return addr
}

func fatalf(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// must проверяет ошибку записи и завершает процесс, если она есть — тот же
// контракт fail-loud, что и в остальных стендах (см. structures/main.go):
// ни одна ошибка команды не должна быть молча проглочена.
func must(label string, cmd redis.Cmder) {
	if err := cmd.Err(); err != nil {
		fatalf("%s: ошибка записи: %v", label, err)
	}
}

// isPoolTimeout отличает ожидаемый эффект тесного пула (не дождались
// свободного соединения) от настоящего сбоя команды. go-redis v9 не
// экспортирует сентинел internal/pool.ErrPoolTimeout, поэтому единственный
// способ — сравнить текст ошибки: "redis: connection pool timeout"
// (проверено по исходникам v9.21.0, internal/pool/pool.go). Любая другая
// ошибка здесь — настоящий сбой, а не измеряемый эффект, и остаётся
// фатальной (см. main/runPoolSize).
func isPoolTimeout(err error) bool {
	return err != nil && strings.Contains(err.Error(), "connection pool timeout")
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// goroutineReport — то, что одна горутина накопила за свои -ops операций.
// elapsed — wall-clock ЭТОЙ горутины от старта до конца её собственного
// последовательного цикла; см. runPoolSize про то, почему это важно.
type goroutineReport struct {
	elapsed  time.Duration
	attempts int
	timeouts int
	succLat  []time.Duration
}

// runPoolSize гоняет -concurrency горутин одновременно (общий стартовый
// барьер start-канала — чтобы контеншн на пуле был максимальным с первой же
// операции, а не нарастал постепенно) против клиента с заданным poolSize.
//
// MaxActiveConns выставлен РАВНЫМ poolSize сознательно: в go-redis v9
// PoolSize — это "базовое" число соединений, и если соединений не хватает,
// пул РАСТЁТ сверх него (см. options.go, комментарий у поля PoolSize в
// v9.21.0: "If there is not enough connections in the pool, new connections
// will be allocated in excess of PoolSize, you can limit it through
// MaxActiveConns"). Без MaxActiveConns сценарий 5/20/50 сравнивал бы три
// одинаково безлимитных пула — ни один размер не дал бы ни одного таймаута,
// а весь смысл сравнения был бы потерян.
//
// PoolTimeout тоже выставлен явно и одинаково для всех размеров (см. флаг
// -pool-timeout в main): дефолт go-redis (ReadTimeout+1с, обычно ≈4с)
// слишком щедрый, чтобы истощение уместилось в разумное время теста —
// короткий и ОДИНАКОВЫЙ таймаут делает сравнение между размерами честным.
//
// Латентность попытки (успех ИЛИ таймаут) меряется вокруг каждого вызова
// целиком — то есть включает время ожидания свободного соединения. Для
// перцентилей используются ТОЛЬКО успешные попытки (таймауты считаются
// отдельно как число/доля, не подмешиваются в p50/p95/p99 — иначе они
// съезжали бы к строго одному значению ≈PoolTimeout и искажали хвост).
//
// Про несмещённое среднее и артефакт часов: elapsed/attempts УЖЕ применялся
// в streams-lua/main.go для ПОСЛЕДОВАТЕЛЬНОГО бенчмарка, где это работает,
// потому что elapsed и attempts относятся к одной и той же горутине. Здесь
// горутины работают ПАРАЛЛЕЛЬНО, поэтому глобальный overallElapsed/attempts
// не был бы оценкой латентности — это throughput (ops/сек), и именно так он
// и печатается. Несмещённая оценка ЛАТЕНТНОСТИ здесь — средневзвешенное по
// горутинам их СОБСТВЕННЫХ elapsed/attempts (внутри каждой горутины операции
// строго последовательны, поэтому там трюк elapsed/attempts работает так же,
// как в streams-lua): meanPerAttempt = Σ(elapsed_g) / Σ(attempts_g).
func runPoolSize(ctx context.Context, addr string, poolSize, concurrency, opsPerGoroutine int, poolTimeout time.Duration) {
	rdb := redis.NewClient(&redis.Options{
		Addr:           addr,
		PoolSize:       poolSize,
		MaxActiveConns: poolSize,
		PoolTimeout:    poolTimeout,
		// MaxRetries:-1 (в go-redis это буквальный код "0 повторов", см.
		// options.go: -1 → 0, 0(default) → 3) — сознательно выключает
		// автоповтор go-redis. По умолчанию
		// клиент САМ повторяет команду после pool timeout (обнаружено живьём:
		// на pool=1 внутренний PoolStats().Timeouts насчитал 1660 таймаутов
		// семафора за прогон, а до вызывающего кода в виде ошибки дошло только
		// 30 — остальные 1630 ретрай тихо поглотил, ценой добавленной
		// латентности, а не видимой ошибки). С ретраями "число pool timeout
		// ошибок" почти всегда занижено и не отражает реальную остроту
		// контеншна на пуле — поэтому здесь ретраи отключены, чтобы первая же
		// неудача была видна как есть.
		MaxRetries: -1,
	})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		fatalf("pool-sizing (size=%d): ping: %v", poolSize, err)
	}

	// FLUSHDB перед КАЖДЫМ размером пула — не гигиена, а условие
	// сравнимости арм. Все арма пишут одни и те же 10000 имён ключей
	// (ops:pool:g<g>:i<i>). Без очистки первая арма создаёт ключи с нуля
	// (и, при тесном пуле, успевает создать только часть — на size=5
	// доходит примерно половина попыток), а следующие армы уже
	// ПЕРЕЗАПИСЫВАЮТ существующие. Создание нового ключа и перезапись
	// существующего — разная работа для сервера (аллокация, рост словаря),
	// то есть армы отличались бы не только размером пула. С очисткой
	// каждая арма стартует с одинаково пустого keyspace.
	must(fmt.Sprintf("pool-sizing (size=%d) FLUSHDB", poolSize), rdb.FlushDB(ctx))

	reports := make([]goroutineReport, concurrency)
	var wg sync.WaitGroup
	start := make(chan struct{})
	overallStart := time.Now()
	for g := 0; g < concurrency; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			gStart := time.Now()
			rep := goroutineReport{succLat: make([]time.Duration, 0, opsPerGoroutine)}
			for i := 0; i < opsPerGoroutine; i++ {
				key := fmt.Sprintf("ops:pool:g%d:i%d", g, i)
				t0 := time.Now()
				err := rdb.Set(ctx, key, "v", 0).Err()
				d := time.Since(t0)
				rep.attempts++
				switch {
				case isPoolTimeout(err):
					rep.timeouts++
				case err != nil:
					fatalf("pool-sizing (size=%d) g=%d i=%d: ошибка записи: %v", poolSize, g, i, err)
				default:
					rep.succLat = append(rep.succLat, d)
				}
			}
			rep.elapsed = time.Since(gStart)
			reports[g] = rep
		}(g)
	}
	close(start)
	wg.Wait()
	overallElapsed := time.Since(overallStart)

	var totalAttempts, totalTimeouts int
	var sumGoroutineElapsed time.Duration
	var allSuccLat []time.Duration
	for _, r := range reports {
		totalAttempts += r.attempts
		totalTimeouts += r.timeouts
		sumGoroutineElapsed += r.elapsed
		allSuccLat = append(allSuccLat, r.succLat...)
	}
	succCount := totalAttempts - totalTimeouts
	meanPerAttempt := time.Duration(0)
	if totalAttempts > 0 {
		meanPerAttempt = sumGoroutineElapsed / time.Duration(totalAttempts)
	}
	throughput := float64(totalAttempts) / overallElapsed.Seconds()

	sort.Slice(allSuccLat, func(i, j int) bool { return allSuccLat[i] < allSuccLat[j] })
	nonZero := make([]time.Duration, 0, len(allSuccLat))
	zeros := 0
	for _, d := range allSuccLat {
		if d <= 0 {
			zeros++
			continue
		}
		nonZero = append(nonZero, d)
	}
	zeroRate := 0.0
	if len(allSuccLat) > 0 {
		zeroRate = float64(zeros) / float64(len(allSuccLat))
	}
	bias := math.Inf(1)
	if zeroRate < 1 {
		bias = 1 / (1 - zeroRate)
	}

	timeoutPct := 0.0
	if totalAttempts > 0 {
		timeoutPct = 100 * float64(totalTimeouts) / float64(totalAttempts)
	}
	fmt.Printf("pool-sizing size=%d: concurrency=%d ops/goroutine=%d pool-timeout-conf=%s attempts=%d succeeded=%d pool_timeouts=%d (%.1f%%)\n",
		poolSize, concurrency, opsPerGoroutine, poolTimeout, totalAttempts, succCount, totalTimeouts, timeoutPct)
	fmt.Printf("pool-sizing size=%d: overall_elapsed=%s throughput=%.1f ops/s | СРЕДНЯЯ латентность попытки (несмещённая, Σ(elapsed_горутины)/Σ(attempts))=%s\n",
		poolSize, overallElapsed.Round(time.Millisecond), throughput, meanPerAttempt)
	if len(nonZero) == 0 {
		fmt.Printf("pool-sizing size=%d: все %d успешных замеров нулевые (артефакт часов) — перцентили посчитать не из чего\n",
			poolSize, len(allSuccLat))
	} else {
		fmt.Printf("pool-sizing size=%d: p50=%s p95=%s p99=%s — по %d/%d ненулевым замерам УСПЕШНЫХ операций (нулевых=%.1f%%, смещение вверх ×%.2f — см. streams-lua/main.go про механизм); таймауты пула сюда НЕ подмешаны, считаются отдельно строкой выше\n",
			poolSize, percentile(nonZero, 0.50), percentile(nonZero, 0.95), percentile(nonZero, 0.99),
			len(nonZero), len(allSuccLat), 100*zeroRate, bias)
	}

	// Живой снимок пула сразу после нагрузки — реальные числа с сервера, а не
	// то, что "должно быть" по -pool-sizes.
	poolStats := rdb.PoolStats()
	fmt.Printf("pool-sizing size=%d: клиентская статистика пула (go-redis PoolStats) — Hits=%d Misses=%d Timeouts=%d TotalConns=%d IdleConns=%d StaleConns=%d\n",
		poolSize, poolStats.Hits, poolStats.Misses, poolStats.Timeouts, poolStats.TotalConns, poolStats.IdleConns, poolStats.StaleConns)
}

// poolSizing прогоняет каждый размер пула по очереди. Порядок арм —
// потенциальный конфаундер: армы идут последовательно в одном процессе, и
// медленный дрейф среды (или разогрев сервера/клиента) достаётся им
// неравномерно. Поэтому -swap-order разворачивает список размеров: тот же
// набор арм в обратном порядке. Две ориентации, прогнанные обе, отделяют
// эффект РАЗМЕРА ПУЛА от эффекта ПОРЯДКА — если направление сохраняется в
// обеих, это свойство размера, а не позиции в очереди. Тот же приём и тот
// же флаг уже применяется в streams-lua/main.go (eval-vs-function).
func poolSizing(ctx context.Context, addr string, concurrency, opsPerGoroutine int, poolTimeout time.Duration, sizesRaw string, swapOrder bool) {
	var sizes []int
	for _, s := range strings.Split(sizesRaw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			fatalf("bad -pool-sizes value %q: %v", s, err)
		}
		sizes = append(sizes, n)
	}
	if len(sizes) == 0 {
		fatalf("-pool-sizes не задал ни одного размера")
	}
	order := "прямой (по возрастанию размера)"
	if swapOrder {
		for i, j := 0, len(sizes)-1; i < j; i, j = i+1, j-1 {
			sizes[i], sizes[j] = sizes[j], sizes[i]
		}
		order = "обратный (-swap-order)"
	}
	fmt.Printf("=== pool-sizing === concurrency=%d ops/goroutine=%d pool-timeout=%s sizes=%v порядок=%s\n",
		concurrency, opsPerGoroutine, poolTimeout, sizes, order)
	for _, size := range sizes {
		runPoolSize(ctx, addr, size, concurrency, opsPerGoroutine, poolTimeout)
	}
}

// monitoring печатает живой снимок операционных сигналов, которые
// оператор смотрит в первую очередь при разборе инцидента: INFO clients,
// CLIENT LIST, SLOWLOG, LATENCY HISTORY/LATEST.
//
// Чтобы SLOWLOG/LATENCY не остались пустыми "в зависимости от того, повезло
// ли", сценарий гарантированно производит одно НАСТОЯЩЕЕ медленное
// событие — не через DEBUG SLEEP (заблокирован на непривилегированных
// инстансах: `enable-debug-command` — protected config, неизменяем через
// CONFIG SET, проверено живьём: "ERR ... can't set immutable config"), а
// через `KEYS <паттерн>` над специально засеянными monitoringSeedKeys
// одноразовыми ключами. `KEYS` — однопоточный O(N) обход всего keyspace;
// над достаточным числом ключей он реально занимает миллисекунды и попадает
// и в SLOWLOG, и в LATENCY HISTORY command (проверено живьём: 300000 ключей
// → SLOWLOG зафиксировал ~39.9мс). Это не синтетика "притворимся медленными"
// — это тот самый механизм, из-за которого `KEYS *` в проде считается
// операционной ошибкой (см. README, «Типовые инциденты»).
const monitoringSeedKeys = 300000

func monitoring(ctx context.Context, rdb *redis.Client) {
	fmt.Println("=== monitoring ===")
	must("CONFIG SET latency-monitor-threshold", rdb.ConfigSet(ctx, "latency-monitor-threshold", "1"))
	must("CONFIG SET slowlog-log-slower-than", rdb.ConfigSet(ctx, "slowlog-log-slower-than", "1000")) // 1мс — явно занижено против дефолтных 10мс, чтобы гарантированно поймать наше собственное медленное KEYS
	must("SLOWLOG RESET", rdb.Do(ctx, "SLOWLOG", "RESET"))
	must("LATENCY RESET", rdb.Do(ctx, "LATENCY", "RESET"))

	fmt.Printf("--- сею %d одноразовых ключей (ops:mon:scan:*) одним EVAL, чтобы KEYS ниже было над чем реально работать ---\n", monitoringSeedKeys)
	seedScript := `for i=1,tonumber(ARGV[1]) do redis.call('SET', 'ops:mon:scan:'..i, 'v') end return redis.call('dbsize')`
	dbsizeAfterSeed, err := rdb.Eval(ctx, seedScript, nil, monitoringSeedKeys).Result()
	if err != nil {
		fatalf("seed EVAL: %v", err)
	}
	fmt.Printf("--- DBSIZE после сева: %v ---\n", dbsizeAfterSeed)

	t0 := time.Now()
	matched, err := rdb.Keys(ctx, "ops:mon:scan:*").Result()
	if err != nil {
		fatalf("KEYS ops:mon:scan:*: %v", err)
	}
	fmt.Printf("--- KEYS ops:mon:scan:*: %d совпадений, клиентское время=%s (это НЕ то же самое, что время исполнения в SLOWLOG ниже — там серверный таймер, здесь round-trip целиком) ---\n",
		len(matched), time.Since(t0))

	cleanupScript := `for i=1,tonumber(ARGV[1]) do redis.call('DEL', 'ops:mon:scan:'..i) end return redis.call('dbsize')`
	dbsizeAfterCleanup, err := rdb.Eval(ctx, cleanupScript, nil, monitoringSeedKeys).Result()
	if err != nil {
		fatalf("cleanup EVAL: %v", err)
	}
	fmt.Printf("--- DBSIZE после очистки: %v ---\n", dbsizeAfterCleanup)

	info, err := rdb.Info(ctx, "clients").Result()
	if err != nil {
		fatalf("INFO clients: %v", err)
	}
	fmt.Println("--- INFO clients ---")
	fmt.Println(strings.TrimSpace(info))

	list, err := rdb.Do(ctx, "CLIENT", "LIST").Text()
	if err != nil {
		fatalf("CLIENT LIST: %v", err)
	}
	list = strings.TrimSpace(list)
	var lines []string
	if list != "" {
		lines = strings.Split(list, "\n")
	}
	fmt.Printf("--- CLIENT LIST: %d подключений ---\n", len(lines))
	for _, l := range lines {
		fmt.Println(l)
	}

	slow, err := rdb.Do(ctx, "SLOWLOG", "GET", "10").Result()
	if err != nil {
		fatalf("SLOWLOG GET: %v", err)
	}
	fmt.Println("--- SLOWLOG GET 10 ---")
	fmt.Printf("%v\n", slow)

	hist, err := rdb.Do(ctx, "LATENCY", "HISTORY", "command").Result()
	if err != nil {
		fatalf("LATENCY HISTORY command: %v", err)
	}
	fmt.Println("--- LATENCY HISTORY command ---")
	fmt.Printf("%v\n", hist)

	latest, err := rdb.Do(ctx, "LATENCY", "LATEST").Result()
	if err != nil {
		fatalf("LATENCY LATEST: %v", err)
	}
	fmt.Println("--- LATENCY LATEST ---")
	fmt.Printf("%v\n", latest)
}

func main() {
	scenario := flag.String("scenario", "", "pool-sizing | monitoring")
	concurrency := flag.Int("concurrency", 50, "число горутин (по заданию Стенда #7 — 50)")
	ops := flag.Int("ops", 200, "операций на горутину (pool-sizing); 200 — значение, на котором сняты все опубликованные числа в FIXTURES.md/README.md")
	poolSizes := flag.String("pool-sizes", "5,20,50", "список размеров пула через запятую (pool-sizing)")
	swapOrder := flag.Bool("swap-order", false,
		"прогнать размеры пула в ОБРАТНОМ порядке — отделяет эффект размера пула от эффекта порядка арм (та же схема, что -swap-order в streams-lua)")
	poolTimeout := flag.Duration("pool-timeout", 2*time.Millisecond,
		"PoolTimeout клиента — явно короткий и одинаковый для всех размеров, чтобы истощение пула укладывалось в разумное время теста; 2мс — значение, зафиксированное в FIXTURES.md/README.md (см. там же таблицу чувствительности к этому параметру на 1/2/5мс)")
	flag.Parse()

	addr := addrFromEnv()
	ctx := context.Background()

	switch *scenario {
	case "pool-sizing":
		poolSizing(ctx, addr, *concurrency, *ops, *poolTimeout, *poolSizes, *swapOrder)
	case "monitoring":
		rdb := redis.NewClient(&redis.Options{Addr: addr})
		defer rdb.Close()
		if err := rdb.Ping(ctx).Err(); err != nil {
			fatalf("ping: %v", err)
		}
		monitoring(ctx, rdb)
	default:
		fatalf("unknown -scenario, expected: pool-sizing | monitoring")
	}
}
