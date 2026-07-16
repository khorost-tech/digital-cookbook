// Стенд #6: streams (consumer groups, at-least-once), Lua-атомарность против
// реальной гонки, EVAL против FUNCTION.
//
// Четыре сценария (-scenario):
//
//   - consumer-groups: XADD -n сообщений (по умолчанию 1000) в stream, группа
//     из 3 консьюмеров читает через XREADGROUP батчами по -batch. Один
//     консьюмер (consumer-2) на середине «падает»: обрабатывает очередной
//     батч (реальный побочный эффект — HINCRBY счётчика по ID сообщения), но
//     НЕ делает XACK и больше не читает. Показывает XPENDING (кто и сколько
//     держит неподтверждённым), затем другой консьюмер забирает зависшие
//     сообщения через XCLAIM и обрабатывает их — те же ID обрабатываются
//     ВТОРОЙ раз, что и даёт настоящие дубликаты at-least-once (не
//     теоретические, а измеренные по счётчику HINCRBY).
//
//   - race-without-lua / atomic-with-lua: -workers горутин делают -iterations
//     read-modify-write операций над общим счётчиком: без Lua — отдельные
//     GET и SET (гонка возможна между ними), с Lua — один атомарный EVAL
//     (GET+условие+SET внутри одного скрипта, выполняется на сервере не
//     прерываясь). Обе версии дополнительно доказывают, что горутины
//     РЕАЛЬНО пересекались во времени (см. detectCrossWorkerOverlaps) — без
//     этого «0 потерь» ничего не доказывает.
//
//   - eval-vs-function: latency одного и того же Lua-скрипта через EVAL
//     (компилируется и кешируется по SHA сервером при первом вызове) и через
//     FUNCTION LOAD + FCALL (функция загружена заранее, как в проде при
//     деплое). Меряется отдельно холодный первый вызов и установившееся
//     состояние (после прогрева) — методология в выводе программы.
//     ВАЖНО про метрику: главное число здесь — elapsed/attempts, а НЕ
//     перцентили. Часть замеров на этой машине возвращает ровно 0 при
//     честно выполненной команде; нули вносят в сумму 0, поэтому среднее по
//     ненулевым завышено ровно в 1/(1-доля_нулей) раз (тождество), а доля
//     нулей у разных серий разная — сравнивать перцентили между сериями
//     нельзя. Подробно — в комментарии к benchmarkCalls; это не косметика, а
//     разница между честным и нечестным сравнением.
//
// Адрес Redis/Valkey читается из REDIS_ADDR, по умолчанию 127.0.0.1:6379.
//
// Честность измерений (см. также README/FIXTURES, раздел «Стенд #6»):
//   - Гонка в race-without-lua недетерминирована: число потерянных
//     инкрементов меняется от прогона к прогону. Публикуется разброс по
//     нескольким прогонам, а не одно удобное число.
//   - «0 потерь» без доказанного пересечения окон GET/SET разных горутин —
//     не результат, а сломанный прогон (os.Exit(1), порог — менее 50%
//     пересёкшихся попыток): конкурентность обязана быть измерена, а не
//     предположена.
//   - Любая настоящая ошибка команды (оборванное соединение, таймаут) —
//     фатальна в обоих сценариях гонки. Потерянный инкремент — это
//     измеряемое ЯВЛЕНИЕ (корректно выполненные GET/SET, которые всё равно
//     потеряли данные из-за гонки), а не ошибка команды — это разные вещи, и
//     код их не путает.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
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

// must проверяет ошибку записи и завершает процесс, если она есть.
// Нюанс для сценариев гонки (race-without-lua/atomic-with-lua): потерянный
// инкремент — это НЕ ошибка команды, GET и SET там оба отрабатывают
// успешно, просто из-за гонки эффект одного из них теряется. must() ловит
// именно сбой самой команды (оборванное соединение, таймаут и т.п.) — это
// всегда фатально, run считается сломанным. Явление гонки считается отдельно
// и печатается как результат, а не как ошибка.
func must(label string, cmd redis.Cmder) {
	if err := cmd.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: ошибка записи: %v\n", label, err)
		os.Exit(1)
	}
}

