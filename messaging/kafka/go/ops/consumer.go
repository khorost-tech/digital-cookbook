package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// newGroupConsumer — общий клиент consumer group для lag-consume/tuning-consumer.
// Автокоммит явно выключен в обоих сценариях: lag-consume коммитит вручную
// ПОСЛЕ обработки каждой записи (чтобы LAG-колонка kafka-consumer-groups.sh
// --describe отражала реальный прогресс обработки, а не позицию чтения),
// tuning-consumer вообще не коммитит (заново читает с начала при каждом
// прогоне -recreate=true).
func newGroupConsumer(seeds []string, topic, group string, extraOpts ...kgo.Opt) *kgo.Client {
	opts := []kgo.Opt{
		kgo.SeedBrokers(seeds...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumerGroup(group),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
	}
	opts = append(opts, extraOpts...)
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		log.Fatalf("kgo.NewClient (group consumer %s): %v", group, err)
	}
	return cl
}

// lagConsume — "медленный" консьюмер группы для демонстрации consumer lag:
// первые slowCount записей обрабатываются с искусственной задержкой slowDelay
// (имитация тяжёлой обработки — пока продюсер (см. seedContinuous, запущен
// ПАРАЛЛЕЛЬНО host-скриптом) продолжает писать, лаг растёт), затем консьюмер
// переключается в быстрый режим и вычитывает накопленный backlog — лаг падает
// к нулю. Коммит — синхронно, СРАЗУ после обработки каждой записи (не пачкой),
// чтобы committed-offset, который видит kafka-consumer-groups.sh --describe,
// как можно точнее отражал реальный прогресс "обработки" в каждый момент
// времени, который поллит host-скрипт.
func lagConsume(seeds []string, topic, group string, slowCount int, slowDelay time.Duration, runFor time.Duration, idle time.Duration) {
	cl := newGroupConsumer(seeds, topic, group)
	defer cl.Close()

	processed := 0
	start := time.Now()
	deadline := start.Add(runFor)
	lastProgress := time.Now()
	lastLog := time.Now()

	for time.Now().Before(deadline) {
		pollCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		fetches := cl.PollFetches(pollCtx)
		cancel()
		for _, e := range fetches.Errors() {
			if e.Err != nil && e.Err != context.DeadlineExceeded {
				fmt.Printf("[lag-consume] fetch error topic=%s partition=%d: %v\n", e.Topic, e.Partition, e.Err)
			}
		}
		n := 0
		fetches.EachRecord(func(r *kgo.Record) {
			n++
			slow := processed < slowCount
			if slow {
				time.Sleep(slowDelay)
			}
			processed++
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := cl.CommitRecords(ctx, r); err != nil {
				log.Printf("[lag-consume] CommitRecords: %v", err)
			}
			cancel()
		})
		if n > 0 {
			lastProgress = time.Now()
		} else if time.Since(lastProgress) > idle {
			fmt.Printf("[lag-consume] idle %s без новых записей — завершаю раньше run-for\n", idle)
			break
		}
		if time.Since(lastLog) > 2*time.Second {
			mode := "МЕДЛЕННЫЙ"
			if processed >= slowCount {
				mode = "быстрый (drain)"
			}
			fmt.Printf("[lag-consume] прогресс: обработано=%d режим=%s\n", processed, mode)
			lastLog = time.Now()
		}
	}
	fmt.Printf("[lag-consume] ЗАВЕРШЕНО: обработано=%d за %s (slow-count=%d slow-delay=%s)\n", processed, time.Since(start), slowCount, slowDelay)
}

// tuningConsume — измеряет throughput/размер пачки при заданных fetch.min.bytes
// / fetch.max.wait.ms (франз-го ConsumerOpt) и эмуляции max.poll.records через
// PollRecords(ctx, maxPollRecords) — франз-го не имеет прямого аналога Java
// max.poll.records как ClientOpt, но PollRecords ограничивает число записей,
// возвращаемых ОДНИМ вызовом Poll, тем же способом, каким Java консьюмер
// ограничивает размер одной пачки poll().
func tuningConsume(seeds []string, topic, group string, fetchMinBytes int32, fetchMaxWait time.Duration, maxPollRecords int, idle time.Duration, label string) {
	cl := newGroupConsumer(seeds, topic, group,
		kgo.FetchMinBytes(fetchMinBytes),
		kgo.FetchMaxWait(fetchMaxWait),
	)
	defer cl.Close()

	var total, polls, nonEmptyPolls int
	lastProgress := time.Now()
	var firstRecordAt, lastRecordAt time.Time
	overall, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	for {
		if time.Since(lastProgress) > idle {
			break
		}
		pollCtx, pollCancel := context.WithTimeout(overall, 2*time.Second)
		fetches := cl.PollRecords(pollCtx, maxPollRecords)
		pollCancel()
		if overall.Err() != nil {
			break
		}
		n := 0
		fetches.EachRecord(func(_ *kgo.Record) { n++ })
		polls++
		if n > 0 {
			now := time.Now()
			if firstRecordAt.IsZero() {
				firstRecordAt = now
			}
			lastRecordAt = now
			total += n
			nonEmptyPolls++
			lastProgress = now
		}
	}
	// elapsed — окно РЕАЛЬНОЙ передачи данных (от первой до последней непустой
	// записи), а НЕ полное время цикла: полный цикл включает idle-хвост
	// (ожидание нового трафика перед выходом), который может быть на порядок
	// длиннее самой передачи и искусственно занижает throughput, если его не
	// вычесть — поймано живьём при первом прогоне: elapsed совпадал с idle
	// (~10-11с) независимо от объёма данных, throughput выходил заведомо
	// заниженным и одинаковым для всех комбинаций параметров.
	elapsed := lastRecordAt.Sub(firstRecordAt)
	if elapsed <= 0 {
		elapsed = time.Millisecond // избегаем деления на 0 при total==0/одном poll
	}
	avgPerPoll := 0.0
	if nonEmptyPolls > 0 {
		avgPerPoll = float64(total) / float64(nonEmptyPolls)
	}
	throughput := float64(total) / elapsed.Seconds()
	fmt.Printf("[tuning-consumer] %s -> total=%d непустых-poll=%d avg-records/poll=%.1f elapsed(данные)=%s throughput=%.1f msg/s\n",
		label, total, nonEmptyPolls, avgPerPoll, elapsed, throughput)
}
