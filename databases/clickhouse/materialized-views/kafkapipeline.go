package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

const (
	kafkaTopic = "mv-events-stream"
	kafkaGroup = "ch-mv-events-consumer"
)

// phaseKafkaSchema — Step 3 брифа: (пере)создаёт топик, ЗАТЕМ объекты
// Kafka->CH пайплайна. Топик пересоздаётся ПЕРВЫМ, ДО того, как ClickHouse
// ENGINE=Kafka подключит консюмер — если бы порядок был обратным (топик
// удаляется/пересоздаётся ПОСЛЕ того, как консюмер уже подписан), уже
// подключённый консюмер оказался бы подписан на удалённый topic-id, что
// потребовало бы отдельного цикла обновления метаданных librdkafka внутри
// ClickHouse. Цепочка: demo.mv_events_queue (ENGINE=Kafka, "вьюпорт" на
// топик, без хранения) -> demo.mv_events_queue_mv (MV-триггер) ->
// demo.mv_events_kafka_raw (MergeTree, реальное хранение) ->
// demo.mv_events_kafka_agg_mv (MV-триггер, тот же паттерн, что Step 1) ->
// demo.mv_events_kafka_agg (AggregatingMergeTree, живой предагрегат).
func phaseKafkaSchema(ctx context.Context, ch clickhouse.Conn, brokerList, kafkaSeeds string) {
	fmt.Println("\n=== KAFKA -> CLICKHOUSE ПАЙПЛАЙН: схема (Step 3 брифа) ===")

	seeds := strings.Split(kafkaSeeds, ",")
	if err := ensureKafkaTopic(seeds, kafkaTopic, 3, 1); err != nil {
		log.Fatalf("ensure kafka topic: %v", err)
	}
	fmt.Printf("[kafka] топик %s (пере)создан (partitions=3, rf=1 — single-node KRaft), ДО подключения ClickHouse-консюмера\n", kafkaTopic)

	mustRun(func() error { return createRawTable(ctx, ch, tblKafkaRaw) })
	mustRun(func() error { return createAggTable(ctx, ch, tblKafkaAgg) })
	mustRun(func() error { return createAggMV(ctx, ch, mvKafkaRawToAgg, tblKafkaAgg, tblKafkaRaw) })
	mustRun(func() error { return createKafkaQueueTable(ctx, ch, tblKafkaQueue, brokerList, kafkaTopic, kafkaGroup) })
	mustRun(func() error { return createKafkaMV(ctx, ch, mvKafkaToRaw, tblKafkaRaw, tblKafkaQueue) })

	fmt.Printf("[kafka] demo.%s (ENGINE=Kafka, broker_list=%s, topic=%s) -> demo.%s (MV) -> demo.%s (MergeTree) -> demo.%s (MV) -> demo.%s (AggregatingMergeTree)\n",
		tblKafkaQueue, brokerList, kafkaTopic, mvKafkaToRaw, tblKafkaRaw, mvKafkaRawToAgg, tblKafkaAgg)
}

// phaseKafkaProduce — franz-go продюсер шлёт n синтетических событий
// (ОГРАНИЧЕННЫЙ объём — брифа Step 3: "напр. 50 000", НЕ бесконечный поток).
// Топик уже создан в phaseKafkaSchema (ДО подключения консюмера) — здесь
// только отправка, без повторного удаления/пересоздания топика.
func phaseKafkaProduce(kafkaSeeds string, n int, seed int64) int {
	fmt.Println("\n=== KAFKA -> CLICKHOUSE ПАЙПЛАЙН: продюсер (Step 3 брифа) ===")
	seeds := strings.Split(kafkaSeeds, ",")

	sent, elapsed, err := produceEvents(seeds, kafkaTopic, n, seed)
	if err != nil {
		log.Fatalf("produce events: %v", err)
	}
	fmt.Printf("[kafka] продюсер (franz-go): отправлено %d/%d сообщений за %s (%.0f msg/s)\n", sent, n, elapsed, float64(sent)/elapsed.Seconds())
	assertFailFast(sent == n, "все сообщения отправлены успешно продюсером: sent=%d == requested=%d", sent, n)
	return sent
}

// phaseKafkaVerify — опрос demo.mv_events_kafka_raw ДО ТЕХ ПОР, пока count()
// не достигнет sent, С ТАЙМАУТОМ (Step 3 брифа: "НЕ бесконечный цикл"). После
// достижения (либо честного таймаута) сверяет предагрегат
// demo.mv_events_kafka_agg с прямым пересчётом по demo.mv_events_kafka_raw —
// тот же compareAggVsRecompute, что Step 1, теперь на данных, пришедших
// через Kafka, а не батч-загрузкой из CSV.
func phaseKafkaVerify(ctx context.Context, ch clickhouse.Conn, sent int, timeout time.Duration) {
	fmt.Println("\n=== KAFKA -> CLICKHOUSE ПАЙПЛАЙН: сверка (Step 3 брифа) ===")

	deadline := time.Now().Add(timeout)
	const pollInterval = 500 * time.Millisecond
	var lastCount uint64
	pollN := 0
	start := time.Now()
	for {
		n, err := countRows(ctx, ch, tblKafkaRaw)
		if err != nil {
			log.Fatalf("poll count demo.%s: %v", tblKafkaRaw, err)
		}
		lastCount = n
		pollN++
		if n >= uint64(sent) {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(pollInterval)
	}
	waited := time.Since(start)
	fmt.Printf("[kafka] опрос demo.%s (интервал %s, таймаут %s): %d rows видно после %s (%d опросов), отправлено продюсером=%d\n",
		tblKafkaRaw, pollInterval, timeout, lastCount, waited, pollN, sent)

	assertFailFast(lastCount == uint64(sent), "Kafka-пайплайн: обработано в CH (%d) == отправлено продюсером (%d) в пределах таймаута %s", lastCount, sent, timeout)

	// state-строки схлопываются в одну на ключ — OPTIMIZE FINAL перед
	// сверкой (тот же приём, что phaseAgg для demo.mv_events_agg).
	if err := ch.Exec(ctx, fmt.Sprintf("OPTIMIZE TABLE demo.%s FINAL", tblKafkaAgg)); err != nil {
		log.Fatalf("optimize final %s: %v", tblKafkaAgg, err)
	}
	compareAggVsRecompute(ctx, ch, tblKafkaAgg, tblKafkaRaw, "Kafka-пайплайн")
}
