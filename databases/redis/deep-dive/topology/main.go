// Стенд #4: репликация, Cluster, Sentinel — полный live-прогон.
//
// Четыре сценария (-scenario):
//
//   - cluster-writes-during-reshard: пишет N ключей `resh:<i>` через
//     redis.ClusterClient, пока СНАРУЖИ (ops/topology-demo.sh) параллельно
//     идёт `redis-cli --cluster reshard`. Считает реальные MOVED/ASK
//     редиректы (см. redirectCounter ниже) и после записи проверяет каждый
//     ключ через GET — «0 потерянных записей» здесь означает «GET нашёл все
//     N ключей», а не «ни одна команда не вернула ошибку» (go-redis
//     по умолчанию сам следует MOVED/ASK, так что ошибка до клиента и не
//     должна доходить в норме).
//
//   - cluster-failover-writes: пишет ключи `fo:<i>` в тугом цикле заданное
//     время, пока СНАРУЖИ убивают мастера (`docker kill`). Ловит первую
//     деградацию (настоящую ошибку ИЛИ аномально долгий, но успешный вызов —
//     см. комментарий у writeLoop, почему это два разных случая) и момент
//     восстановления — это окно и есть клиентски наблюдаемое время failover.
//     «0 деградаций за весь прогон» — не повод завершиться с ошибкой: живые
//     прогоны показали, что go-redis иногда полностью поглощает failover
//     без видимого эффекта на клиенте; реальность самого kill и промоушена
//     проверяется снаружи, в ops/topology-demo.sh, docker-уровнем.
//
//   - sentinel-failover: то же самое (тугой цикл записи `sfo:<i>`) поверх
//     redis.NewFailoverClient (клиент, знающий про Sentinel), ПАРАЛЛЕЛЬНО —
//     отдельная горутина каждые 300мс спрашивает у одного из Sentinel
//     `SENTINEL get-master-addr-by-name` и логирует момент, когда адрес
//     реально поменялся. Так в одном прогоне видно и «когда Sentinel
//     признал промоушен», и «когда клиент реально восстановил запись» —
//     это разные моменты, и разница между ними по конструкции.
//
//   - split-brain-writer: отдельный «изолированный» клиент (обычный
//     redis.Client, БЕЗ Sentinel — то есть подключается напрямую к адресу
//     из REDIS_ADDR) пишет `splitbrain:marker:<i>` в тугом цикле. Он
//     предназначен для запуска в контейнере, у которого есть прямой сетевой
//     путь к старому мастеру, даже когда основной сети (redis-sentinel-net)
//     мастер лишился — реализация партиции (docker network disconnect) и
//     проверка того, что случилось с этими ключами после воссоединения сети
//     — в ops/topology-demo.sh, не здесь.
//
// Адреса — из REDIS_CLUSTER_ADDRS / REDIS_SENTINEL_ADDRS (CSV) и REDIS_ADDR
// (одиночный адрес, тот же контракт, что и в структурах/eventloop/persistence).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// --- env-контракт ---

func addrFromEnv() string {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	return addr
}

