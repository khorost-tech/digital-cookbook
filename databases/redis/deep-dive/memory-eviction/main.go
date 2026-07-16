// Стенд #5: память и вытеснение (maxmemory-policy живьём).
//
// Контейнер поднимается с ограниченным maxmemory (64mb, см.
// compose/base.yml, переменные REDIS_MAXMEMORY/REDIS_MAXMEMORY_POLICY) —
// это единственный способ реально загнать сервер в нехватку памяти, а не
// просто прочитать документацию про LRU/LFU.
//
// Два сценария (-scenario):
//
//   - fill-until-oom: заливает hot-ключи (часто перечитываются — сигнал для
//     LRU/LFU) и cold-ключи (пишутся один раз и больше не трогаются), затем
//     непрерывно доливает filler-ключи, пока вытеснение не выйдет на
//     устойчивый режим (evicted_keys растёт, used_memory держится у потолка)
//     — или, для noeviction, пока сервер не начнёт отвечать
//     "OOM command not allowed". Считает, сколько hot и сколько cold
//     ключей пережили это.
//
//   - volatile-ttl-degenerate: та же заливка, но БЕЗ единого TTL — при
//     policy=volatile-ttl это по определению означает пустое
//     volatile-множество: Redis не может вытеснить то, чего нет в
//     volatile-множестве, и вырождается в поведение noeviction (OOM
//     вместо вытеснения). Отдельный сценарий, чтобы не путать эту
//     деградацию с «нормальным» результатом volatile-ttl из
//     fill-until-oom (там cold-ключам TTL ставится намеренно).
//
// Честность измерения (см. также README/FIXTURES, раздел «Стенд #5»):
//   - LRU и LFU в Redis — приближённые (sampled, maxmemory-samples,
//     по умолчанию 5): вытесняется не глобально самый старый/редкий ключ, а
//     худший среди случайной выборки. Несовершенная выживаемость (часть hot
//     вытеснена, часть cold выжила) — это РЕАЛЬНЫЙ результат, не брак
//     сценария; подгонять числа под чистые 100%/0% нельзя.
//   - Ни один путь не должен выглядеть как «чистый» результат сломанного
//     прогона: если maxmemory не был реально достигнут (used_memory_peak
//     не приблизился к лимиту) или вытеснение не сработало там, где
//     политика обязана вытеснять — это фатальная ошибка прогона (os.Exit(1)),
//     а не «0 evicted» в отчёте.
//   - Ожидаемая ошибка "OOM command not allowed" (noeviction; либо
//     degenerate-сценарий volatile-ttl) ловится и фиксируется как результат
//     измерения. Любая ДРУГАЯ ошибка записи — признак сломанного прогона и
//     завершает процесс немедленно.
//
// Адрес Redis/Valkey читается из REDIS_ADDR, по умолчанию 127.0.0.1:6379.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
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

// must проверяет ошибку записи, которая НЕ является измеряемым результатом
// (в отличие от ожидаемого OOM в filler-цикле — тот разбирается по месту, в
// fillUntilOOM/volatileTTLDegenerate), и завершает процесс, если она есть.
// Молча проглоченная ошибка здесь означала бы, что итог о выживших hot/cold
// посчитан по недозаписанному датасету.
func must(label string, cmd redis.Cmder) {
	if err := cmd.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: ошибка записи: %v\n", label, err)
		os.Exit(1)
	}
}

func configGetStr(ctx context.Context, rdb *redis.Client, name string) string {
	m, err := rdb.ConfigGet(ctx, name).Result()
	if err != nil {
		fmt.Fprintf(os.Stderr, "CONFIG GET %s: ошибка: %v\n", name, err)
		os.Exit(1)
	}
	v, ok := m[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "CONFIG GET %s: параметр не найден\n", name)
		os.Exit(1)
	}
	return v
}

func parseInfo(text string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(text, "\r\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		m[parts[0]] = parts[1]
	}
	return m
}