func fatalf(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// Общие Lua-артефакты для race/atomic и eval-vs-function.
// ---------------------------------------------------------------------------

// conditionalIncrScript — тот же скрипт используется и в atomic-with-lua
// (атомарность против гонки), и в eval-vs-function (методология «один и тот
// же скрипт через два способа вызова»).
const conditionalIncrScript = `
local v = tonumber(redis.call('GET', KEYS[1]))
if v < tonumber(ARGV[1]) then
  redis.call('SET', KEYS[1], v + 1)
  return 1
end
return 0
`

const functionLibraryName = "cookbookstreamslua"
const functionName = "incr_if_lt"

// functionLibraryCode — та же логика, что и conditionalIncrScript, обёрнутая
// в библиотеку FUNCTION (Redis требует register_function, а не голый скрипт).
var functionLibraryCode = fmt.Sprintf(`#!lua name=%s
redis.register_function('%s', function(keys, args)
  local v = tonumber(redis.call('GET', keys[1]))
  if v < tonumber(args[1]) then
    redis.call('SET', keys[1], v + 1)
    return 1
  end
  return 0
end)
`, functionLibraryName, functionName)

// ---------------------------------------------------------------------------
// consumer-groups
// ---------------------------------------------------------------------------

const (
	cgStream        = "cg:orders"
	cgGroup         = "cg:workers"
	cgProcessedKey  = "cg:processed"
	cgCrashConsumer = "consumer-2"
)

func idsOf(msgs []redis.XMessage) []string {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}

func consumerGroups(ctx context.Context, rdb *redis.Client, n, batch int) {
	fmt.Println("=== consumer-groups ===")
	fmt.Printf("n=%d batch=%d consumers=[consumer-1 consumer-2 consumer-3] crash=%s\n", n, batch, cgCrashConsumer)

	must("DEL stream(before)", rdb.Del(ctx, cgStream))
	must("DEL processed(before)", rdb.Del(ctx, cgProcessedKey))

	for i := 1; i <= n; i++ {
		must(fmt.Sprintf("XADD seq=%d", i), rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: cgStream,
			Values: map[string]interface{}{"seq": i, "payload": fmt.Sprintf("payload-%d", i)},
		}))
	}
	xlen, err := rdb.XLen(ctx, cgStream).Result()
	if err != nil {
		fatalf("XLEN после заливки: ошибка записи: %v", err)
	}
	if xlen != int64(n) {
		fatalf("XADD залил %d сообщений вместо %d — прогон сломан", xlen, n)
	}
	fmt.Printf("XADD: залито %d сообщений в %s\n", xlen, cgStream)

	// "0" — группа видит все уже существующие сообщения как новые (доставка
	// с начала стрима), а не только будущие.
	must("XGROUP CREATE", rdb.XGroupCreate(ctx, cgStream, cgGroup, "0"))

	order := []string{"consumer-1", "consumer-2", "consumer-3"}
	crashed := map[string]bool{}
	totalRead := 0
	consumer2AckedCount := 0
	// падаем не раньше, чем consumer-2 сам нормально обработал и подтвердил
	// хотя бы 2 полных батча — это и есть «на середине», а не на первом же
	// чтении.
	crashThreshold := 2 * batch
	var stuckIDs []string

	for {
		progressed := false
		for _, name := range order {
			if crashed[name] {
				continue
			}
			res, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    cgGroup,
				Consumer: name,
				Streams:  []string{cgStream, ">"},
				Count:    int64(batch),
				Block:    -1, // без BLOCK: сразу вернуть то, что есть, включая "ничего"
			}).Result()
			if err == redis.Nil {
				continue // для этого консьюмера прямо сейчас новых сообщений нет
			}
			if err != nil {
				fatalf("XREADGROUP consumer=%s: ошибка записи: %v", name, err)
			}
			if len(res) == 0 || len(res[0].Messages) == 0 {
				continue
			}
			msgs := res[0].Messages
			totalRead += len(msgs)
			progressed = true

			if name == cgCrashConsumer && consumer2AckedCount >= crashThreshold {
				// "падаем": реальный побочный эффект уже произошёл
				// (HINCRBY), но XACK не делаем и больше не читаем.
				for _, m := range msgs {
					must(fmt.Sprintf("HINCRBY processed id=%s (pre-crash)", m.ID),
						rdb.HIncrBy(ctx, cgProcessedKey, m.ID, 1))
				}
				stuckIDs = idsOf(msgs)
				crashed[name] = true
				fmt.Printf("%s: обработал %d сообщений (%s..%s) и «упал» БЕЗ XACK\n",
					name, len(msgs), msgs[0].ID, msgs[len(msgs)-1].ID)
				continue
			}

			for _, m := range msgs {
				must(fmt.Sprintf("HINCRBY processed id=%s", m.ID),
					rdb.HIncrBy(ctx, cgProcessedKey, m.ID, 1))
			}
			must(fmt.Sprintf("XACK consumer=%s", name), rdb.XAck(ctx, cgStream, cgGroup, idsOf(msgs)...))
			if name == cgCrashConsumer {
				consumer2AckedCount += len(msgs)
			}
		}
		if !progressed {
			break
		}
	}

	if totalRead != n {
		fatalf("доставлено %d сообщений вместо %d — часть стрима не была прочитана ни одним консьюмером, прогон сломан", totalRead, n)
	}
	if len(stuckIDs) == 0 {
		fatalf("consumer-2 ни разу не «упал» с необработанным батчем (crashThreshold=%d не был достигнут при totalRead=%d) — сценарий не индуцировал зависшие сообщения, прогон нельзя публиковать как «PEL непуст»", crashThreshold, totalRead)
	}
	fmt.Printf("доставлено всего: %d/%d, зависло у %s: %d сообщений (%s..%s)\n",
		totalRead, n, cgCrashConsumer, len(stuckIDs), stuckIDs[0], stuckIDs[len(stuckIDs)-1])

	// --- XPENDING: сводка + полный список ---
	pending, err := rdb.XPending(ctx, cgStream, cgGroup).Result()
	if err != nil {
		fatalf("XPENDING (summary): ошибка записи: %v", err)
	}
	fmt.Printf("XPENDING summary: count=%d lower=%s higher=%s consumers=%v\n",
		pending.Count, pending.Lower, pending.Higher, pending.Consumers)
	if pending.Count != int64(len(stuckIDs)) {
		fatalf("XPENDING count=%d не совпадает с числом зависших при «падении» (%d) — прогон сломан", pending.Count, len(stuckIDs))
	}

	ext, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: cgStream, Group: cgGroup, Start: "-", End: "+", Count: int64(n),
	}).Result()
	if err != nil {
		fatalf("XPENDING (ext): ошибка записи: %v", err)
	}
	fmt.Printf("XPENDING ext: %d записей, владелец всех — %s (idle разброс %s..%s)\n",
		len(ext), ext[0].Consumer, ext[0].Idle, ext[len(ext)-1].Idle)

	// --- XCLAIM: другой (живой) консьюмер забирает зависшее ---
	claimant := "consumer-1"
	claimed, err := rdb.XClaim(ctx, &redis.XClaimArgs{
		Stream: cgStream, Group: cgGroup, Consumer: claimant,
		MinIdle: 0, // форс-клейм: оператор уже объявил consumer-2 мёртвым, ждать
		// стандартный idle-порог незачем — именно так это и делается на
		// практике после подтверждённого падения процесса.
		Messages: stuckIDs,
	}).Result()
	if err != nil {
		fatalf("XCLAIM: ошибка записи: %v", err)
	}
	if len(claimed) != len(stuckIDs) {
		fatalf("XCLAIM забрал %d сообщений вместо %d — прогон сломан", len(claimed), len(stuckIDs))
	}
	fmt.Printf("XCLAIM: %s забрал %d сообщений у %s\n", claimant, len(claimed), cgCrashConsumer)

	// claimant реально переобрабатывает — вот тут и рождается настоящий
	// дубликат: тот же ID уже был обработан один раз до падения.
	for _, m := range claimed {
		must(fmt.Sprintf("HINCRBY processed id=%s (post-claim)", m.ID),
			rdb.HIncrBy(ctx, cgProcessedKey, m.ID, 1))
	}
	must("XACK post-claim", rdb.XAck(ctx, cgStream, cgGroup, idsOf(claimed)...))

	pendingAfter, err := rdb.XPending(ctx, cgStream, cgGroup).Result()
	if err != nil {
		fatalf("XPENDING (после ack): ошибка записи: %v", err)
	}
	if pendingAfter.Count != 0 {
		fatalf("XPENDING после финального XACK всё ещё показывает count=%d — PEL не опустел, прогон сломан", pendingAfter.Count)
	}
	fmt.Printf("XPENDING после XACK: count=%d (PEL пуст)\n", pendingAfter.Count)

	// --- честный подсчёт дубликатов по реальному счётчику обработки ---
	processed, err := rdb.HGetAll(ctx, cgProcessedKey).Result()
	if err != nil {
		fatalf("HGETALL processed: ошибка записи: %v", err)
	}
	if len(processed) != n {
		fatalf("processed содержит %d уникальных ID вместо %d — часть сообщений вообще не обработана, прогон сломан", len(processed), n)
	}
	var duplicates, singles []string
	for id, v := range processed {
		switch v {
		case "1":
			singles = append(singles, id)
		case "2":
			duplicates = append(duplicates, id)
		default:
			fatalf("id=%s обработан %s раз(а) — ожидалось 1 или 2, прогон сломан", id, v)
		}
	}
	if len(duplicates) != len(stuckIDs) {
		fatalf("дубликатов после XCLAIM: %d, ожидалось ровно %d (=размер зависшего батча) — прогон сломан", len(duplicates), len(stuckIDs))
	}
	if len(duplicates) == 0 {
		fatalf("дубликатов после XCLAIM: 0 — переобработка не индуцирована, «чистый» результат публиковать нельзя")
	}
	sort.Strings(duplicates)
	fmt.Printf("итог: обработано ровно 1 раз=%d, обработано ДВАЖДЫ (реальные at-least-once дубликаты после XCLAIM)=%d\n",
		len(singles), len(duplicates))
	fmt.Printf("ID дубликатов: %s..%s\n", duplicates[0], duplicates[len(duplicates)-1])
}