func csvAddrsFromEnv(name string) []string {
	raw := os.Getenv(name)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func clusterAddrsFromEnv() []string {
	addrs := csvAddrsFromEnv("REDIS_CLUSTER_ADDRS")
	if len(addrs) == 0 {
		fmt.Fprintln(os.Stderr, "REDIS_CLUSTER_ADDRS не задан или пуст (ожидается CSV из 6 host:port)")
		os.Exit(1)
	}
	return addrs
}

func sentinelAddrsFromEnv() []string {
	addrs := csvAddrsFromEnv("REDIS_SENTINEL_ADDRS")
	if len(addrs) == 0 {
		fmt.Fprintln(os.Stderr, "REDIS_SENTINEL_ADDRS не задан или пуст (ожидается CSV из 3 host:port)")
		os.Exit(1)
	}
	return addrs
}

func sentinelMasterFromEnv() string {
	name := os.Getenv("REDIS_SENTINEL_MASTER")
	if name == "" {
		name = "mymaster"
	}
	return name
}

// --- MOVED/ASK счётчик ---
//
// go-redis ClusterClient по умолчанию (MaxRedirects не выставлен явно —
// значит 3) САМ прозрачно следует MOVED/ASK внутри c.process(): вызывающий
// код обычно ни разу не видит саму ошибку MOVED/ASK, только конечный
// результат. Хук на уровне ClusterClient.AddHook оборачивает как раз этот
// верхнеуровневый c.process — то есть тоже увидел бы только финал, не каждый
// отдельный редирект.
//
// ClusterClient.OnNewNode(fn) — это единственный публичный способ достучаться
// до КАЖДОГО отдельного узлового *redis.Client (по одному на физический
// узел кластера) в момент его создания и повесить хук именно на него: этот
// хук оборачивает node.Client.Process(ctx, cmd) — ровно ту функцию, которую
// c.process() дёргает на каждой попытке внутри своего redirect-цикла,
// включая все промежуточные попытки, кончившиеся MOVED/ASK. Так реальные
// редиректы становятся видимы и считаемы, не трогая транспарентную (и
// корректную) обработку MaxRedirects по умолчанию — команда всё равно в
// итоге успешно доезжает до правильного узла, мы просто параллельно считаем,
// сколько раз сервер её туда переслал.
type redirectCounter struct {
	moved int64
	ask   int64
}

func (rc *redirectCounter) DialHook(next redis.DialHook) redis.DialHook { return next }

func (rc *redirectCounter) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if err != nil {
			if _, ok := redis.IsMovedError(err); ok {
				atomic.AddInt64(&rc.moved, 1)
			} else if _, ok := redis.IsAskError(err); ok {
				atomic.AddInt64(&rc.ask, 1)
			}
		}
		return err
	}
}

func (rc *redirectCounter) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func newClusterClientWithRedirectCounter(addrs []string) (*redis.ClusterClient, *redirectCounter) {
	rc := &redirectCounter{}
	rdb := redis.NewClusterClient(&redis.ClusterOptions{Addrs: addrs})
	// Регистрируем ДО первой команды — GetOrCreate вызывается лениво при
	// первом обращении к узлу (в т.ч. к изначальным seed-адресам), поэтому
	// поздняя регистрация пропустила бы узлы, созданные до неё.
	rdb.OnNewNode(func(n *redis.Client) {
		n.AddHook(rc)
	})
	return rdb, rc
}

// --- тугой цикл записи с детектором окна недоступности ---

type writeLoopResult struct {
	attempts, success, failed, stalled int64
	// firstFailAt/recoveredAt — границы САМОГО ДЛИННОГО эпизода деградации за
	// прогон, а не последнего. Разница принципиальна: эпизодов за прогон может
	// быть несколько (килл мастера + случайный 300мс-всплеск под конец), и если
	// просто перезаписывать поля на каждом новом эпизоде, то отчёт покажет
	// последний — то есть безобидный всплеск мог бы молча выдать себя за
	// «окно failover». Ровно тот класс подмены, ради недопущения которого
	// стенд и написан, поэтому держим максимум явно.
	firstFailAt time.Time // zero, если сбоев/подвисаний не было
	recoveredAt time.Time // zero, если после сбоя не восстановилось (или сбоев не было)
	episodes    int64     // сколько всего эпизодов деградации было
	maxLatency  time.Duration
}

