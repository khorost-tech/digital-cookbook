// Стенд etcd: координация, не прикладные данные. Четыре сценария —
// watch (подписка на изменения с ревизиями, переживает разрыв через
// WithRev), lease (TTL-жизнь ключа + keepalive), lock (distributed mutex,
// N=10 горутин под concurrency.Mutex) и revisions (MVCC-история + Compact
// + Defragment).
//
// ГРАНИЦЫ ЗАДАЧИ: здесь etcd показывается в штатной роли координатора
// небольшого состояния. Поведение etcd под несвойственной нагрузкой —
// сценарий 3 бенчмарка (Task 10), сюда не тащить.
//
// Датасет/PostgreSQL этому стенду не нужны — etcd здесь не хранит каталог
// товаров, только служебные ключи демонстраций (см. brief).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func connectEtcd(endpoints string) *clientv3.Client {
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   strings.Split(endpoints, ","),
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("clientv3.New(%q): %v", endpoints, err)
	}
	return cli
}

func main() {
	var (
		endpoints = flag.String("etcd-endpoints", envOr("ETCD_ENDPOINTS", "127.0.0.1:2379"), "etcd endpoints, comma-separated")
		scenario  = flag.String("scenario", "", "watch|lease|lock|revisions")
		lockRuns  = flag.Int("lock-runs", 10, "число независимых прогонов сценария lock (урок Task 2: гонка вскрылась только на 12 прогонах)")
	)
	flag.Parse()

	cli := connectEtcd(*endpoints)
	defer cli.Close()

	ctx := context.Background()

	switch *scenario {
	case "watch":
		runWatch(ctx, cli)
	case "lease":
		runLease(ctx, cli)
	case "lock":
		runLock(ctx, cli, *endpoints, *lockRuns)
	case "revisions":
		runRevisions(ctx, cli, *endpoints)
	default:
		log.Fatalf("неизвестный -scenario=%q, ожидался watch|lease|lock|revisions", *scenario)
	}
}

// ============================== watch ==============================
//
// Демонстрирует: (1) все изменения обязаны прийти в watch — потеря события
// ломает всю модель использования etcd для координации; (2) ревизии в
// потоке событий строго возрастают; (3) watch переживает разрыв —
// пересоздание с WithRev(lastSeen+1) досматривает историю без потерь,
// как если бы клиент переподключился после сетевого сбоя.

type watchEvent struct {
	rev int64
	key string
	typ string
}

func collectWatchEvents(ctx context.Context, wch clientv3.WatchChan, want int, timeout time.Duration) []watchEvent {
	var events []watchEvent
	deadline := time.After(timeout)
	for len(events) < want {
		select {
		case wresp, ok := <-wch:
			if !ok {
				log.Fatalf("АССЕРТ watch: канал закрылся раньше, чем получено %d событий (получено %d)", want, len(events))
			}
			if err := wresp.Err(); err != nil {
				log.Fatalf("watch ошибка: %v", err)
			}
			for _, ev := range wresp.Events {
				events = append(events, watchEvent{
					rev: ev.Kv.ModRevision,
					key: string(ev.Kv.Key),
					typ: ev.Type.String(),
				})
			}
		case <-deadline:
			log.Fatalf("АССЕРТ watch: таймаут %s, получено %d из %d событий", timeout, len(events), want)
		}
	}
	return events
}