func infoSection(ctx context.Context, rdb *redis.Client, section string) map[string]string {
	text, err := rdb.Info(ctx, section).Result()
	if err != nil {
		fmt.Fprintf(os.Stderr, "INFO %s: ошибка: %v\n", section, err)
		os.Exit(1)
	}
	return parseInfo(text)
}

func humanMB(bytes int64) string {
	return fmt.Sprintf("%.1fMB", float64(bytes)/1024/1024)
}

// evictedKeysNow читает evicted_keys из INFO stats. Важно: это счётчик за
// время жизни ПРОЦЕССА redis-server, FLUSHALL его не обнуляет (обнуляет
// только CONFIG RESETSTAT или рестарт сервера). ops/eviction-demo.sh всегда
// поднимает свежий контейнер под каждую ячейку матрицы, так что там это не
// проблема, но сам бинарник не должен полагаться на дисциплину вызывающего
// — поэтому baseline снимается явно (см. использование ниже) и вычитается,
// чтобы результат был верным даже при повторном запуске против уже
// пожившего контейнера.
func evictedKeysNow(ctx context.Context, rdb *redis.Client) int64 {
	stats := infoSection(ctx, rdb, "stats")
	n, _ := strconv.ParseInt(stats["evicted_keys"], 10, 64)
	return n
}

// countExisting считает, сколько ключей вида prefix+i (i от 0 до n-1) всё
// ещё существуют — через один pipeline EXISTS, а не n отдельных запросов.
// Ошибка pipeline фатальна: молча проглоченная ошибка здесь могла бы
// занизить число выживших и выдать это за «вытеснено больше, чем на самом
// деле».
func countExisting(ctx context.Context, rdb *redis.Client, prefix string, n int) int {
	if n == 0 {
		return 0
	}
	pipe := rdb.Pipeline()
	cmds := make([]*redis.IntCmd, n)
	for i := 0; i < n; i++ {
		cmds[i] = pipe.Exists(ctx, fmt.Sprintf("%s%d", prefix, i))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "countExisting(%s): ошибка pipeline EXISTS: %v\n", prefix, err)
		os.Exit(1)
	}
	total := 0
	for _, c := range cmds {
		v, err := c.Result()
		if err != nil {
			fmt.Fprintf(os.Stderr, "countExisting(%s): ошибка EXISTS: %v\n", prefix, err)
			os.Exit(1)
		}
		total += int(v)
	}
	return total
}

// freqStats возвращает min/median/max счётчика LFU (OBJECT FREQ) по живым
// ключам заданного класса. Нужно, чтобы объяснение результата LFU опиралось
// на ИЗМЕРЕННЫЕ счётчики, а не на пересказ того, как LFU устроен по
// исходникам: если у cold и filler счётчики совпадают, то «LFU не защитил
// cold специально» — наблюдение, а не гипотеза. OBJECT FREQ доступен только
// при maxmemory-policy=allkeys-lfu/volatile-lfu, поэтому зовётся только оттуда.
func freqStats(ctx context.Context, rdb *redis.Client, prefix string, n int) (minV, medV, maxV int64, count int) {
	pipe := rdb.Pipeline()
	cmds := make([]*redis.IntCmd, n)
	for i := 0; i < n; i++ {
		cmds[i] = pipe.ObjectFreq(ctx, fmt.Sprintf("%s%d", prefix, i))
	}
	// Ошибку Exec не проверяем как фатальную: часть ключей уже вытеснена,
	// и на них OBJECT FREQ вернёт redis.Nil — это ожидаемо. Каждую команду
	// разбираем поштучно ниже, где redis.Nil пропускается, а любая другая
	// ошибка — фатальна.
	_, _ = pipe.Exec(ctx)

	var vals []int64
	for _, c := range cmds {
		v, err := c.Result()
		if err == redis.Nil {
			continue // ключ вытеснен — в статистику живых не входит
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "OBJECT FREQ(%s): неожиданная ошибка: %v\n", prefix, err)
			os.Exit(1)
		}
		vals = append(vals, v)
	}
	if len(vals) == 0 {
		return 0, 0, 0, 0
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	return vals[0], vals[len(vals)/2], vals[len(vals)-1], len(vals)
}