// writeLoop пишет ключи `<prefix>:<i>` монотонным счётчиком в тугом цикле
// (без пайплайна, один клиент) в течение duration.
//
// Важное наблюдение из реальных прогонов (не из документации): недоступность
// мастера НЕ всегда доезжает до вызывающего кода в виде ошибки. Клиент может
// просто ЗАБЛОКИРОВАТЬ один-единственный вызов Set() на всё время
// недоступности и в итоге успешно вернуть OK, когда новый мастер готов — со
// стороны вызывающего кода это не «ошибка записи», а «одна аномально долгая,
// но успешная запись». Живьём это поймано на FailoverClient (Sentinel):
// `failed=0 stalled=1 maxLatency=10.64s`. В сценарии Cluster на этом стенде
// длинный вызов, наоборот, во всех прогонах возвращал ошибку
// (`failed=1 stalled=0`, maxLatency 41.5–48.4с) — то есть какой из двух
// случаев реализуется, зависит от клиента и обстоятельств, и заранее
// полагаться на «ошибка обязательно будет» нельзя.
//
// Поэтому окно недоступности детектируется ДВУМЯ признаками сразу:
// (a) err != nil И (b) длительность отдельного вызова Set() дольше
// stallThreshold (успешный, но подозрительно долгий вызов). Детектор только
// по ошибкам пропустил бы весь Sentinel-случай целиком.
//
// При err != nil тот же ключ повторяется (i не продвигается) — иначе
// «первая успешная запись ПОСЛЕ сбоя» могла бы относиться к другому ключу,
// чем тот, что реально не прошёл. При «медленном успехе» ключ уже реально
// записан — двигаем i дальше, это не сбой записи, а сбой задержки.
//
// Прогресс (T+, текущий i, attempts/success/failed) печатается каждые 2с
// отдельной горутиной поверх atomic-счётчиков (не через res напрямую —
// иначе это была бы гонка данных с основным циклом): длинный live-прогон
// должен быть виден по ходу дела, а не только итоговой строкой после
// факта — это и обнаружило описанное выше поведение при первом прогоне.
func writeLoop(ctx context.Context, rdb redis.Cmdable, label, prefix string, duration, retryDelay, stallThreshold time.Duration) writeLoopResult {
	fmt.Printf("=== %s: тугая запись %s:<i> в течение %s (порог «подвисания» = %s) ===\n", label, prefix, duration, stallThreshold)
	start := time.Now()
	deadline := start.Add(duration)
	var res writeLoopResult
	var i int64
	var attemptsA, successA, failedA int64
	inFailure := false
	var episodeStart time.Time

	progressDone := make(chan struct{})
	progressStop := make(chan struct{})
	progressTicker := time.NewTicker(2 * time.Second)
	go func() {
		defer close(progressDone)
		for {
			select {
			case <-progressStop:
				return
			case <-progressTicker.C:
				fmt.Printf("%s: прогресс T+%s: i=%d attempts=%d success=%d failed=%d\n",
					label, time.Since(start).Round(time.Millisecond),
					atomic.LoadInt64(&i), atomic.LoadInt64(&attemptsA), atomic.LoadInt64(&successA), atomic.LoadInt64(&failedA))
			}
		}
	}()

	for time.Now().Before(deadline) {
		key := fmt.Sprintf("%s:%d", prefix, atomic.LoadInt64(&i))
		callStart := time.Now()
		err := rdb.Set(ctx, key, i, 0).Err()
		callDur := time.Since(callStart)
		res.attempts++
		atomic.StoreInt64(&attemptsA, res.attempts)

		degraded := err != nil || callDur > stallThreshold
		if degraded {
			if callDur > res.maxLatency {
				res.maxLatency = callDur
			}
			if !inFailure {
				inFailure = true
				res.episodes++
				episodeStart = callStart
				if err != nil {
					fmt.Printf("%s: эпизод #%d — первая ошибка записи на попытке #%d (T+%s): %v\n", label, res.episodes, res.attempts, callStart.Sub(start), err)
				} else {
					fmt.Printf("%s: эпизод #%d — попытка #%d (T+%s, ключ=%s) заняла аномально долго — %s (успешно, но клиент был заблокирован внутри пула/редиректа, ошибку наружу не вернул)\n",
						label, res.episodes, res.attempts, callStart.Sub(start), key, callDur)
				}
			}
			if err != nil {
				res.failed++
				atomic.StoreInt64(&failedA, res.failed)
				time.Sleep(retryDelay)
				// та же самая пара ключ/значение, ещё одна попытка — счётчик i
				// не двигаем, пока запись не подтвердится.
				continue
			}
			res.stalled++
		}

		res.success++
		atomic.StoreInt64(&successA, res.success)
		if inFailure {
			inFailure = false
			episodeEnd := time.Now()
			episodeDur := episodeEnd.Sub(episodeStart)
			// Запоминаем эпизод, только если он ДЛИННЕЕ всех предыдущих:
			// отчёт обязан показывать худшее окно за прогон, а не то, которое
			// случилось последним.
			if episodeDur > res.recoveredAt.Sub(res.firstFailAt) || res.recoveredAt.IsZero() {
				res.firstFailAt = episodeStart
				res.recoveredAt = episodeEnd
			}
			fmt.Printf("%s: эпизод #%d — восстановление на попытке #%d (T+%s), окно недоступности: %s\n",
				label, res.episodes, res.attempts, episodeEnd.Sub(start), episodeDur)
		}
		atomic.AddInt64(&i, 1)
	}
	progressTicker.Stop()
	close(progressStop)
	<-progressDone
	fmt.Printf("%s: итог — attempts=%d success=%d failed=%d stalled=%d эпизодов_деградации=%d maxLatency=%s elapsed=%s\n",
		label, res.attempts, res.success, res.failed, res.stalled, res.episodes, res.maxLatency, time.Since(start))
	if res.episodes > 1 {
		fmt.Printf("%s: ВНИМАНИЕ — эпизодов деградации было %d; ниже приводится САМЫЙ ДЛИННЫЙ, а не последний\n", label, res.episodes)
	}
	return res
}