func runWatch(ctx context.Context, cli *clientv3.Client) {
	const prefix = "/inmemory/watch-demo/"
	const changesBeforeGap = 12
	const changesAfterGap = 8
	changesMade := changesBeforeGap + changesAfterGap

	// Чистый префикс — идемпотентность повторного прогона.
	if _, err := cli.Delete(ctx, prefix, clientv3.WithPrefix()); err != nil {
		log.Fatalf("очистка префикса перед watch: %v", err)
	}

	// Фаза 1: watch "живьём" с текущего момента, без WithRev.
	watchCtx1, cancel1 := context.WithCancel(ctx)
	wch1 := cli.Watch(watchCtx1, prefix, clientv3.WithPrefix())

	for i := 0; i < changesBeforeGap; i++ {
		key := fmt.Sprintf("%skey-%02d", prefix, i)
		if _, err := cli.Put(ctx, key, fmt.Sprintf("v%d", i)); err != nil {
			log.Fatalf("put %s: %v", key, err)
		}
	}

	events1 := collectWatchEvents(ctx, wch1, changesBeforeGap, 10*time.Second)
	lastRev1 := events1[len(events1)-1].rev
	cancel1() // симулируем разрыв подписки — watcher больше не слушает

	// Пока watch1 остановлен, продолжаем менять ключи — эти изменения
	// НЕ увидены никаким живым watch-каналом в момент своего появления.
	for i := changesBeforeGap; i < changesMade; i++ {
		key := fmt.Sprintf("%skey-%02d", prefix, i)
		if _, err := cli.Put(ctx, key, fmt.Sprintf("v%d", i)); err != nil {
			log.Fatalf("put %s: %v", key, err)
		}
	}

	// Фаза 2: пересоздаём watch с WithRev(lastRev1+1) — досмотр истории
	// с прошлой ревизии, как после переподключения после сбоя.
	watchCtx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	wch2 := cli.Watch(watchCtx2, prefix, clientv3.WithPrefix(), clientv3.WithRev(lastRev1+1))
	events2 := collectWatchEvents(ctx, wch2, changesAfterGap, 10*time.Second)

	all := append(append([]watchEvent{}, events1...), events2...)

	// АССЕРТ: все N изменений обязаны прийти в watch (по обеим фазам).
	if len(all) != changesMade {
		log.Fatalf("АССЕРТ: изменений %d, событий watch %d — потеря", changesMade, len(all))
	}

	// АССЕРТ: ревизии строго возрастают через весь склеенный поток,
	// включая границу между "живой" фазой и "досмотром через WithRev".
	for i := 1; i < len(all); i++ {
		if all[i].rev <= all[i-1].rev {
			log.Fatalf("АССЕРТ: ревизии не возрастают: %d после %d (индекс %d)", all[i].rev, all[i-1].rev, i)
		}
	}

	fmt.Printf("watch: фаза1 (живой канал) событий=%d, последняя увиденная ревизия=%d\n", len(events1), lastRev1)
	fmt.Printf("watch: разрыв подписки, %d изменений сделано МИМО живого канала\n", changesAfterGap)
	fmt.Printf("watch: фаза2 (WithRev=%d, досмотр истории) событий=%d\n", lastRev1+1, len(events2))
	fmt.Printf("watch: итого событий=%d, изменений сделано=%d — потерь нет\n", len(all), changesMade)
	fmt.Printf("watch: ревизии по всему потоку строго возрастают: %d -> %d\n", all[0].rev, all[len(all)-1].rev)
	fmt.Println("watch: OK")
}

// ============================== lease ==============================
//
// Демонстрирует: ключ обязан исчезнуть после истечения lease (без
// keepalive), и отдельно — keepalive продлевает жизнь ключа за пределы
// исходного TTL. TTL здесь в секундах — одно из немногих мест стенда, где
// абсолютное время осмысленно (платформенный шум в десятки мс на секундный
// TTL не влияет). Публикуется факт исчезновения ключа, не миллисекунды.