// reportLFUCounters печатает распределение счётчиков LFU по трём классам
// ключей. Это то, что превращает «LFU почему-то почти не трогал cold» в
// проверяемое объяснение: видно, отличается ли счётчик cold от filler вообще.
func reportLFUCounters(ctx context.Context, rdb *redis.Client, hotKeys, coldKeys, fillerWritten int) {
	fmt.Println("--- OBJECT FREQ (счётчики LFU по живым ключам: min/медиана/max) ---")
	for _, cl := range []struct {
		label  string
		prefix string
		n      int
	}{
		{"hot", "hot:", hotKeys},
		{"cold", "cold:", coldKeys},
		{"filler", "filler:", fillerWritten},
	} {
		// filler может быть очень многочисленным (~12000+) — берём выборку.
		// Выборка — ПЕРВЫЕ n по индексу, то есть для filler это самые СТАРЫЕ
		// записанные ключи. Направление консервативное: у старых filler-ключей
		// счётчик LFU имел больше всего времени просесть от затухания, так что
		// если даже они не отличаются от cold — вывод «LFU не различает cold и
		// filler» тем более верен для свежих. Область выборки указана в
		// печати ниже («из первых N»), чтобы её не пришлось угадывать.
		n := cl.n
		if n > 2000 {
			n = 2000
		}
		lo, med, hi, cnt := freqStats(ctx, rdb, cl.prefix, n)
		fmt.Printf("%s: freq min=%d медиана=%d max=%d (по %d живым из первых %d)\n",
			cl.label, lo, med, hi, cnt, n)
	}
}

// rereadHot пробегает GET по всем hot-ключам одним pipeline — сигнал
// "недавно использовался" для LRU (idle=0) и инкремент частотного счётчика
// для LFU. Ошибки здесь не фатальны сами по себе (часть hot-ключей уже
// могла быть вытеснена — это ожидаемо и фиксируется отдельно через
// countExisting в конце), но должны быть реально выполненным pipeline, а не
// пропущенным шагом.
func rereadHot(ctx context.Context, rdb *redis.Client, hotKeys int) {
	pipe := rdb.Pipeline()
	for i := 0; i < hotKeys; i++ {
		pipe.Get(ctx, fmt.Sprintf("hot:%d", i))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		// Exec с pipeline возвращает первую встреченную ошибку команды;
		// redis.Nil (ключа нет — уже вытеснен) — ожидаемый шум, не повод
		// падать. Любая другая ошибка (обрыв соединения и т.п.) означает,
		// что LRU/LFU-сигнал реально не был отправлен — это ломает
		// методику, поэтому фатально.
		fmt.Fprintf(os.Stderr, "rereadHot: неожиданная ошибка pipeline: %v\n", err)
		os.Exit(1)
	}
}

const (
	hotValSize    = 1024
	coldValSize   = 1024
	fillerValSize = 4096

	rereadEvery = 20  // раундов между перечитыванием hot-ключей
	checkEvery  = 100 // раундов между снятием INFO memory/stats
	hardCap     = 150000
	graceRounds = 200 // доп. раунды после выхода на устойчивое вытеснение — для статистики выживаемости
)