// --- сценарий 1: запись во время resharding ---

func clusterWritesDuringReshard(ctx context.Context, n int) {
	addrs := clusterAddrsFromEnv()
	rdb, rc := newClusterClientWithRedirectCounter(addrs)
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		fmt.Fprintln(os.Stderr, "ping failed:", err)
		os.Exit(1)
	}

	fmt.Printf("=== cluster-writes-during-reshard: n=%d addrs=%v ===\n", n, addrs)
	start := time.Now()
	var failed int
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("resh:%d", i)
		if err := rdb.Set(ctx, key, i, 0).Err(); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "SET %s: ошибка (не восстановлено внутренним редиректом go-redis): %v\n", key, err)
		}
	}
	elapsed := time.Since(start)
	fmt.Printf("запись завершена: n=%d failed=%d elapsed=%s (%.1f ops/s) MOVED=%d ASK=%d\n",
		n, failed, elapsed, float64(n)/elapsed.Seconds(), atomic.LoadInt64(&rc.moved), atomic.LoadInt64(&rc.ask))

	// Верификация: реально ли все ключи читаются обратно. Это и есть
	// проверка «0 потерянных записей» — не отсутствие ошибок на SET (их
	// в норме и не должно быть видно, см. комментарий у redirectCounter),
	// а фактическое присутствие данных.
	fmt.Println("=== верификация: GET каждого ключа ===")
	verifyStart := time.Now()
	missing := 0
	wrong := 0
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("resh:%d", i)
		val, err := rdb.Get(ctx, key).Int()
		if err != nil {
			missing++
			fmt.Fprintf(os.Stderr, "GET %s: отсутствует или ошибка: %v\n", key, err)
			continue
		}
		if val != i {
			wrong++
			fmt.Fprintf(os.Stderr, "GET %s: ожидалось %d, получено %d\n", key, i, val)
		}
	}
	fmt.Printf("верификация завершена за %s: missing=%d wrong=%d из n=%d\n", time.Since(verifyStart), missing, wrong, n)

	if failed > 0 || missing > 0 || wrong > 0 {
		fmt.Fprintf(os.Stderr, "cluster-writes-during-reshard: реальные потери/ошибки — failed=%d missing=%d wrong=%d\n", failed, missing, wrong)
		os.Exit(1)
	}
	fmt.Println("cluster-writes-during-reshard: 0 потерянных записей, все ключи верифицированы")
}

// --- сценарий 2: запись во время killa мастера Cluster ---