func runLease(ctx context.Context, cli *clientv3.Client) {
	const ttlSeconds = 3
	const graceSeconds = 2 // запас сверх TTL перед проверкой — не абсолютная метрика, а буфер надёжности

	// ---- часть A: lease без keepalive истекает, ключ исчезает ----
	keyA := "/inmemory/lease-demo/no-keepalive"
	leaseA, err := cli.Grant(ctx, ttlSeconds)
	if err != nil {
		log.Fatalf("Grant (A): %v", err)
	}
	if _, err := cli.Put(ctx, keyA, "alive", clientv3.WithLease(leaseA.ID)); err != nil {
		log.Fatalf("Put (A) с lease: %v", err)
	}

	getResp, err := cli.Get(ctx, keyA)
	if err != nil {
		log.Fatalf("Get (A) сразу после Put: %v", err)
	}
	existsImmediately := len(getResp.Kvs) == 1
	fmt.Printf("lease: ключ %s сразу после Put с lease TTL=%ds — существует=%v\n", keyA, ttlSeconds, existsImmediately)

	fmt.Printf("lease: ждём TTL+запас (%ds) без keepalive...\n", ttlSeconds+graceSeconds)
	time.Sleep(time.Duration(ttlSeconds+graceSeconds) * time.Second)

	getResp, err = cli.Get(ctx, keyA)
	if err != nil {
		log.Fatalf("Get (A) после истечения TTL: %v", err)
	}
	existsAfterExpiry := len(getResp.Kvs) != 0

	// АССЕРТ: ключ обязан исчезнуть после истечения lease.
	if existsAfterExpiry {
		log.Fatalf("АССЕРТ: ключ пережил истечение lease — TTL не работает")
	}
	fmt.Printf("lease: ключ %s после истечения TTL — существует=%v (ожидалось false)\n", keyA, existsAfterExpiry)

	// ---- часть B: тот же TTL, но с keepalive — ключ живёт дольше TTL ----
	keyB := "/inmemory/lease-demo/with-keepalive"
	leaseB, err := cli.Grant(ctx, ttlSeconds)
	if err != nil {
		log.Fatalf("Grant (B): %v", err)
	}
	if _, err := cli.Put(ctx, keyB, "alive", clientv3.WithLease(leaseB.ID)); err != nil {
		log.Fatalf("Put (B) с lease: %v", err)
	}

	kaCtx, kaCancel := context.WithCancel(ctx)
	kaCh, err := cli.KeepAlive(kaCtx, leaseB.ID)
	if err != nil {
		log.Fatalf("KeepAlive (B): %v", err)
	}
	kaResponses := int64(0)
	var kaWG sync.WaitGroup
	kaWG.Add(1)
	go func() {
		defer kaWG.Done()
		for range kaCh {
			atomic.AddInt64(&kaResponses, 1)
		}
	}()

	waitBeyondTTL := time.Duration(ttlSeconds+graceSeconds) * time.Second
	fmt.Printf("lease: держим keepalive %s (больше исходного TTL=%ds)...\n", waitBeyondTTL, ttlSeconds)
	time.Sleep(waitBeyondTTL)

	getResp, err = cli.Get(ctx, keyB)
	if err != nil {
		log.Fatalf("Get (B) во время keepalive: %v", err)
	}
	survivedPastTTL := len(getResp.Kvs) == 1
	if !survivedPastTTL {
		log.Fatalf("АССЕРТ: ключ с активным keepalive исчез раньше срока — keepalive не работает")
	}
	fmt.Printf("lease: ключ %s пережил исходный TTL=%ds под keepalive — существует=%v, откликов keepalive получено=%d\n",
		keyB, ttlSeconds, survivedPastTTL, atomic.LoadInt64(&kaResponses))

	// Останавливаем keepalive и убеждаемся, что ключ теперь тоже истекает.
	kaCancel()
	kaWG.Wait()
	fmt.Printf("lease: keepalive остановлен, ждём TTL+запас (%ds) ещё раз...\n", ttlSeconds+graceSeconds)
	time.Sleep(time.Duration(ttlSeconds+graceSeconds) * time.Second)

	getResp, err = cli.Get(ctx, keyB)
	if err != nil {
		log.Fatalf("Get (B) после остановки keepalive: %v", err)
	}
	existsAfterKeepaliveStopped := len(getResp.Kvs) != 0
	if existsAfterKeepaliveStopped {
		log.Fatalf("АССЕРТ: ключ пережил истечение lease после остановки keepalive")
	}
	fmt.Printf("lease: после остановки keepalive ключ %s тоже исчезает — существует=%v (ожидалось false)\n",
		keyB, existsAfterKeepaliveStopped)

	fmt.Println("lease: OK")
}

// ============================== lock ==============================
//
// N=10 горутин инкрементируют общий счётчик под concurrency.Mutex. Урок
// Task 2 этой же серии: ассерт "counter == N" может пройти по случайности,
// если блокировка на самом деле не держит, но гонка не проявилась в
// единственном прогоне. Поэтому: (а) не менее 10 независимых прогонов,
// каждый на СВОЁМ ключе-префиксе (никакого переиспользования состояния
// между прогонами) и (б) отдельный счётчик maxConcurrent, который ловит
// сломанную блокировку даже тогда, когда итоговый counter случайно сошёлся.

const lockN = 10

func runLockOnce(ctx context.Context, cli *clientv3.Client, runID int) (counter int32, maxConcurrent int32) {
	prefix := fmt.Sprintf("/inmemory/lock-demo/run-%03d", runID)

	var (
		wg          sync.WaitGroup
		inCritical  int32
		counterVal  int32
		maxSeen     int32
		maxSeenLock sync.Mutex
	)

	wg.Add(lockN)
	for i := 0; i < lockN; i++ {
		go func(workerID int) {
			defer wg.Done()

			sess, err := concurrency.NewSession(cli)
			if err != nil {
				log.Fatalf("run %d worker %d: NewSession: %v", runID, workerID, err)
			}
			defer sess.Close()

			mu := concurrency.NewMutex(sess, prefix)
			if err := mu.Lock(ctx); err != nil {
				log.Fatalf("run %d worker %d: Lock: %v", runID, workerID, err)
			}

			// ---- критическая секция ----
			cur := atomic.AddInt32(&inCritical, 1)
			maxSeenLock.Lock()
			if cur > maxSeen {
				maxSeen = cur
			}
			maxSeenLock.Unlock()

			counterVal++ // намеренно НЕ atomic — инкремент защищён (по гипотезе) самим mutex'ом
			time.Sleep(5 * time.Millisecond) // расширяет окно гонки, если блокировка не держит

			atomic.AddInt32(&inCritical, -1)
			// ---- конец критической секции ----

			if err := mu.Unlock(ctx); err != nil {
				log.Fatalf("run %d worker %d: Unlock: %v", runID, workerID, err)
			}
		}(i)
	}
	wg.Wait()

	return counterVal, maxSeen
}

