// Command materialized-views — стенд #4 серии "ClickHouse: глубокое
// погружение": материализованные представления + real-time агрегации +
// живой Kafka->CH пайплайн.
//
//   - MV как ТРИГГЕР на вставку: demo.mv_events (MergeTree) -> MV ->
//     demo.mv_events_agg (AggregatingMergeTree, -State-функции по
//     (country, toStartOfHour(event_time))). Явно проверено: MV не
//     обрабатывает данные, вставленные ДО его создания.
//   - SummingMergeTree (простое суммирование), projections (альтернативный
//     ORDER BY внутри таблицы, EXPLAIN indexes=1 + force_optimize_projection
//     подтверждают использование), TTL на партициях.
//   - Kafka -> ClickHouse: apache/kafka:4.3.1 (KRaft single-node,
//     ../compose/kafka.yml) -> ENGINE=Kafka -> MV -> MergeTree -> MV ->
//     AggregatingMergeTree, franz-go продюсер (ограниченный объём, опрос
//     ClickHouse с таймаутом — НЕ бесконечный цикл).
//
// Запуск (из контейнера golang:1.25, сеть clickhouse-cookbook-net, см.
// ../ops/matviews-demo.sh за полный live-сценарий):
//
//	docker run --rm --network clickhouse-cookbook-net \
//	  -v "$(pwd)/materialized-views:/app" -v "$(pwd)/dataset/out:/data" -w /app golang:1.25 \
//	  go run . -phase=all -csv=/data/events-matviews.csv -expect-rows=1000000 \
//	  -ch-addr=clickhouse:9000 -kafka-brokers=kafka:9092
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func main() {
	phase := flag.String("phase", "all", "agg|summing|projection|ttl|kafka-schema|kafka-produce|kafka-verify|kafka|all")
	chAddr := flag.String("ch-addr", "clickhouse:9000", "ClickHouse native addr")
	csvPath := flag.String("csv", "/data/events-matviews.csv", "путь к CSV-датасету (dataset/main.go)")
	preMVRows := flag.Int("pre-mv-rows", 1000, "число строк, вставляемых ДО создания MV (Step 1: демонстрация 'MV не триггерится на существующие данные')")
	bulkBatch := flag.Int("bulk-batch", 100_000, "размер батча основной загрузки после создания MV")
	expectRows := flag.Int64("expect-rows", 0, "если >0, ассерт: число строк основной загрузки == это значение")

	kafkaBrokers := flag.String("kafka-brokers", "kafka:9092", "comma-separated Kafka bootstrap servers (compose-net internal listener)")
	kafkaBrokerListForCH := flag.String("kafka-broker-list-ch", "kafka:9092", "kafka_broker_list для ENGINE=Kafka (обычно совпадает с -kafka-brokers)")
	kafkaEvents := flag.Int("kafka-events", 50_000, "число событий, отправляемых франц-go продюсером (ограниченный объём, Step 3 брифа)")
	kafkaSeed := flag.Int64("kafka-seed", 7, "seed генератора синтетических Kafka-событий")
	kafkaTimeout := flag.Duration("kafka-timeout", 60*time.Second, "таймаут опроса ClickHouse до совпадения count() с отправленным (НЕ бесконечный цикл)")

	flag.Parse()

	ctx := context.Background()
	ch, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{*chAddr},
		Auth:        clickhouse.Auth{Database: "demo", Username: "default"},
		DialTimeout: 10 * time.Second,
		Settings:    clickhouse.Settings{"max_execution_time": 180},
	})
	if err != nil {
		log.Fatalf("ch open: %v", err)
	}
	defer ch.Close()
	if err := ch.Ping(ctx); err != nil {
		log.Fatalf("ch ping: %v", err)
	}

	switch *phase {
	case "agg":
		phaseAgg(ctx, ch, *csvPath, *preMVRows, *bulkBatch, *expectRows)
	case "summing":
		phaseSumming(ctx, ch)
	case "projection":
		phaseProjection(ctx, ch)
	case "ttl":
		phaseTTL(ctx, ch)
	case "kafka-schema":
		phaseKafkaSchema(ctx, ch, *kafkaBrokerListForCH, *kafkaBrokers)
	case "kafka-produce":
		phaseKafkaProduce(*kafkaBrokers, *kafkaEvents, *kafkaSeed)
	case "kafka-verify":
		phaseKafkaVerify(ctx, ch, *kafkaEvents, *kafkaTimeout)
	case "kafka":
		phaseKafkaSchema(ctx, ch, *kafkaBrokerListForCH, *kafkaBrokers)
		sent := phaseKafkaProduce(*kafkaBrokers, *kafkaEvents, *kafkaSeed)
		phaseKafkaVerify(ctx, ch, sent, *kafkaTimeout)
	case "all":
		phaseAgg(ctx, ch, *csvPath, *preMVRows, *bulkBatch, *expectRows)
		phaseSumming(ctx, ch)
		phaseProjection(ctx, ch)
		phaseTTL(ctx, ch)
		phaseKafkaSchema(ctx, ch, *kafkaBrokerListForCH, *kafkaBrokers)
		sent := phaseKafkaProduce(*kafkaBrokers, *kafkaEvents, *kafkaSeed)
		phaseKafkaVerify(ctx, ch, sent, *kafkaTimeout)
		fmt.Println("\n[materialized-views] all phases completed, all asserts passed")
	default:
		fmt.Fprintf(os.Stderr, "unknown -phase=%s\n", *phase)
		os.Exit(2)
	}
}

func mustRun(f func() error) {
	if err := f(); err != nil {
		log.Fatalf("%v", err)
	}
}
