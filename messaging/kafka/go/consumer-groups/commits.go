package main

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	commitDemoMessages     = 20
	commitDemoProcessDelay = 400 * time.Millisecond
)

// scenarioCommits показывает разницу между auto-commit (риск at-most-once:
// офсет коммитится по таймеру/позиции чтения независимо от того, доделана
// ли фактическая обработка записи) и ручным аналогом commitSync
// (CommitRecords ПОСЛЕ обработки — at-least-once: при сбое до коммита
// запись будет передоставлена повторно, а не потеряна).
func scenarioCommits() {
	autoCommitAtMostOnceDemo()
	manualCommitAtLeastOnceDemo()
}

// autoCommitAtMostOnceDemo моделирует реалистичный баг auto-commit:
// consumer раздаёт полученные записи в фоновые "воркеры" (имитация
// асинхронной обработки — например, отправка в очередь/микросервис) и
// СРАЗУ возвращается за следующей пачкой, не дожидаясь их завершения.
// Позиция чтения (а с ней и auto-commit) уходит вперёд быстрее, чем
// реально завершается обработка. Если процесс "падает" до того как
// воркеры доделали работу, а офсет уже автокоммитнут — эти записи
// потеряны безвозвратно (при рестарте consumer начнёт после них).
func autoCommitAtMostOnceDemo() {
	groupID := "cg-commit-auto"
	fmt.Println("\n--- commit: auto-commit (риск at-most-once) ---")

	var dispatched atomic.Int64
	var trulyProcessed atomic.Int64
	var crashed atomic.Bool

	process := func(m *member, fetches kgo.Fetches) {
		fetches.EachRecord(func(r *kgo.Record) {
			dispatched.Add(1)
			off, val := r.Offset, string(r.Value)
			go func() {
				time.Sleep(commitDemoProcessDelay) // "бизнес-логика" в фоне, poll-цикл дальше НЕ ждёт
				if crashed.Load() {
					logf("[auto] воркер доделал offset=%d value=%s ПОСЛЕ 'краша' — результат потерян (в реальности процесс уже мёртв)", off, val)
					return
				}
				n := trulyProcessed.Add(1)
				logf("[auto] воркер доделал offset=%d value=%s (итого доделано=%d)", off, val, n)
			}()
		})
	}

	c1 := newMember("auto-1", groupID, process, kgo.AutoCommitInterval(100*time.Millisecond))
	c1.waitFirstAssign(15 * time.Second)
	time.Sleep(300 * time.Millisecond)

	producer, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		log.Fatalf("producer: %v", err)
	}
	sent := 0
	for i := 0; i < commitDemoMessages; i++ {
		rec := &kgo.Record{Topic: topic, Key: []byte(fmt.Sprintf("part-%d", i%partitions)), Value: []byte(fmt.Sprintf("auto-msg-%d", i))}
		if _, err := producer.ProduceSync(context.Background(), rec).First(); err != nil {
			log.Fatalf("produce: %v", err)
		}
		sent++
	}
	producer.Close()
	logf("[auto] отправлено %d сообщений", sent)

	deadline := time.Now().Add(5 * time.Second)
	for dispatched.Load() < int64(sent) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if dispatched.Load() < int64(sent) {
		log.Fatalf("[assert] FAIL (auto-commit): консьюмер не забрал все %d сообщений (забрал %d)", sent, dispatched.Load())
	}
	// автокоммиту (интервал 100мс) хватит времени тикнуть и закоммитить
	// позицию чтения; воркерам (спят 400мс) — НЕ хватит.
	time.Sleep(250 * time.Millisecond)

	crashed.Store(true)
	c1.close() // "краш": просто останавливаем поллинг, руками ничего не коммитим

	logf("[auto] 'краш': раздиспатчено=%d, реально доделано ДО краша=%d", dispatched.Load(), trulyProcessed.Load())

	// новый консьюмер той же группы продолжит с закоммиченной (уже ушедшей
	// вперёд) позиции — то, что было раздиспатчено, но не доделано, он
	// просто НЕ увидит.
	var resumed atomic.Int64
	resumeProcess := func(m *member, fetches kgo.Fetches) {
		fetches.EachRecord(func(r *kgo.Record) {
			resumed.Add(1)
			logf("[auto] (новый консьюмер, resume) offset=%d value=%s", r.Offset, string(r.Value))
		})
	}
	c2 := newMember("auto-2", groupID, resumeProcess)
	c2.waitFirstAssign(15 * time.Second)
	time.Sleep(1500 * time.Millisecond)
	c2.close()

	// дать зависшим воркерам добежать — только чтобы честно залогировать
	// их (уже помеченный потерянным) результат.
	time.Sleep(commitDemoProcessDelay + 200*time.Millisecond)

	seenTotal := trulyProcessed.Load() + resumed.Load()
	logf("[auto] итог: отправлено=%d, реально доделано до краша=%d, дочитано новым консьюмером=%d, суммарно=%d",
		sent, trulyProcessed.Load(), resumed.Load(), seenTotal)

	if seenTotal >= int64(sent) {
		log.Fatalf("[assert] FAIL (auto-commit): ожидалась потеря сообщений (auto-commit обогнал фоновую обработку), но seenTotal=%d >= sent=%d — гонка не проявилась в этом прогоне (тайминги host-зависимы)", seenTotal, sent)
	}
	logf("[assert] OK (auto-commit): потеря продемонстрирована — отправлено=%d, суммарно доделано+дочитано=%d (< %d) => at-most-once", sent, seenTotal, sent)
}

