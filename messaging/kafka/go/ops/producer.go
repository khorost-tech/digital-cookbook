package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// pad — тот же паттерн, что в ../storage/producer.go: значение фиксированного
// размера (нужно для честного throughput/сжатия — реальный размер записи
// управляем мы, а не длина случайного мусора).
func pad(base string, targetBytes int, filler byte) string {
	if len(base) >= targetBytes {
		return base
	}
	return base + strings.Repeat(string(filler), targetBytes-len(base))
}

// newSeedProducer — простой acks=all клиент без явной тонкой настройки
// batch/linger/compression (эти параметры — предмет tuning-producer, а не
// seed/seed-continuous, которые лишь готовят данные для lag/tuning-consumer
// демонстраций).
func newSeedProducer(seeds []string) *kgo.Client {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(seeds...),
		kgo.RequiredAcks(kgo.AllISRAcks()),
	)
	if err != nil {
		log.Fatalf("kgo.NewClient (seed producer): %v", err)
	}
	return cl
}

// seedFast — n записей асинхронно (Produce + один Flush в конце), максимально
// быстро. Используется, чтобы заранее наполнить топик данными для
// tuning-consumer (там тюнингуется КОНСЬЮМЕР, а не продюсер, поэтому сам
// сид должен быть быстрым и не влиять на измерения).
func seedFast(cl *kgo.Client, topic string, n int, valueBytes int, prefix string) {
	var acked, failed atomic.Int64
	start := time.Now()
	for i := 0; i < n; i++ {
		value := pad(fmt.Sprintf("%s-%08d-", prefix, i), valueBytes, 'v')
		rec := &kgo.Record{Topic: topic, Value: []byte(value)}
		cl.Produce(context.Background(), rec, func(_ *kgo.Record, err error) {
			if err != nil {
				failed.Add(1)
			} else {
				acked.Add(1)
			}
		})
	}
	if err := cl.Flush(context.Background()); err != nil {
		log.Fatalf("seedFast: Flush: %v", err)
	}
	elapsed := time.Since(start)
	fmt.Printf("[seed] topic=%s: отправлено %d записей (~%d байт значение) acked=%d failed=%d за %s\n",
		topic, n, valueBytes, acked.Load(), failed.Load(), elapsed)
}

// seedContinuous — фоновый продюсер для lag-демо: шлёт записи с целевым
// темпом ratePerSec в течение duration, затем останавливается. Печатает
// прогресс каждые ~2с, чтобы оркестрирующий bash-скрипт видел живой процесс
// и мог синхронизировать запуск lag-consume/поллинг kafka-consumer-groups.sh
// с фактическим стартом продюсинга, а не вслепую по sleep.
func seedContinuous(cl *kgo.Client, topic string, duration time.Duration, ratePerSec int, valueBytes int) {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}
	interval := time.Second / time.Duration(ratePerSec)
	var sent atomic.Int64
	deadline := time.Now().Add(duration)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	progress := time.NewTicker(2 * time.Second)
	defer progress.Stop()
	i := 0
	fmt.Printf("[seed-continuous] СТАРТ topic=%s rate=%d/s duration=%s (~%d записей ожидается)\n", topic, ratePerSec, duration, int(duration.Seconds())*ratePerSec)
	for time.Now().Before(deadline) {
		select {
		case <-ticker.C:
			value := pad(fmt.Sprintf("lag-%08d-", i), valueBytes, 'l')
			i++
			rec := &kgo.Record{Topic: topic, Value: []byte(value)}
			cl.Produce(context.Background(), rec, func(_ *kgo.Record, err error) {
				if err == nil {
					sent.Add(1)
				} else {
					log.Printf("[seed-continuous] produce error: %v", err)
				}
			})
		case <-progress.C:
			fmt.Printf("[seed-continuous] прогресс: отправлено=%d\n", sent.Load())
		}
	}
	if err := cl.Flush(context.Background()); err != nil {
		log.Printf("[seed-continuous] Flush: %v", err)
	}
	fmt.Printf("[seed-continuous] ЗАВЕРШЕНО: всего отправлено %d записей за %s (целевой темп %d/s)\n", sent.Load(), duration, ratePerSec)
}