// fillHotAndCold заливает hot- и cold-ключи.
//
// ВАЖНО, и это влияет на чтение результата: hot-ключи ВСЕГДА пишутся с ttl=0,
// то есть никогда не попадают в volatile-множество. Под volatile-* политиками
// это делает их НЕВЫТЕСНЯЕМЫМИ ПО ПОСТРОЕНИЮ: их 100% выживаемость там —
// свойство сценария, а не измерение того, как политика ранжирует ключи. Под
// allkeys-* политиками таких поблажек нет, и 100% там — настоящее наблюдение.
// В FIXTURES соответствующие ячейки помечены отдельно; не сводить их в одну
// колонку с allkeys-* без пометки.
func fillHotAndCold(ctx context.Context, rdb *redis.Client, hotKeys, coldKeys int, coldTTL bool) {
	hotVal := strings.Repeat("x", hotValSize)
	coldVal := strings.Repeat("x", coldValSize)

	for i := 0; i < hotKeys; i++ {
		must(fmt.Sprintf("SET hot:%d", i), rdb.Set(ctx, fmt.Sprintf("hot:%d", i), hotVal, 0))
	}
	for i := 0; i < coldKeys; i++ {
		var ttl time.Duration
		if coldTTL {
			// разброс TTL (5–7 минут) — дольше, чем длится сам прогон
			// (десятки секунд), так что естественное истечение по часам
			// не подмешивается к результату: единственный способ ключу
			// исчезнуть в этом прогоне — реальное вытеснение.
			//
			// ВНИМАНИЕ: filler-ключи получают TTL из ТОГО ЖЕ диапазона
			// [300,420) (см. fillUntilOOM). Это означает, что volatile-ttl
			// (вытесняет ключ с ближайшим истечением) НЕ МОЖЕТ отличить cold
			// от filler по существу — распределения совпадают. Выживаемость
			// cold под volatile-ttl объясняется тем же, чем под LFU:
			// численным доминированием filler, а не «политика сберегла cold».
			// Слабый перекос в сторону cold остаётся лишь потому, что их TTL
			// проставлены на десятки секунд раньше, т.е. истекают раньше.
			ttl = time.Duration(300+i%120) * time.Second
		}
		must(fmt.Sprintf("SET cold:%d", i), rdb.Set(ctx, fmt.Sprintf("cold:%d", i), coldVal, ttl))
	}
}

// requireMaxmemory проверяет, что сервер реально поднят с ограничением
// памяти и заявленной политикой — без этой проверки сценарий мог бы
// «успешно» отчитаться нулями просто потому, что maxmemory=0 (без лимита) и
// вытеснение в принципе невозможно.
func requireMaxmemory(ctx context.Context, rdb *redis.Client, wantPolicy string) (maxMemBytes int64) {
	maxMemStr := configGetStr(ctx, rdb, "maxmemory")
	maxMemBytes, err := strconv.ParseInt(maxMemStr, 10, 64)
	if err != nil || maxMemBytes <= 0 {
		fmt.Fprintf(os.Stderr, "maxmemory=%q — сервер поднят без ограничения памяти, сценарий не может создать давление, это ошибка окружения, не результат\n", maxMemStr)
		os.Exit(1)
	}
	serverPolicy := configGetStr(ctx, rdb, "maxmemory-policy")
	if serverPolicy != wantPolicy {
		fmt.Fprintf(os.Stderr, "maxmemory-policy на сервере=%q, ожидали %q (-policy) — контейнер поднят не в том режиме\n", serverPolicy, wantPolicy)
		os.Exit(1)
	}
	samples := configGetStr(ctx, rdb, "maxmemory-samples")
	fmt.Printf("конфигурация: maxmemory=%s (%s), maxmemory-policy=%s, maxmemory-samples=%s\n",
		maxMemStr, humanMB(maxMemBytes), serverPolicy, samples)
	if wantPolicy == "allkeys-lfu" {
		// Для LFU результат нельзя интерпретировать, не зная этих двух:
		// lfu-log-factor задаёт, насколько логарифмически насыщается счётчик
		// обращений, а lfu-decay-time — за сколько минут простоя счётчик
		// уменьшается на единицу. Если прогон короче lfu-decay-time, затухание
		// практически не успевает сработать, и «редко используемые» ключи не
		// отличаются по счётчику от только что созданных.
		fmt.Printf("конфигурация LFU: lfu-log-factor=%s, lfu-decay-time=%s (минут)\n",
			configGetStr(ctx, rdb, "lfu-log-factor"), configGetStr(ctx, rdb, "lfu-decay-time"))
	}
	return maxMemBytes
}