// manualCommitAtLeastOnceDemo: (1) сравнивает латентность commitSync-аналога
// (CommitRecords) вызванного per-record vs одним батчем — content-note про
// то, что per-record коммит медленнее; (2) показывает, что при отключённом
// autocommit и коммите ПОСЛЕ обработки, крах ДО коммита ведёт к повторной
// доставке уже обработанных записей (дубли), но НЕ к потере.
func manualCommitAtLeastOnceDemo() {
	fmt.Println("\n--- commit: ручной commitSync-аналог (at-least-once) ---")
	measureCommitLatency()
	crashBeforeCommitDemo()
}

func measureCommitLatency() {
	const n = 10
	groupID := "cg-commit-latency"

	// ВАЖНО: здесь НЕ используем newMember — у него свой фоновый pollLoop,
	// который конкурировал бы за PollFetches с ручным опросом ниже (записи
	// доставались бы то одному, то другому вызывающему, и часть терялась
	// бы для нашего замера). Для чистого измерения латентности коммита
	// нужен один-единственный, полностью ручной consumer.
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(seeds...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumerGroup(groupID),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtEnd()),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		log.Fatalf("[commit-latency] kgo.NewClient: %v", err)
	}
	defer cl.Close()

	// первый пустой пул устанавливает членство в группе и назначение партиций
	warmupCtx, warmupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	cl.PollFetches(warmupCtx)
	warmupCancel()
	time.Sleep(500 * time.Millisecond)

	producer, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		log.Fatalf("producer: %v", err)
	}
	defer producer.Close()
	for i := 0; i < n; i++ {
		rec := &kgo.Record{Topic: topic, Key: []byte(fmt.Sprintf("part-%d", i%partitions)), Value: []byte(fmt.Sprintf("latency-%d", i))}
		if _, err := producer.ProduceSync(context.Background(), rec).First(); err != nil {
			log.Fatalf("produce (latency): %v", err)
		}
	}

	var recs []*kgo.Record
	deadline := time.Now().Add(15 * time.Second)
	for len(recs) < n && time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		fetches := cl.PollFetches(ctx)
		cancel()
		fetches.EachRecord(func(r *kgo.Record) { recs = append(recs, r) })
	}
	if len(recs) < n {
		log.Fatalf("[commit-latency] не дождались %d сообщений (получено %d)", n, len(recs))
	}

	start := time.Now()
	for _, r := range recs {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := cl.CommitRecords(ctx, r)
		cancel()
		if err != nil {
			log.Fatalf("[commit-latency] per-record commit: %v", err)
		}
	}
	perRecord := time.Since(start)

	start = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = cl.CommitRecords(ctx, recs...)
	cancel()
	if err != nil {
		log.Fatalf("[commit-latency] batch commit: %v", err)
	}
	batch := time.Since(start)

	logf("[commit-latency] %d коммитов по одной записи: %s (%.1f мс/коммит) vs 1 батч-коммит на %d записей: %s",
		n, perRecord, float64(perRecord.Microseconds())/1000.0/float64(n), n, batch)
	logf("[commit-latency] абсолютные мс — host-зависимая величина (сеть/диск брокера); важен ФАКТ (N round-trip'ов вместо одного), не точные цифры")
}