// ---------------------------------------------------------------------------
// race-without-lua / atomic-with-lua
// ---------------------------------------------------------------------------

// window — интервал одной попытки [t0 начала GET (или EVAL), t1 конца
// последней команды попытки]. Нужен, чтобы доказать реальное пересечение
// времени между РАЗНЫМИ горутинами — без этого «0 потерь» ничего не значит
// (могло просто не быть конкурентности).
type window struct {
	worker     int
	start, end time.Time
}

// detectCrossWorkerOverlaps — сколько попыток имели хотя бы одно активное
// (не завершившееся) окно ДРУГОЙ горутины в момент своего начала. O(n log n)
// сортировка + линейная прополка отработавших окон (типичный размер active —
// порядка -workers, не всего n).
func detectCrossWorkerOverlaps(windows []window) int {
	sorted := make([]window, len(windows))
	copy(sorted, windows)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start.Before(sorted[j].start) })

	var active []window
	overlapping := 0
	for _, w := range sorted {
		next := active[:0]
		for _, a := range active {
			if a.end.After(w.start) {
				next = append(next, a)
			}
		}
		active = next
		for _, a := range active {
			if a.worker != w.worker {
				overlapping++
				break
			}
		}
		active = append(active, w)
	}
	return overlapping
}

// runRace — общее тело для race-without-lua (useLua=false: раздельные
// GET+SET, гонка возможна) и atomic-with-lua (useLua=true: один EVAL,
// атомарно на сервере). limit намеренно равен total (число попыток) — при
// такой границе условие v<limit истинно на КАЖДОЙ попытке (реальное значение
// не может обогнать число уже свершившихся записей), поэтому все total
// попыток реально пытаются писать, и потерянные инкременты считаются просто
// как total - итоговое_значение.
func runRace(ctx context.Context, rdb *redis.Client, label, key string, workers, iterations int, useLua bool) {
	fmt.Printf("=== %s ===\n", label)
	must(label+" DEL", rdb.Del(ctx, key))
	must(label+" SET init=0", rdb.Set(ctx, key, 0, 0))

	total := workers * iterations
	limit := int64(total)

	var mu sync.Mutex
	windows := make([]window, 0, total)
	record := func(id int, t0, t1 time.Time) {
		mu.Lock()
		windows = append(windows, window{worker: id, start: t0, end: t1})
		mu.Unlock()
	}

	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				t0 := time.Now()
				if useLua {
					if _, err := rdb.Eval(ctx, conditionalIncrScript, []string{key}, limit).Result(); err != nil {
						fatalf("%s EVAL worker=%d iter=%d: ошибка записи: %v", label, id, i, err)
					}
				} else {
					v, err := rdb.Get(ctx, key).Int()
					if err != nil {
						fatalf("%s GET worker=%d iter=%d: ошибка записи: %v", label, id, i, err)
					}
					if int64(v) < limit {
						must(fmt.Sprintf("%s SET worker=%d iter=%d", label, id, i), rdb.Set(ctx, key, v+1, 0))
					}
				}
				t1 := time.Now()
				record(id, t0, t1)
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)

	finalVal, err := rdb.Get(ctx, key).Int64()
	if err != nil {
		fatalf("%s: финальный GET: ошибка записи: %v", label, err)
	}
	if finalVal > limit {
		fatalf("%s: итоговое значение %d превысило теоретический максимум %d — прогон сломан (условие v<limit нарушено сервером?)", label, finalVal, limit)
	}
	lost := limit - finalVal
	overlaps := detectCrossWorkerOverlaps(windows)

	fmt.Printf("%s: workers=%d iterations=%d attempts=%d final=%d lost=%d elapsed=%s cross-worker-overlaps=%d/%d попыток\n",
		label, workers, iterations, total, finalVal, lost, elapsed.Round(time.Millisecond), overlaps, total)

	// Порог — доля, а не «хоть одно пересечение». При `> 0` тест проходил бы
	// в буквальном граничном случае (одно пересечение из 10000), который
	// доказывает конкурентность разве что формально. Наблюдаемые значения —
	// 99.4–99.9%, так что 50% оставляет большой запас и при этом требует,
	// чтобы конкурентной была БОЛЬШИНСТВО попыток, а не единицы.
	const minOverlapFraction = 0.5
	overlapFraction := float64(overlaps) / float64(total)
	if overlapFraction < minOverlapFraction {
		fatalf("%s: пересеклись во времени лишь %d из %d попыток (%.1f%%, порог %.0f%%) — конкурентность не доказана, значит lost=%d недостоверен (мог быть получен и без гонки). Прогон сломан: увеличьте -workers/-iterations или проверьте, что клиент реально параллелен.",
			label, overlaps, total, 100*overlapFraction, 100*minOverlapFraction, lost)
	}

	if useLua {
		if lost != 0 {
			fmt.Printf("%s: ВНИМАНИЕ — атомарный Lua-сценарий потерял %d инкрементов при доказанной конкурентности (overlaps=%d/%d). Это противоречит гарантии атомарности EVAL и является находкой, а не браком измерения — печатается как есть.\n",
				label, lost, overlaps, total)
		} else {
			fmt.Printf("%s: 0 потерь при доказанной конкурентности (overlaps=%d/%d попыток пересеклись во времени с другой горутиной) — атомарность EVAL подтверждена измерением, не только документацией.\n",
				label, overlaps, total)
		}
	} else {
		if lost == 0 {
			fmt.Printf("%s: lost=0 в ЭТОМ конкретном прогоне при доказанной конкурентности (overlaps=%d/%d) — гонка возможна, но в этот раз ни одна пара не столкнулась на одном и том же значении. Это реальный, а не подогнанный результат; см. README/FIXTURES для разброса по нескольким прогонам.\n",
				label, overlaps, total)
		}
	}
}