// newTuningProducer — клиент с явно управляемыми batch.size (ProducerBatchMaxBytes)
// / linger.ms (ProducerLinger) / compression.type (ProducerBatchCompression) —
// это и есть предмет демонстрации данного сценария, поэтому все три параметра
// приходят снаружи, а не из дефолтов библиотеки.
func newTuningProducer(seeds []string, batchBytes int32, linger time.Duration, compression kgo.CompressionCodec, clientID string) *kgo.Client {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(seeds...),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerBatchMaxBytes(batchBytes),
		kgo.ProducerLinger(linger),
		kgo.ProducerBatchCompression(compression),
		kgo.ClientID(clientID),
	)
	if err != nil {
		log.Fatalf("kgo.NewClient (tuning producer): %v", err)
	}
	return cl
}

// runTuningProducer — n записей АСИНХРОННО (не ProduceSync!) — синхронная
// отправка "запись за записью" вырождает batch.size/linger.ms в бессмысленные
// параметры (каждый батч был бы размера 1, см. тот же довод в
// ../storage/producer.go про produceBatchedAsync и компрессию). Здесь
// нарочно то же самое: чтобы batch/linger реально повлияли на throughput,
// клиенту нужно дать накопить настоящие батчи.
func runTuningProducer(cl *kgo.Client, topic string, n int, valueBytes int, label string) {
	var acked, failed atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		value := pad(fmt.Sprintf("tune-%08d-", i), valueBytes, 't')
		rec := &kgo.Record{Topic: topic, Value: []byte(value)}
		wg.Add(1)
		cl.Produce(context.Background(), rec, func(_ *kgo.Record, err error) {
			defer wg.Done()
			if err == nil {
				acked.Add(1)
			} else {
				failed.Add(1)
			}
		})
	}
	wg.Wait()
	elapsed := time.Since(start)
	throughput := float64(n) / elapsed.Seconds()
	mbps := throughput * float64(valueBytes) / (1024 * 1024)
	fmt.Printf("[tuning-producer] %s n=%d value-bytes=%d -> acked=%d/%d failed=%d elapsed=%s throughput=%.1f msg/s (%.2f MB/s)\n",
		label, n, valueBytes, acked.Load(), n, failed.Load(), elapsed, throughput, mbps)
}

// quotaProduce — продюсит НЕПРЕРЫВНО (без искусственного rate limit) в течение
// duration под заданным client-id — именно client-id используется Kafka для
// сопоставления с quota-конфигом (entity-type=clients, entity-name=<client-id>).
// Измеряет ДОСТИГНУТЫЙ throughput за окно — до применения quota он упирается
// в реальную пропускную способность (сеть/диск), после применения — в
// producer_byte_rate, который выставляет ops-скрипт МЕЖДУ двумя прогонами.
func quotaProduce(seeds []string, topic, clientID string, duration time.Duration, valueBytes int) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(seeds...),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ClientID(clientID),
	)
	if err != nil {
		log.Fatalf("kgo.NewClient (quota producer): %v", err)
	}
	defer cl.Close()

	var acked, failed atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	deadline := start.Add(duration)
	i := 0
	for time.Now().Before(deadline) {
		value := pad(fmt.Sprintf("quota-%08d-", i), valueBytes, 'q')
		i++
		rec := &kgo.Record{Topic: topic, Value: []byte(value)}
		wg.Add(1)
		cl.Produce(context.Background(), rec, func(_ *kgo.Record, err error) {
			defer wg.Done()
			if err == nil {
				acked.Add(1)
			} else {
				failed.Add(1)
			}
		})
	}
	wg.Wait()
	elapsed := time.Since(start)
	throughput := float64(acked.Load()) / elapsed.Seconds()
	mbps := throughput * float64(valueBytes) / (1024 * 1024)
	fmt.Printf("[quota-produce] client-id=%s duration=%s -> acked=%d failed=%d throughput=%.1f msg/s (%.2f MB/s)\n",
		clientID, elapsed, acked.Load(), failed.Load(), throughput, mbps)
}

func codecFromString(s string) kgo.CompressionCodec {
	switch s {
	case "none":
		return kgo.NoCompression()
	case "gzip":
		return kgo.GzipCompression()
	case "lz4":
		return kgo.Lz4Compression()
	case "zstd":
		return kgo.ZstdCompression()
	case "snappy":
		return kgo.SnappyCompression()
	default:
		log.Fatalf("неизвестный -compression=%q (none|gzip|lz4|zstd|snappy)", s)
		return kgo.NoCompression()
	}
}