func crashBeforeCommitDemo() {
	groupID := "cg-commit-manual"

	producer, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		log.Fatalf("producer: %v", err)
	}

	var processedRun1 int64
	// Ключ — VALUE записи (уникальные "manual-msg-N"), а НЕ Kafka-offset:
	// топик demo-groups общий для всех сценариев и 6 партиций имеют
	// независимые, несовпадающие последовательности offset'ов, так что
	// offset НЕ идентифицирует конкретное сообщение однозначно между
	// прогонами/сценариями — value идентифицирует.
	processedValues := map[string]bool{}
	crashAfterRun1 := 12
	crashed := false

	process := func(m *member, fetches kgo.Fetches) {
		var batchRecs []*kgo.Record
		fetches.EachRecord(func(r *kgo.Record) {
			if crashed {
				return
			}
			time.Sleep(commitDemoProcessDelay)
			processedRun1++
			processedValues[string(r.Value)] = true
			batchRecs = append(batchRecs, r)
			logf("[manual] обработано offset=%d value=%s (итого в run1=%d)", r.Offset, string(r.Value), processedRun1)
			if int(processedRun1) == crashAfterRun1 {
				crashed = true
			}
		})
		if len(batchRecs) == 0 {
			return
		}
		if !crashed {
			// commitSync-аналог: коммитим ПОСЛЕ обработки всей пачки, не до.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := m.cl.CommitRecords(ctx, batchRecs...); err != nil {
				log.Printf("[manual] commit error: %v", err)
			}
			cancel()
		} else {
			logf("[manual] 'краш' — пачка из %d записей обработана, но НЕ закоммичена (коммит не успел)", len(batchRecs))
		}
	}

	c1 := newMember("manual-1", groupID, process, kgo.DisableAutoCommit())
	c1.waitFirstAssign(15 * time.Second)
	time.Sleep(300 * time.Millisecond)

	sent := 0
	for i := 0; i < commitDemoMessages; i++ {
		rec := &kgo.Record{Topic: topic, Key: []byte(fmt.Sprintf("part-%d", i%partitions)), Value: []byte(fmt.Sprintf("manual-msg-%d", i))}
		if _, err := producer.ProduceSync(context.Background(), rec).First(); err != nil {
			log.Fatalf("produce: %v", err)
		}
		sent++
	}
	producer.Close()
	logf("[manual] отправлено %d сообщений", sent)

	deadline := time.Now().Add(time.Duration(crashAfterRun1+2) * commitDemoProcessDelay * 2)
	for !crashed && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !crashed {
		log.Fatalf("[assert] FAIL (manual-commit): не дождались обработки %d записей до 'краша'", crashAfterRun1)
	}
	time.Sleep(200 * time.Millisecond)
	c1.close()

	logf("[manual] 'краш' после %d обработанных записей (закоммичено раньше, батчами, без последней незакоммиченной пачки)", processedRun1)

	// новый консьюмер той же группы: должен ПЕРЕЧИТАТЬ незакоммиченный
	// хвост (в т.ч. часть уже обработанных в run1, но не закоммиченных).
	resumedValues := map[string]bool{}
	resumeProcess := func(m *member, fetches kgo.Fetches) {
		fetches.EachRecord(func(r *kgo.Record) {
			val := string(r.Value)
			resumedValues[val] = true
			dup := ""
			if processedValues[val] {
				dup = " (ДУБЛЬ — уже обрабатывался в run1 до краша, но не был закоммичен)"
			}
			logf("[manual] (новый консьюмер, resume) offset=%d value=%s%s", r.Offset, val, dup)
		})
	}
	c2 := newMember("manual-2", groupID, resumeProcess, kgo.DisableAutoCommit())
	c2.waitFirstAssign(15 * time.Second)
	time.Sleep(2 * time.Second)
	c2.close()

	// ассерт: НИ ОДНО отправленное сообщение (по value, "manual-msg-0".."manual-msg-{sent-1}")
	// не потеряно — присутствует хотя бы в одном из двух прогонов (могут
	// быть дубли, но не пропуски).
	missing := []string{}
	dupCount := 0
	for i := 0; i < sent; i++ {
		val := fmt.Sprintf("manual-msg-%d", i)
		inRun1 := processedValues[val]
		inRun2 := resumedValues[val]
		if !inRun1 && !inRun2 {
			missing = append(missing, val)
		}
		if inRun1 && inRun2 {
			dupCount++
		}
	}
	if len(missing) > 0 {
		log.Fatalf("[assert] FAIL (manual-commit): потеряны сообщения %v — at-least-once нарушен", missing)
	}
	logf("[assert] OK (manual-commit): все %d сообщений покрыты (run1=%d, run2=%d, дублей=%d) — потерь нет, at-least-once подтверждён",
		sent, len(processedValues), len(resumedValues), dupCount)
}
