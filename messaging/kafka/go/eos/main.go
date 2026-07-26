// Command eos — стенд #5 серии "Kafka: глубокое погружение": exactly-once и
// транзакции (EOS).
//
// Как и replication/storage, часть сценариев (consume-process-produce с
// "убийством" процесса в середине транзакции) требует, чтобы ХОСТ реально
// прибил процесс между двумя вызовами клиента — оркестрирует
// ../../ops/eos-kill.sh (docker run -d + docker logs -f для ожидания
// маркера + docker kill контейнера).
//
// Запуск отдельной фазы (пример):
//
//	docker run --rm --network kafka-cookbook-net -v "$(pwd)/go:/app" -w /app golang:1.25 \
//	  go run ./eos -scenario=txn-run
//
// См. ../../ops/eos-kill.sh за полный сценарий consume-process-produce и
// ../../README.md за реальный прогон.
package main

import (
	"flag"
	"log"
	"strings"
)

func main() {
	brokers := flag.String("brokers", "kafka1:9092,kafka2:9092,kafka3:9092", "comma-separated bootstrap servers")
	scenario := flag.String("scenario", "", "txn-setup|txn-run|txn-verify|cpp-setup|cpp-seed|cpp-attempt|cpp-verify|cpp-inspect")
	topic := flag.String("topic", "demo-eos-txn", "топик (txn-*)")
	inputTopic := flag.String("input-topic", "demo-eos-cpp-input", "input-топик (cpp-*)")
	outputTopic := flag.String("output-topic", "demo-eos-cpp-output", "output-топик (cpp-*)")
	group := flag.String("group", "eos-cpp-group", "consumer group (cpp-*)")
	txnID := flag.String("txn-id", "cookbook-eos-cpp-producer", "TransactionalID (cpp-attempt)")
	batchSize := flag.Int("batch-size", 5, "размер батча (txn-run)")
	n := flag.Int("n", 10, "число записей (cpp-seed/cpp-attempt)")
	prefix := flag.String("prefix", "cpp", "префикс значения записи (cpp-seed)")
	pause := flag.Duration("pause", 0, "пауза после produce, ДО commit (cpp-attempt; 0 = commit сразу)")
	readyMarker := flag.String("ready-marker", "READY-TO-COMMIT", "строка-маркер, которую host-скрипт ищет в логах перед docker kill (cpp-attempt)")
	partitions := flag.Int("partitions", 3, "число партиций (txn-setup/cpp-setup)")
	rf := flag.Int("rf", 3, "replication factor (txn-setup/cpp-setup)")
	expectCommitted := flag.Int("expect-committed", 0, "ожидаемый read_committed count (txn-verify)")
	expectPhysical := flag.Int("expect-physical", 0, "ожидаемый read_uncommitted (физический) count (txn-verify)")
	label := flag.String("label", "", "метка снимка состояния (cpp-verify)")
	expectOutput := flag.Int64("expect-output-committed", 0, "ожидаемый output read_committed count (cpp-verify)")
	expectGroupOffset := flag.Int64("expect-group-offset", 0, "ожидаемый committed-offset группы на input (cpp-verify)")
	flag.Parse()

	seeds := strings.Split(*brokers, ",")

	switch *scenario {
	case "txn-setup":
		recreateTopic(seeds, *topic, int32(*partitions), int16(*rf))

	case "txn-run":
		runTxnBatches(seeds, *topic, *batchSize)

	case "txn-verify":
		runTxnVerify(seeds, *topic, *expectCommitted, *expectPhysical)

	case "cpp-setup":
		recreateTopic(seeds, *inputTopic, int32(*partitions), int16(*rf))
		recreateTopic(seeds, *outputTopic, int32(*partitions), int16(*rf))
		deleteGroup(seeds, *group)

	case "cpp-seed":
		cppSeed(seeds, *inputTopic, *n, *prefix)

	case "cpp-attempt":
		cppAttempt(seeds, *group, *txnID, *inputTopic, *outputTopic, *n, *pause, *readyMarker)

	case "cpp-verify":
		cppVerify(seeds, *group, *inputTopic, *outputTopic, *label, *expectOutput, *expectGroupOffset)

	case "cpp-inspect":
		cppInspectOutputSamples(seeds, *outputTopic, *n)

	default:
		log.Fatalf("неизвестный -scenario=%q (txn-setup|txn-run|txn-verify|cpp-setup|cpp-seed|cpp-attempt|cpp-verify|cpp-inspect)", *scenario)
	}
}