func clusterFailoverWrites(ctx context.Context, duration time.Duration) {
	addrs := clusterAddrsFromEnv()
	rdb, rc := newClusterClientWithRedirectCounter(addrs)
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		fmt.Fprintln(os.Stderr, "ping failed:", err)
		os.Exit(1)
	}

	res := writeLoop(ctx, rdb, "cluster-failover-writes", "fo", duration, 150*time.Millisecond, 300*time.Millisecond)
	fmt.Printf("cluster-failover-writes: MOVED=%d ASK=%d\n", atomic.LoadInt64(&rc.moved), atomic.LoadInt64(&rc.ask))

	// Намеренно НЕ используем «0 сбоев/подвисаний» как признак «kill не
	// попал в окно» и не завершаемся с ошибкой на этом основании: живые
	// прогоны этого сценария показали, что go-redis ClusterClient иногда
	// поглощает целый failover без единого видимого эффекта на стороне
	// клиента (см. комментарий у writeLoop) — это тоже реальный,
	// воспроизводимый результат, а не признак сломанной ячейки. То, что
	// kill реально произошёл и промоушен реально случился, проверяется
	// СНАРУЖИ (ops/topology-demo.sh, через exit-код docker kill и
	// `cluster nodes`/`INFO replication` на выживших узлах) — независимо
	// от того, что увидел (или не увидел) этот клиент.
	if res.failed == 0 && res.stalled == 0 {
		fmt.Println("cluster-failover-writes: НАБЛЮДЕНИЕ — за весь прогон не было ни одной ошибки записи и ни одного аномально долгого вызова (клиент не заметил failover); реальность самого kill/промоушена проверяется отдельно снаружи")
	} else if res.recoveredAt.IsZero() {
		fmt.Println("cluster-failover-writes: НАБЛЮДЕНИЕ — деградация зафиксирована, но восстановления в пределах duration не случилось (либо промоушен не произошёл в отведённое время, либо duration слишком мал)")
	} else {
		fmt.Printf("cluster-failover-writes: окно недоступности (клиентски наблюдаемое) = %s, неудачных попыток за окно=%d, аномально долгих (но успешных) вызовов=%d, максимальная задержка одного вызова=%s\n",
			res.recoveredAt.Sub(res.firstFailAt), res.failed, res.stalled, res.maxLatency)
	}
}

// --- сценарий 3: Sentinel failover ---

func sentinelFailover(ctx context.Context, duration time.Duration) {
	sentinelAddrs := sentinelAddrsFromEnv()
	master := sentinelMasterFromEnv()

	rdb := redis.NewFailoverClient(&redis.FailoverOptions{
		MasterName:    master,
		SentinelAddrs: sentinelAddrs,
	})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		fmt.Fprintln(os.Stderr, "ping failed:", err)
		os.Exit(1)
	}

	// Отдельный «сырой» клиент к одному Sentinel — только для
	// SENTINEL get-master-addr-by-name, независимо от FailoverClient.
	sentinelRaw := redis.NewSentinelClient(&redis.Options{Addr: sentinelAddrs[0]})
	defer sentinelRaw.Close()

	initialAddr, err := sentinelRaw.GetMasterAddrByName(ctx, master).Result()
	if err != nil {
		fmt.Fprintln(os.Stderr, "SENTINEL get-master-addr-by-name (начальный опрос):", err)
		os.Exit(1)
	}
	fmt.Printf("sentinel-failover: начальный мастер по данным %s: %v\n", sentinelAddrs[0], initialAddr)

	var wg sync.WaitGroup
	var res writeLoopResult
	var promotionDetectedAt time.Time
	var promotedAddr []string
	start := time.Now()

	wg.Add(1)
	go func() {
		defer wg.Done()
		res = writeLoop(ctx, rdb, "sentinel-failover", "sfo", duration, 150*time.Millisecond, 300*time.Millisecond)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		deadline := start.Add(duration)
		last := initialAddr
		for time.Now().Before(deadline) {
			addr, err := sentinelRaw.GetMasterAddrByName(ctx, master).Result()
			if err == nil && (len(addr) != len(last) || addr[0] != last[0] || addr[1] != last[1]) {
				promotionDetectedAt = time.Now()
				promotedAddr = addr
				fmt.Printf("sentinel-failover: SENTINEL get-master-addr-by-name сменил адрес на T+%s: %v -> %v\n",
					promotionDetectedAt.Sub(start), last, addr)
				last = addr
			}
			time.Sleep(300 * time.Millisecond)
		}
	}()

	wg.Wait()

	noClientImpact := res.failed == 0 && res.stalled == 0
	if noClientImpact {
		fmt.Println("sentinel-failover: НАБЛЮДЕНИЕ — за весь прогон не было ни одной ошибки записи и ни одного аномально долгого вызова (клиент не заметил failover); Sentinel-промоушен проверяется отдельно ниже, независимо от этого")
	} else if res.recoveredAt.IsZero() {
		fmt.Fprintln(os.Stderr, "sentinel-failover: деградация была, но клиент не восстановился в пределах duration — ячейка недействительна, нужен повтор с большим duration")
		os.Exit(1)
	}
	if promotionDetectedAt.IsZero() {
		fmt.Fprintln(os.Stderr, "sentinel-failover: SENTINEL get-master-addr-by-name ни разу не поменял адрес за duration — промоушен через Sentinel не зафиксирован опросом (возможно, duration мал или опрос не совпал по времени), ячейка недействительна")
		os.Exit(1)
	}

	fmt.Printf("sentinel-failover: promoted master по SENTINEL: %v, обнаружено на T+%s\n", promotedAddr, promotionDetectedAt.Sub(start))
	if !noClientImpact {
		fmt.Printf("sentinel-failover: окно недоступности клиента (FailoverClient) = %s (первая деградация T+%s, восстановление T+%s), неудачных попыток=%d, аномально долгих (но успешных) вызовов=%d, максимальная задержка одного вызова=%s\n",
			res.recoveredAt.Sub(res.firstFailAt), res.firstFailAt.Sub(start), res.recoveredAt.Sub(start), res.failed, res.stalled, res.maxLatency)
		fmt.Printf("sentinel-failover: разница между обнаружением через SENTINEL и восстановлением записи клиента = %s\n",
			res.recoveredAt.Sub(promotionDetectedAt))
	}
}