func runLock(ctx context.Context, cli *clientv3.Client, endpoint string, runs int) {
	type result struct {
		counter       int32
		maxConcurrent int32
	}
	results := make([]result, 0, runs)

	for r := 1; r <= runs; r++ {
		c, m := runLockOnce(ctx, cli, r)
		results = append(results, result{c, m})
		fmt.Printf("lock: прогон %d/%d — counter=%d maxConcurrent=%d\n", r, runs, c, m)

		// АССЕРТ (на каждом прогоне, не только в среднем): взаимное
		// исключение обязано держаться на КАЖДОМ прогоне, а не "в среднем".
		if c != lockN {
			log.Fatalf("АССЕРТ: счётчик %d, ожидался %d — взаимное исключение нарушено (прогон %d/%d)", c, lockN, r, runs)
		}
		if m != 1 {
			log.Fatalf("АССЕРТ: одновременно в критической секции было %d (прогон %d/%d)", m, r, runs)
		}
	}

	fmt.Printf("lock: %d/%d прогонов дали counter=%d, maxConcurrent=1\n", runs, runs, lockN)
	fmt.Println("lock: OK")
}

// ============================== revisions ==============================
//
// MVCC-история и компакция. Перезаписываем ключ N раз, показываем, что
// старые ревизии доступны до компакции и недоступны после, снимаем размер
// базы (endpoint status: DbSize — физический файл, DbSizeInUse — логически
// занято) до/после Compact и до/после Defragment. DbSizeInUse падает сразу
// после Compact (свободное место помечено, но файл не усечён); DbSize
// (физический размер файла на диске) падает только после Defragment —
// поэтому обе метрики публикуются раздельно, а не одна вместо другой.

func statusStable(ctx context.Context, cli *clientv3.Client, endpoint string, label string) (dbSize, dbSizeInUse int64) {
	const settleReads = 3
	const pollInterval = 300 * time.Millisecond
	const budget = 10 * time.Second

	deadline := time.Now().Add(budget)
	var lastSize, lastInUse int64 = -1, -1
	stableCount := 0

	for {
		st, err := cli.Status(ctx, endpoint)
		if err != nil {
			log.Fatalf("Status(%s) [%s]: %v", endpoint, label, err)
		}
		if st.DbSize == lastSize && st.DbSizeInUse == lastInUse {
			stableCount++
		} else {
			stableCount = 1
		}
		lastSize, lastInUse = st.DbSize, st.DbSizeInUse

		if stableCount >= settleReads {
			return lastSize, lastInUse
		}
		if time.Now().After(deadline) {
			fmt.Printf("revisions: ПРЕДУПРЕЖДЕНИЕ — [%s] размер базы не стабилизировался за %s (последнее: dbSize=%d dbSizeInUse=%d)\n",
				label, budget, lastSize, lastInUse)
			return lastSize, lastInUse
		}
		time.Sleep(pollInterval)
	}
}