// ---------------------------------------------------------------------------
// eval-vs-function
// ---------------------------------------------------------------------------

// Stats — результат замера серии вызовов.
//
// ГЛАВНОЕ число здесь — MeanPerAttempt (= Elapsed/Attempts), а НЕ перцентили.
// Причина (см. benchmarkCalls): часть замеров возвращает ровно 0. Нули вносят
// в сумму 0, поэтому среднее по одним лишь ненулевым замерам =
// Elapsed/NonZero = MeanPerAttempt/(1-ZeroRate) — это ТОЖДЕСТВО, а не модель:
// отбрасывание нулей завышает результат ровно во столько раз. Доля нулей у
// разных серий разная, поэтому сравнивать перцентили между сериями нельзя.
// MeanPerAttempt же берёт только две отметки времени (начало и конец серии) и
// от распределения нулей внутри серии не зависит вовсе.
type Stats struct {
	Attempts     int           // сколько вызовов реально сделано
	NonZero      int           // сколько из них дали ненулевую длительность
	ZeroReadings int           // сколько вернули ровно 0 (артефакт часов)
	Elapsed      time.Duration // wall-clock на все Attempts
	// SumMeasured — сумма всех замеров (включая нули). Служит проверкой того,
	// что межвызовные накладные расходы пренебрежимы (см. print): сумма
	// телескопируется в Elapsed минус промежутки между вызовами ПРИ ЛЮБЫХ
	// часах, поэтому отношение ~1.000 говорит именно про оверхед цикла и
	// НИЧЕГО не говорит про природу нулей.
	SumMeasured      time.Duration
	MeanPerAttempt   time.Duration // Elapsed/Attempts — НЕСМЕЩЁННАЯ оценка
	P50, P95, P99    time.Duration // по ненулевым; СМЕЩЕНЫ вверх, см. выше
	ThroughputOpsSec float64       // Attempts/Elapsed — считаем по попыткам, а не по выжившим замерам
}