// Report — итог сценария fill-until-oom.
type Report struct {
	Policy                    string
	HotTotal, ColdTotal       int
	SurvivedHot, SurvivedCold int
	FillerWritten             int
	EvictedKeys               int64
	ExpiredKeys               int64
	UsedMemory                int64
	UsedMemoryPeak            int64
	MaxMemoryBytes            int64
	FragRatio                 string
	OOMHit                    bool
}

// SummaryLine — однострочная машиночитаемая выжимка прогона. Человекочитаемый
// вывод печатается по ходу сценария и удобен для чтения глазами, но выдирать
// из него числа в FIXTURES приходится бы регулярками по нескольким строкам.
// Эта строка помечена префиксом RESULT и содержит всё существенное разом:
// `grep '^RESULT ' scratchout/*.log` даёт готовую таблицу по всей матрице.
func (r Report) SummaryLine() string {
	return fmt.Sprintf(
		"RESULT policy=%s hot_survived=%d/%d cold_survived=%d/%d filler_written=%d "+
			"evicted=%d expired=%d oom=%v used_memory=%d peak=%d maxmemory=%d frag_ratio=%s",
		r.Policy, r.SurvivedHot, r.HotTotal, r.SurvivedCold, r.ColdTotal, r.FillerWritten,
		r.EvictedKeys, r.ExpiredKeys, r.OOMHit, r.UsedMemory, r.UsedMemoryPeak,
		r.MaxMemoryBytes, r.FragRatio)
}

