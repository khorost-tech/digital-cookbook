// Command ops — стенд #6 серии "Kafka: глубокое погружение": эксплуатация —
// consumer lag, тюнинг producer/consumer, quotas. Клиентская (Go/Java) часть
// этого стенда сознательно ограничена тем, что требует поведения БИБЛИОТЕКИ
// клиента (lag/тюнинг/квоты — это client-side эффекты). Ручная/автоматическая
// перебалансировка партиций, rack-awareness и KRaft-кворум — это операции над
// БРОКЕРОМ/кластером, не над клиентом, и демонстрируются напрямую через
// kafka-*.sh CLI внутри контейнеров брокеров (см. ../../ops/reassign-demo.sh,
// ../../ops/rack-quorum-demo.sh) — так же, как в предыдущих стендах host-скрипт
// делал docker kill/inspect segments напрямую, а не через клиентскую программу.
//
// Запуск отдельного сценария (пример):
//
//	docker run --rm --network kafka-cookbook-net -v "$(pwd)/go:/app" -w /app golang:1.25 \
//	  go run ./ops -scenario=seed -topic=demo-ops-lag -n=2000
//
// См. ../../ops/lag-demo.sh за полный оркестрированный прогон и ../../README.md
// за реальный вывод.
package main

import (
	"flag"
	"log"
	"strconv"
	"strings"
	"time"
)

func main() {
	brokers := flag.String("brokers", "kafka1:9092,kafka2:9092,kafka3:9092", "comma-separated bootstrap servers")
	scenario := flag.String("scenario", "", "seed|seed-continuous|lag-consume|tuning-producer|tuning-consumer|quota-produce")
	topic := flag.String("topic", "demo-ops", "имя топика")
	group := flag.String("group", "ops-group", "consumer group id")
	partitions := flag.Int("partitions", 3, "число партиций (при -recreate)")
	rf := flag.Int("rf", 3, "replication factor (при -recreate)")
	minIsr := flag.String("minisr", "2", "min.insync.replicas (при -recreate); пусто = не задавать")
	recreate := flag.Bool("recreate", false, "пересоздать топик перед запуском сценария")
	n := flag.Int("n", 1000, "число записей (seed/tuning-producer)")
	valueBytes := flag.Int("value-bytes", 300, "размер значения записи в байтах")
	prefix := flag.String("prefix", "seed", "префикс значения (seed)")
	rate := flag.Int("rate", 20, "целевой темп записей/сек (seed-continuous)")
	duration := flag.Duration("duration", 30*time.Second, "длительность (seed-continuous/quota-produce)")
	slowCount := flag.Int("slow-count", 0, "число первых записей, обрабатываемых с задержкой (lag-consume)")
	slowDelay := flag.Duration("slow-delay", 0, "задержка на запись в 'медленной' фазе (lag-consume)")
	runFor := flag.Duration("run-for", 60*time.Second, "сколько всего работать (lag-consume)")
	idle := flag.Duration("idle", 8*time.Second, "idle-таймаут без новых записей -> завершение (lag-consume/tuning-consumer)")
	clientID := flag.String("client-id", "ops-client", "kgo.ClientID — для tuning-producer(лог)/quota-produce(сопоставление с quota на брокере)")
	batchBytes := flag.Int("batch-bytes", 16384, "batch.size (ProducerBatchMaxBytes), байт (tuning-producer)")
	lingerMs := flag.Int("linger-ms", 0, "linger.ms (tuning-producer)")
	compression := flag.String("compression", "none", "compression.type: none|gzip|lz4|zstd|snappy (tuning-producer)")
	fetchMinBytes := flag.Int("fetch-min-bytes", 1, "fetch.min.bytes (tuning-consumer)")
	fetchMaxWaitMs := flag.Int("fetch-max-wait-ms", 500, "fetch.max.wait.ms (tuning-consumer)")
	maxPollRecords := flag.Int("max-poll-records", 500, "эмуляция max.poll.records через PollRecords (tuning-consumer)")
	label := flag.String("label", "", "метка для вывода (какая комбинация параметров тестируется)")
	flag.Parse()

	seeds := strings.Split(*brokers, ",")

	if *recreate {
		configs := map[string]*string{}
		if *minIsr != "" {
			configs["min.insync.replicas"] = strPtr(*minIsr)
		}
		recreateTopic(seeds, *topic, int32(*partitions), int16(*rf), configs)
		waitTopicReady(seeds, *topic, *partitions, 30*time.Second)
	}

	switch *scenario {
	case "seed":
		cl := newSeedProducer(seeds)
		defer cl.Close()
		seedFast(cl, *topic, *n, *valueBytes, *prefix)

	case "seed-continuous":
		cl := newSeedProducer(seeds)
		defer cl.Close()
		seedContinuous(cl, *topic, *duration, *rate, *valueBytes)

	case "lag-consume":
		lagConsume(seeds, *topic, *group, *slowCount, *slowDelay, *runFor, *idle)

	case "tuning-producer":
		cl := newTuningProducer(seeds, int32(*batchBytes), time.Duration(*lingerMs)*time.Millisecond, codecFromString(*compression), *clientID)
		defer cl.Close()
		lbl := *label
		if lbl == "" {
			lbl = flagsLabel(*batchBytes, *lingerMs, *compression)
		}
		runTuningProducer(cl, *topic, *n, *valueBytes, lbl)

	case "tuning-consumer":
		lbl := *label
		if lbl == "" {
			lbl = flagsLabelConsumer(*fetchMinBytes, *fetchMaxWaitMs, *maxPollRecords)
		}
		tuningConsume(seeds, *topic, *group, int32(*fetchMinBytes), time.Duration(*fetchMaxWaitMs)*time.Millisecond, *maxPollRecords, *idle, lbl)

	case "quota-produce":
		quotaProduce(seeds, *topic, *clientID, *duration, *valueBytes)

	default:
		log.Fatalf("неизвестный -scenario=%q (seed|seed-continuous|lag-consume|tuning-producer|tuning-consumer|quota-produce)", *scenario)
	}
}

func flagsLabel(batchBytes, lingerMs int, compression string) string {
	return "batch-bytes=" + itoa(batchBytes) + " linger-ms=" + itoa(lingerMs) + " compression=" + compression
}

func flagsLabelConsumer(fetchMinBytes, fetchMaxWaitMs, maxPollRecords int) string {
	return "fetch-min-bytes=" + itoa(fetchMinBytes) + " fetch-max-wait-ms=" + itoa(fetchMaxWaitMs) + " max-poll-records=" + itoa(maxPollRecords)
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