func (s Stats) zeroRate() float64 {
	if s.Attempts == 0 {
		return 0
	}
	return float64(s.ZeroReadings) / float64(s.Attempts)
}

// biasFactor — во сколько раз перцентили по ненулевым завышены относительно
// истинной латентности, если артефакт сохраняет время: выжившие замеры несут
// всё время серии, но их всего (1-ZeroRate) доля.
func (s Stats) biasFactor() float64 {
	z := s.zeroRate()
	if z >= 1 {
		return math.Inf(1)
	}
	return 1 / (1 - z)
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

// computeStats принимает ВСЕ замеры серии (включая нулевые) и elapsed на всю
// серию. Перцентили считаются по ненулевым, среднее — по elapsed/attempts.
func computeStats(all []time.Duration, elapsed time.Duration) Stats {
	nonZero := make([]time.Duration, 0, len(all))
	var sum time.Duration
	zeros := 0
	for _, d := range all {
		sum += d
		if d <= 0 {
			zeros++
			continue
		}
		nonZero = append(nonZero, d)
	}
	sort.Slice(nonZero, func(i, j int) bool { return nonZero[i] < nonZero[j] })

	s := Stats{
		Attempts:     len(all),
		NonZero:      len(nonZero),
		ZeroReadings: zeros,
		Elapsed:      elapsed,
		SumMeasured:  sum,
		P50:          percentile(nonZero, 0.50),
		P95:          percentile(nonZero, 0.95),
		P99:          percentile(nonZero, 0.99),
	}
	if len(all) > 0 {
		s.MeanPerAttempt = elapsed / time.Duration(len(all))
	}
	if elapsed > 0 {
		s.ThroughputOpsSec = float64(len(all)) / elapsed.Seconds()
	}
	return s
}

func (s Stats) print(label string) {
	// Порядок печати намеренный: сначала несмещённое среднее (главное число),
	// потом доля нулей, и только потом — перцентили с явной пометкой о
	// смещении. Иначе перцентили читаются как «латентность», хотя это
	// латентность выжившей части замеров.
	fmt.Printf("%s: attempts=%d elapsed=%s | СРЕДНЕЕ (elapsed/attempts, несмещённое)=%s | throughput=%.1f ops/s\n",
		label, s.Attempts, s.Elapsed.Round(time.Microsecond), s.MeanPerAttempt, s.ThroughputOpsSec)

	consRatio := 0.0
	if s.Elapsed > 0 {
		consRatio = float64(s.SumMeasured) / float64(s.Elapsed)
	}
	// ВНИМАНИЕ на формулировку: отношение ~1.000 НЕ является свидетельством
	// аляйзинга часов — сумма телескопируется в elapsed минус межвызовные
	// промежутки при любых часах, и идеальные часы без единого нуля дали бы
	// те же 1.000. Проверка говорит ровно одно: оверхед цикла пренебрежим,
	// значит elapsed/attempts — это среднее время именно ВЫЗОВОВ.
	fmt.Printf("%s: нулевых замеров=%d/%d (%.1f%%); сумма замеров=%s против elapsed=%s (отношение %.3f — проверка того, что накладные расходы цикла пренебрежимы; про природу нулей это отношение не говорит ничего)\n",
		label, s.ZeroReadings, s.Attempts, 100*s.zeroRate(),
		s.SumMeasured.Round(time.Millisecond), s.Elapsed.Round(time.Millisecond), consRatio)

	fmt.Printf("%s: p50=%s p95=%s p99=%s — ТОЛЬКО по %d ненулевым замерам, СМЕЩЕНЫ ВВЕРХ примерно в %.2f× (=1/(1-%.3f)); сравнивать перцентили между сериями с РАЗНОЙ долей нулей нельзя\n",
		label, s.P50, s.P95, s.P99, s.NonZero, s.biasFactor(), s.zeroRate())
}

// benchmarkCalls — warmup вызовов отбрасывается (даём серверу закешировать
// скрипт/выйти на установившееся состояние), затем РОВНО n вызовов
// измеряются по отдельности. Любая ошибка вызова — фатальна: латентность
// вокруг команды, которая не выполнилась, ничего не измеряет.
//
// # Артефакт часов и почему здесь НЕЛЬЗЯ «просто отбросить нули»
//
// Наблюдение (живьём, эта машина, Docker Desktop/Windows): time.Since()
// вокруг реального сетевого round-trip к Redis регулярно возвращает РОВНО 0,
// при этом команда честно выполняется (значение меняется, ошибок нет). Доля
// таких замеров — 34.9–46.4% по протоколированным сериям (см. FIXTURES,
// раздел «Стенд #6»).
//
// МЕХАНИЗМ НЕ УСТАНОВЛЕН, и стенд его не устанавливает. Известно только
// встречное свидетельство против наивного «низкое разрешение часов»: все
// ненулевые значения кратны 100нс, а часы с шагом 100нс физически не могут
// округлить ~390мкс до нуля. Что именно происходит (Hyper-V/QPC/планировщик)
// — не диагностировано и не утверждается.
//
// Но для корректного замера механизм и НЕ НУЖЕН — достаточно арифметики:
// нулевые замеры вносят в сумму ровно 0, поэтому среднее по ненулевым =
// сумма/nonZero = elapsed/nonZero = (elapsed/attempts)/(1-доля_нулей). Это
// ТОЖДЕСТВО: отбрасывание нулей завышает результат ровно в 1/(1-доля_нулей)
// раз. Доля нулей у разных серий РАЗНАЯ, поэтому разница перцентилей между
// сериями отражает в первую очередь разницу долей нулей, а не разницу
// латентности вызовов. Симметричный код при асимметричной доле артефакта —
// это НЕ несмещённое сравнение.
//
// Поэтому стенд:
//   - НЕ добирает попытки взамен нулевых (это раздувало бы elapsed на серию,
//     не увеличивая число полезных данных, и ломало бы elapsed/attempts);
//   - главным числом печатает elapsed/attempts — оценку, которая берёт лишь
//     две отметки времени (начало и конец серии) и потому не зависит от того,
//     как часы распределили время между отдельными замерами;
//   - перцентили печатает, но с явной долей нулей и множителем смещения рядом.
func benchmarkCalls(label string, warmup, n int, call func() error) Stats {
	for i := 0; i < warmup; i++ {
		if err := call(); err != nil {
			fatalf("%s warmup#%d: ошибка записи: %v", label, i, err)
		}
	}
	lat := make([]time.Duration, 0, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		t0 := time.Now()
		if err := call(); err != nil {
			fatalf("%s call#%d: ошибка записи: %v", label, i, err)
		}
		lat = append(lat, time.Since(t0))
	}
	elapsed := time.Since(start)
	stats := computeStats(lat, elapsed)
	if stats.NonZero == 0 {
		fatalf("%s: ВСЕ %d замеров нулевые — перцентили посчитать не из чего, замер недостоверен", label, n)
	}
	return stats
}

func evalVsFunction(ctx context.Context, rdb *redis.Client, swapOrder bool) {
	fmt.Println("=== eval-vs-function ===")
	const opCount = 2000
	const warmup = 200
	limit := int64(10_000_000) // v<limit всегда true — здесь не гонка, а чистая латентность вызова на одном клиенте без конкуренции

	fmt.Printf("методика: один и тот же Lua-скрипт (%d байт), один клиент без конкуренции, op count=%d, warmup=%d отброшено перед каждым измерением\n",
		len(conditionalIncrScript), opCount, warmup)

	// FUNCTION LOAD делается ЗАРАНЕЕ (как в проде, при деплое) — поэтому у
	// FCALL нет отдельного «холодного» шага загрузки библиотеки в момент
	// первого вызова. SCRIPT FLUSH — чтобы холодный первый EVAL не
	// унаследовал кеш от предыдущего запуска этого же скрипта в этом же
	// процессе/сервере.
	if _, err := rdb.FunctionLoadReplace(ctx, functionLibraryCode).Result(); err != nil {
		fatalf("FUNCTION LOAD REPLACE: ошибка записи: %v", err)
	}
	must("SCRIPT FLUSH", rdb.ScriptFlush(ctx))

	evalKey, fcallKey := "evalfn:eval", "evalfn:fcall"
	must("DEL eval key", rdb.Del(ctx, evalKey))
	must("DEL fcall key", rdb.Del(ctx, fcallKey))
	must("SET eval key", rdb.Set(ctx, evalKey, 0, 0))
	must("SET fcall key", rdb.Set(ctx, fcallKey, 0, 0))

	// холодный первый вызов каждого способа
	t0 := time.Now()
	if _, err := rdb.Eval(ctx, conditionalIncrScript, []string{evalKey}, limit).Result(); err != nil {
		fatalf("EVAL холодный: ошибка записи: %v", err)
	}
	coldEval := time.Since(t0)

	t0 = time.Now()
	if _, err := rdb.FCall(ctx, functionName, []string{fcallKey}, limit).Result(); err != nil {
		fatalf("FCALL холодный: ошибка записи: %v", err)
	}
	coldFcall := time.Since(t0)

	formatCold := func(d time.Duration) string {
		if d <= 0 {
			// см. benchmarkCalls: единичный замер (не серия), поймавший
			// артефакт часов. Переизмерить «начисто» нельзя — EVAL после
			// первого выполнения уже не холодный (закеширован по SHA).
			// Честно помечаем как недостоверный, а не печатаем "0s".
			return "<замер поймал артефакт часов (вернул 0); недостоверен>"
		}
		return d.String()
	}
	fmt.Printf("холодный первый вызов: EVAL=%s FCALL=%s (после этого вызова EVAL уже закеширован сервером по SHA — дальше он не «холоднее» FCALL)\n",
		formatCold(coldEval), formatCold(coldFcall))
	// Серии идут последовательно, не вперемешку. Это оставляет конфаунд:
	// «медленнее» может означать не «эта команда дороже», а «эта серия шла
	// первой/второй» (дрейф среды). -swap-order меняет порядок серий местами;
	// прогнав обе ориентации, можно разделить эффект РУКИ и эффект ПОРЯДКА:
	// если одна и та же команда медленнее в обеих ориентациях — это рука;
	// если медленнее всегда первая/вторая серия — это порядок.
	runEval := func() Stats {
		return benchmarkCalls("EVAL(warm)", warmup, opCount, func() error {
			return rdb.Eval(ctx, conditionalIncrScript, []string{evalKey}, limit).Err()
		})
	}
	runFcall := func() Stats {
		return benchmarkCalls("FCALL(warm)", warmup, opCount, func() error {
			return rdb.FCall(ctx, functionName, []string{fcallKey}, limit).Err()
		})
	}

	var evalStats, fcallStats Stats
	if swapOrder {
		fmt.Println("порядок серий: FCALL, затем EVAL (-swap-order=true) — обратная ориентация для отделения эффекта руки от эффекта порядка")
		fcallStats = runFcall()
		evalStats = runEval()
	} else {
		fmt.Println("порядок серий: EVAL, затем FCALL (-swap-order=false) — прямая ориентация; серии идут ПОСЛЕДОВАТЕЛЬНО, не вперемешку, поэтому эффект руки и эффект порядка в ОДНОМ прогоне не разделены (см. -swap-order)")
		evalStats = runEval()
		fcallStats = runFcall()
	}

	evalStats.print("EVAL(warm)")
	fcallStats.print("FCALL(warm)")

	// ГЛАВНОЕ сравнение — по несмещённому среднему. Перцентили для сравнения
	// между сериями непригодны: у серий разная доля нулевых замеров, и разница
	// перцентилей отражает в первую очередь её, а не латентность вызова.
	deltaMean := evalStats.MeanPerAttempt - fcallStats.MeanPerAttempt
	order := "EVAL-first"
	if swapOrder {
		order = "FCALL-first"
	}
	fmt.Printf("СРАВНЕНИЕ (несмещённое, по elapsed/attempts, порядок=%s): EVAL=%s FCALL=%s дельта(EVAL-FCALL)=%s\n",
		order, evalStats.MeanPerAttempt, fcallStats.MeanPerAttempt, deltaMean)

	fmt.Printf("СПРАВОЧНО (НЕ сравнивать): дельта p50=%s при долях нулей EVAL=%.1f%% / FCALL=%.1f%% — перцентили смещены вверх в %.2f× и %.2f× соответственно, поэтому их разница отражает прежде всего разницу долей нулей, а не разницу EVAL и FCALL\n",
		evalStats.P50-fcallStats.P50,
		100*evalStats.zeroRate(), 100*fcallStats.zeroRate(),
		evalStats.biasFactor(), fcallStats.biasFactor())
}

// ---------------------------------------------------------------------------

func main() {
	scenario := flag.String("scenario", "", "consumer-groups | race-without-lua | atomic-with-lua | eval-vs-function")
	n := flag.Int("n", 1000, "число сообщений (consumer-groups)")
	batch := flag.Int("batch", 50, "размер батча XREADGROUP (consumer-groups)")
	workers := flag.Int("workers", 20, "число горутин (race-without-lua / atomic-with-lua)")
	iterations := flag.Int("iterations", 500, "итераций на горутину (race-without-lua / atomic-with-lua)")
	swapOrder := flag.Bool("swap-order", false, "eval-vs-function: гонять серию FCALL до серии EVAL (обратная ориентация — отделяет эффект руки от эффекта порядка)")
	flag.Parse()

	ctx := context.Background()
	poolSize := *workers + 10
	rdb := redis.NewClient(&redis.Options{Addr: addrFromEnv(), PoolSize: poolSize})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		fmt.Fprintln(os.Stderr, "ping failed:", err)
		os.Exit(1)
	}

	switch *scenario {
	case "consumer-groups":
		consumerGroups(ctx, rdb, *n, *batch)
	case "race-without-lua":
		runRace(ctx, rdb, "race-without-lua", "race:counter:no-lua", *workers, *iterations, false)
	case "atomic-with-lua":
		runRace(ctx, rdb, "atomic-with-lua", "race:counter:lua", *workers, *iterations, true)
	case "eval-vs-function":
		evalVsFunction(ctx, rdb, *swapOrder)
	default:
		fmt.Fprintln(os.Stderr, "unknown -scenario, expected: consumer-groups | race-without-lua | atomic-with-lua | eval-vs-function")
		os.Exit(1)
	}
}