// --- сценарий 4: изолированный писатель для демонстрации split-brain ---

func splitBrainWriter(ctx context.Context, duration time.Duration) {
	addr := addrFromEnv()
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	fmt.Printf("=== split-brain-writer: пишу splitbrain:marker:<i> в %s в течение %s (БЕЗ Sentinel, прямое подключение) ===\n", addr, duration)
	start := time.Now()
	deadline := start.Add(duration)
	var i int
	var success, failed int
	for time.Now().Before(deadline) {
		key := fmt.Sprintf("splitbrain:marker:%d", i)
		val := time.Now().Format(time.RFC3339Nano)
		err := rdb.Set(ctx, key, val, 0).Err()
		if err != nil {
			failed++
			fmt.Printf("T+%s SET %s: ошибка: %v\n", time.Since(start), key, err)
		} else {
			success++
			fmt.Printf("T+%s SET %s: ok\n", time.Since(start), key)
		}
		i++
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Printf("split-brain-writer: итог — success=%d failed=%d из %d попыток, последний индекс=%d\n", success, failed, i, i-1)
}

func main() {
	scenario := flag.String("scenario", "", "cluster-writes-during-reshard | cluster-failover-writes | sentinel-failover | split-brain-writer")
	n := flag.Int("n", 20000, "cluster-writes-during-reshard: сколько ключей записать")
	duration := flag.Duration("duration", 45*time.Second, "cluster-failover-writes/sentinel-failover/split-brain-writer: длительность цикла записи")
	flag.Parse()

	ctx := context.Background()

	switch *scenario {
	case "cluster-writes-during-reshard":
		clusterWritesDuringReshard(ctx, *n)
	case "cluster-failover-writes":
		clusterFailoverWrites(ctx, *duration)
	case "sentinel-failover":
		sentinelFailover(ctx, *duration)
	case "split-brain-writer":
		splitBrainWriter(ctx, *duration)
	default:
		fmt.Fprintln(os.Stderr, "unknown -scenario, expected: cluster-writes-during-reshard | cluster-failover-writes | sentinel-failover | split-brain-writer")
		os.Exit(1)
	}
}