func fillUntilOOM(ctx context.Context, rdb *redis.Client, policy string, hotKeys, coldKeys int) Report {
	must("FLUSHALL", rdb.FlushAll(ctx))
	evictedBaseline := evictedKeysNow(ctx, rdb)

	maxMemBytes := requireMaxmemory(ctx, rdb, policy)
	volatileCold := policy == "volatile-ttl"

	fillHotAndCold(ctx, rdb, hotKeys, coldKeys, volatileCold)
	fmt.Printf("залито: hot=%d (без TTL), cold=%d (%s)\n", hotKeys, coldKeys,
		map[bool]string{true: "с TTL 300–420с", false: "без TTL"}[volatileCold])

	fillerVal := strings.Repeat("x", fillerValSize)

	oomHit := false
	round := 0
	stableChecks := 0
	graceLeft := -1 // -1 = условие остановки ещё не выполнено

	for {
		if round%rereadEvery == 0 {
			rereadHot(ctx, rdb, hotKeys)
		}

		var fillerTTL time.Duration
		if volatileCold {
			fillerTTL = time.Duration(300+round%120) * time.Second
		}
		err := rdb.Set(ctx, fmt.Sprintf("filler:%d", round), fillerVal, fillerTTL).Err()
		round++
		if err != nil {
			if policy == "noeviction" && strings.Contains(err.Error(), "OOM command not allowed") {
				oomHit = true
				fmt.Printf("filler:%d: \"OOM command not allowed\" (ожидаемо для noeviction) — остановка после %d filler-записей\n", round-1, round)
				break
			}
			// Любая другая ошибка (включая OOM там, где политика ОБЯЗАНА
			// вытеснять, а не отказывать) — признак сломанного прогона, не
			// измеряемый результат.
			fmt.Fprintf(os.Stderr, "SET filler:%d: неожиданная ошибка записи (прогон сломан): %v\n", round-1, err)
			os.Exit(1)
		}

		if round%checkEvery == 0 {
			info := infoSection(ctx, rdb, "memory")
			stats := infoSection(ctx, rdb, "stats")
			used, _ := strconv.ParseInt(info["used_memory"], 10, 64)
			evictedTotal, _ := strconv.ParseInt(stats["evicted_keys"], 10, 64)
			evicted := evictedTotal - evictedBaseline
			fmt.Printf("round=%d filler_written=%d used_memory=%d (%.1f%% of maxmemory) evicted_keys=%d (за этот прогон)\n",
				round, round, used, 100*float64(used)/float64(maxMemBytes), evicted)

			if policy != "noeviction" {
				nearFull := float64(used) >= 0.9*float64(maxMemBytes)
				if evicted > 0 && nearFull {
					stableChecks++
				} else {
					stableChecks = 0
				}
				if stableChecks >= 2 && graceLeft < 0 {
					graceLeft = graceRounds
				}
			}
		}

		if graceLeft == 0 {
			break
		}
		if graceLeft > 0 {
			graceLeft--
		}

		if round >= hardCap {
			fmt.Fprintf(os.Stderr, "достигнут hardCap=%d filler-записей, а устойчивое вытеснение/OOM так и не наступили — прогон сломан (maxmemory не соблюдается сервером?)\n", hardCap)
			os.Exit(1)
		}
	}

	finalMem := infoSection(ctx, rdb, "memory")
	finalStats := infoSection(ctx, rdb, "stats")

	usedMemory, _ := strconv.ParseInt(finalMem["used_memory"], 10, 64)
	usedMemoryPeak, _ := strconv.ParseInt(finalMem["used_memory_peak"], 10, 64)
	evictedTotal, _ := strconv.ParseInt(finalStats["evicted_keys"], 10, 64)
	evictedKeys := evictedTotal - evictedBaseline
	expiredKeys, _ := strconv.ParseInt(finalStats["expired_keys"], 10, 64)
	fragRatio := finalMem["mem_fragmentation_ratio"]

	survivedHot := countExisting(ctx, rdb, "hot:", hotKeys)
	survivedCold := countExisting(ctx, rdb, "cold:", coldKeys)

	// Санити-проверки: ни один путь не должен давать «чистый» результат по
	// сломанному прогону (см. пакетный комментарий вверху файла).
	if policy == "noeviction" {
		if !oomHit {
			fmt.Fprintln(os.Stderr, "noeviction: \"OOM command not allowed\" ни разу не встретился — maxmemory не был реально достигнут, прогон сломан")
			os.Exit(1)
		}
		if evictedKeys != 0 {
			fmt.Fprintf(os.Stderr, "noeviction: evicted_keys=%d — при noeviction вытеснения быть не должно вообще, сервер поднят не в том режиме\n", evictedKeys)
			os.Exit(1)
		}
	} else {
		if evictedKeys == 0 {
			fmt.Fprintf(os.Stderr, "%s: evicted_keys=0 — вытеснение не сработало, «0 вытеснено» здесь означает сломанный прогон, а не результат\n", policy)
			os.Exit(1)
		}
	}
	if usedMemoryPeak < int64(0.9*float64(maxMemBytes)) {
		fmt.Fprintf(os.Stderr, "used_memory_peak=%d не приблизился к maxmemory=%d (<90%%) — память реально не была под давлением\n", usedMemoryPeak, maxMemBytes)
		os.Exit(1)
	}
	// TTL в сценарии заведомо длиннее прогона (300–420с против десятков
	// секунд), поэтому истечь по часам не должен НИ ОДИН ключ. Проверять это
	// обязательно: число вытесненных cold считается ВЫЧИТАНИЕМ выживших из
	// исходных, так что истёкший по TTL ключ молча приплюсовался бы к
	// «вытесненным» и завысил бы мнимую разборчивость политики.
	if expiredKeys != 0 {
		fmt.Fprintf(os.Stderr, "expired_keys=%d — ключи истекли по TTL, хотя TTL заведомо длиннее прогона; вытеснение и истечение смешались, результат по классам недостоверен\n", expiredKeys)
		os.Exit(1)
	}

	// Состав keyspace на момент финала — не украшение отчёта, а то, без чего
	// выживаемость нельзя интерпретировать. LRU/LFU в Redis приближённые:
	// вытесняется худший ключ из СЛУЧАЙНОЙ выборки (maxmemory-samples), а
	// выборка берётся из всего keyspace. Если filler численно доминирует, то
	// большинство вытеснений приходится на filler просто по вероятности
	// попадания в выборку, а не потому, что политика «решила» их защитить.
	dbSize, err := rdb.DBSize(ctx).Result()
	if err != nil {
		fmt.Fprintf(os.Stderr, "DBSIZE: ошибка: %v\n", err)
		os.Exit(1)
	}
	survivedFiller := int(dbSize) - survivedHot - survivedCold
	evictedHot := hotKeys - survivedHot
	evictedCold := coldKeys - survivedCold

	fmt.Println("=== итог ===")
	fmt.Printf("policy=%s hot: %d/%d выжило (%.1f%%)  cold: %d/%d выжило (%.1f%%)\n",
		policy, survivedHot, hotKeys, 100*float64(survivedHot)/float64(hotKeys),
		survivedCold, coldKeys, 100*float64(survivedCold)/float64(coldKeys))
	fmt.Printf("filler_written=%d evicted_keys=%d expired_keys=%d oom_hit=%v\n", round, evictedKeys, expiredKeys, oomHit)
	fmt.Printf("состав keyspace на финале: dbsize=%d = hot %d + cold %d + filler %d\n",
		dbSize, survivedHot, survivedCold, survivedFiller)
	if dbSize > 0 {
		fmt.Printf("доля в keyspace: hot=%.1f%% cold=%.1f%% filler=%.1f%% (из неё берётся выборка maxmemory-samples)\n",
			100*float64(survivedHot)/float64(dbSize), 100*float64(survivedCold)/float64(dbSize),
			100*float64(survivedFiller)/float64(dbSize))
	}
	fmt.Printf("вытеснено по классам: hot=%d cold=%d filler=%d (сумма %d, evicted_keys=%d)\n",
		evictedHot, evictedCold, int(evictedKeys)-evictedHot-evictedCold,
		evictedHot+evictedCold+(int(evictedKeys)-evictedHot-evictedCold), evictedKeys)
	fmt.Printf("used_memory=%d used_memory_peak=%d maxmemory=%d mem_fragmentation_ratio=%s\n",
		usedMemory, usedMemoryPeak, maxMemBytes, fragRatio)

	if policy == "allkeys-lfu" {
		reportLFUCounters(ctx, rdb, hotKeys, coldKeys, round)
	}

	return Report{
		Policy: policy, HotTotal: hotKeys, ColdTotal: coldKeys,
		SurvivedHot: survivedHot, SurvivedCold: survivedCold,
		FillerWritten: round, EvictedKeys: evictedKeys, ExpiredKeys: expiredKeys,
		UsedMemory: usedMemory, UsedMemoryPeak: usedMemoryPeak, MaxMemoryBytes: maxMemBytes,
		FragRatio: fragRatio, OOMHit: oomHit,
	}
}