func runRevisions(ctx context.Context, cli *clientv3.Client, endpoint string) {
	const key = "/inmemory/mvcc-demo/counter"
	const overwrites = 2000 // достаточно, чтобы дать заметную дельту dbSize/dbSizeInUse

	// Идемпотентность повторного прогона: чистим ключ (создаёт tombstone-
	// ревизию, не мешает демонстрации — история всё равно растёт дальше).
	if _, err := cli.Delete(ctx, key); err != nil {
		log.Fatalf("очистка ключа перед revisions: %v", err)
	}

	dbSizeBefore, dbSizeInUseBefore := statusStable(ctx, cli, endpoint, "до перезаписей")
	fmt.Printf("revisions: до перезаписей — dbSize=%d dbSizeInUse=%d\n", dbSizeBefore, dbSizeInUseBefore)

	var revisions []int64
	// Значение достаточно большое, чтобы overwrites реально нагрузили MVCC-лог,
	// а не потерялись в шуме исходного размера базы.
	payload := strings.Repeat("x", 256)
	for i := 0; i < overwrites; i++ {
		resp, err := cli.Put(ctx, key, fmt.Sprintf("%s-%d", payload, i))
		if err != nil {
			log.Fatalf("Put #%d: %v", i, err)
		}
		revisions = append(revisions, resp.Header.Revision)
	}
	latestRev := revisions[len(revisions)-1]

	dbSizeAfterWrites, dbSizeInUseAfterWrites := statusStable(ctx, cli, endpoint, "после перезаписей, до компакции")
	fmt.Printf("revisions: после %d перезаписей, ДО компакции — dbSize=%d dbSizeInUse=%d\n", overwrites, dbSizeAfterWrites, dbSizeInUseAfterWrites)

	// История доступна: старая ревизия читается по WithRev.
	midIdx := len(revisions) / 2
	oldRev := revisions[midIdx]
	oldGet, err := cli.Get(ctx, key, clientv3.WithRev(oldRev))
	if err != nil {
		log.Fatalf("Get по старой ревизии %d (до компакции): %v", oldRev, err)
	}
	if len(oldGet.Kvs) != 1 {
		log.Fatalf("АССЕРТ: старая ревизия %d недоступна ДО компакции — история MVCC не работает", oldRev)
	}
	expectedOldVal := fmt.Sprintf("%s-%d", payload, midIdx)
	if string(oldGet.Kvs[0].Value) != expectedOldVal {
		log.Fatalf("АССЕРТ: значение по старой ревизии %d не совпадает: got=%q want=%q", oldRev, oldGet.Kvs[0].Value, expectedOldVal)
	}
	fmt.Printf("revisions: старая ревизия %d читается ДО компакции, значение совпадает\n", oldRev)

	// Компакция до latestRev-1 (оставляем последнюю ревизию доступной как "текущую").
	compactTo := latestRev - 1
	if _, err := cli.Compact(ctx, compactTo); err != nil {
		log.Fatalf("Compact(%d): %v", compactTo, err)
	}
	fmt.Printf("revisions: Compact выполнен до ревизии %d\n", compactTo)

	dbSizeAfterCompact, dbSizeInUseAfterCompact := statusStable(ctx, cli, endpoint, "после компакции, до defrag")
	fmt.Printf("revisions: после Compact, ДО Defragment — dbSize=%d dbSizeInUse=%d\n", dbSizeAfterCompact, dbSizeInUseAfterCompact)

	// АССЕРТ: старая ревизия, ушедшая под компакцию, больше недоступна.
	_, err = cli.Get(ctx, key, clientv3.WithRev(oldRev))
	if err == nil {
		log.Fatalf("АССЕРТ: старая ревизия %d осталась доступна ПОСЛЕ компакции до %d — компакция не сработала", oldRev, compactTo)
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "compacted") {
		log.Fatalf("АССЕРТ: ожидалась ошибка 'compacted' по старой ревизии %d, получено: %v", oldRev, err)
	}
	fmt.Printf("revisions: старая ревизия %d ПОСЛЕ компакции недоступна, как и ожидалось: %v\n", oldRev, err)

	// Defragment — физически сжимает файл базы на диске.
	if _, err := cli.Defragment(ctx, endpoint); err != nil {
		log.Fatalf("Defragment(%s): %v", endpoint, err)
	}
	fmt.Println("revisions: Defragment выполнен")

	dbSizeAfterDefrag, dbSizeInUseAfterDefrag := statusStable(ctx, cli, endpoint, "после Defragment")
	fmt.Printf("revisions: после Defragment — dbSize=%d dbSizeInUse=%d\n", dbSizeAfterDefrag, dbSizeInUseAfterDefrag)

	fmt.Println("=== сводка revisions ===")
	fmt.Printf("dbSize:      до=%d  после Compact=%d  после Defragment=%d\n", dbSizeBefore, dbSizeAfterCompact, dbSizeAfterDefrag)
	fmt.Printf("dbSizeInUse: до=%d  после Compact=%d  после Defragment=%d\n", dbSizeInUseBefore, dbSizeInUseAfterCompact, dbSizeInUseAfterDefrag)
	if dbSizeAfterDefrag < dbSizeAfterWrites {
		fmt.Printf("revisions: физический размер базы сократился после Defragment: %d -> %d (-%d байт, -%.1f%%)\n",
			dbSizeAfterWrites, dbSizeAfterDefrag, dbSizeAfterWrites-dbSizeAfterDefrag,
			100*float64(dbSizeAfterWrites-dbSizeAfterDefrag)/float64(dbSizeAfterWrites))
	} else {
		fmt.Printf("revisions: ПРЕДУПРЕЖДЕНИЕ — физический размер базы НЕ сократился после Defragment (%d -> %d); возможно, свободное место уже было переиспользовано под новые ревизии до компакции\n",
			dbSizeAfterWrites, dbSizeAfterDefrag)
	}

	fmt.Println("revisions: OK")
}