// volatileTTLDegenerate заливает hot/cold/filler БЕЗ единого TTL при
// maxmemory-policy=volatile-ttl. По документации Redis это должно выродиться
// в поведение noeviction: вытеснять нечего (volatile-множество пусто),
// сервер обязан отвечать OOM. Проверяет это живьём, а не по документации.
func volatileTTLDegenerate(ctx context.Context, rdb *redis.Client, hotKeys, coldKeys int) {
	must("FLUSHALL", rdb.FlushAll(ctx))
	evictedBaseline := evictedKeysNow(ctx, rdb)

	maxMemBytes := requireMaxmemory(ctx, rdb, "volatile-ttl")
	fillHotAndCold(ctx, rdb, hotKeys, coldKeys, false)
	fmt.Printf("залито: hot=%d, cold=%d — оба БЕЗ TTL (проверка вырождения volatile-ttl)\n", hotKeys, coldKeys)

	fillerVal := strings.Repeat("x", fillerValSize)
	oomHit := false
	round := 0
	for {
		err := rdb.Set(ctx, fmt.Sprintf("filler:%d", round), fillerVal, 0).Err()
		round++
		if err != nil {
			if strings.Contains(err.Error(), "OOM command not allowed") {
				oomHit = true
				fmt.Printf("filler:%d: \"OOM command not allowed\" — подтверждено, volatile-ttl без volatile-ключей ведёт себя как noeviction (остановка после %d filler-записей)\n", round-1, round)
				break
			}
			fmt.Fprintf(os.Stderr, "SET filler:%d: неожиданная ошибка записи (прогон сломан): %v\n", round-1, err)
			os.Exit(1)
		}
		if round >= hardCap {
			fmt.Fprintf(os.Stderr, "достигнут hardCap=%d filler-записей без единого OOM — гипотеза о вырождении volatile-ttl не подтвердилась (или maxmemory не соблюдается)\n", hardCap)
			os.Exit(1)
		}
	}

	finalMem := infoSection(ctx, rdb, "memory")
	finalStats := infoSection(ctx, rdb, "stats")
	usedMemoryPeak, _ := strconv.ParseInt(finalMem["used_memory_peak"], 10, 64)
	evictedTotal, _ := strconv.ParseInt(finalStats["evicted_keys"], 10, 64)
	evictedKeys := evictedTotal - evictedBaseline

	if !oomHit {
		fmt.Fprintln(os.Stderr, "volatile-ttl-degenerate: OOM ни разу не встретился — прогон сломан")
		os.Exit(1)
	}
	if usedMemoryPeak < int64(0.9*float64(maxMemBytes)) {
		fmt.Fprintf(os.Stderr, "used_memory_peak=%d не приблизился к maxmemory=%d (<90%%) — память реально не была под давлением\n", usedMemoryPeak, maxMemBytes)
		os.Exit(1)
	}

	fmt.Println("=== итог (volatile-ttl-degenerate) ===")
	fmt.Printf("filler_written=%d evicted_keys=%d oom_hit=%v used_memory_peak=%d maxmemory=%d\n",
		round, evictedKeys, oomHit, usedMemoryPeak, maxMemBytes)
	if evictedKeys != 0 {
		fmt.Printf("ПРИМЕЧАНИЕ: evicted_keys=%d — не строго ноль; часть ключей всё же была вытеснена до того, как volatile-множество опустело окончательно\n", evictedKeys)
	}
}

func main() {
	scenario := flag.String("scenario", "", "fill-until-oom | volatile-ttl-degenerate")
	policy := flag.String("policy", "", "allkeys-lru | allkeys-lfu | volatile-ttl | noeviction (должна совпадать с maxmemory-policy сервера)")
	hot := flag.Int("hot", 200, "число hot-ключей (часто перечитываемых)")
	cold := flag.Int("cold", 2000, "число cold-ключей (пишутся один раз)")
	flag.Parse()

	switch *policy {
	case "allkeys-lru", "allkeys-lfu", "volatile-ttl", "noeviction":
	default:
		fmt.Fprintln(os.Stderr, "нужен -policy: allkeys-lru | allkeys-lfu | volatile-ttl | noeviction")
		os.Exit(1)
	}

	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: addrFromEnv()})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		fmt.Fprintln(os.Stderr, "ping failed:", err)
		os.Exit(1)
	}

	switch *scenario {
	case "fill-until-oom":
		fmt.Println(fillUntilOOM(ctx, rdb, *policy, *hot, *cold).SummaryLine())
	case "volatile-ttl-degenerate":
		if *policy != "volatile-ttl" {
			fmt.Fprintln(os.Stderr, "volatile-ttl-degenerate имеет смысл только при -policy volatile-ttl")
			os.Exit(1)
		}
		volatileTTLDegenerate(ctx, rdb, *hot, *cold)
	default:
		fmt.Fprintln(os.Stderr, "unknown -scenario, expected: fill-until-oom | volatile-ttl-degenerate")
		os.Exit(1)
	}
}
