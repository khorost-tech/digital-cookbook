// Command confluent — стенд #8, драйвер confluent-kafka-go/v2 (обёртка над
// librdkafka на CGo): producer с ключом+идемпотентностью → consumer в group
// с ручным коммитом.
//
// Родословная: обёртка над librdkafka (C) — тот же движок, что у
// confluent-kafka (Python), Confluent.Kafka (C#), rdkafka (Rust),
// modern-cpp-kafka (C++). НУЖНА нативная зависимость (librdkafka-dev в
// образе сборки) — единственный из четырёх Go-драйверов стенда с CGo.
//
// Запуск (librdkafka-dev ставится inline в golang-образе — Dockerfile не нужен):
//
//	docker run --rm --network kafka-cookbook-net -v "$(pwd)/clients/go/confluent:/app" -w /app golang:1.25 \
//	  sh -c "apt-get update -qq && apt-get install -y -qq librdkafka-dev pkg-config >/dev/null && \
//	         go run . -brokers=kafka1:9092,kafka2:9092,kafka3:9092"
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sort"
	"time"

	kafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

const (
	partitions  = 3
	replication = 3
	groupID     = "demo-clients-go-confluent-group"
)

// var (не const) — нужен адресуемый string для kafka.TopicPartition.Topic (*string).
var topic = "demo-clients-go-confluent"

var keys = []string{"order-1", "order-2", "order-3", "order-4"}

func main() {
	brokersFlag := flag.String("brokers", "kafka1:9092,kafka2:9092,kafka3:9092", "comma-separated bootstrap servers")
	flag.Parse()
	brokers := *brokersFlag

	ensureTopic(brokers)
	sent := produce(brokers)
	fmt.Printf("[producer] отправлено (acks=all, enable.idempotence=true): %d\n", sent)
	recv := consume(brokers, sent)
	fmt.Printf("[consumer] получено (group=%s, manual commit): %d\n", groupID, len(recv))

	if sent != len(recv) {
		log.Fatalf("[assert] FAIL: отправлено %d != получено %d", sent, len(recv))
	}
	fmt.Println("[assert] OK: отправлено == получено")
}

func ensureTopic(brokers string) {
	adm, err := kafka.NewAdminClient(&kafka.ConfigMap{"bootstrap.servers": brokers})
	if err != nil {
		log.Fatalf("NewAdminClient: %v", err)
	}
	defer adm.Close()

	deadlineCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, _ = adm.DeleteTopics(deadlineCtx, []string{topic}) // игнорируем "unknown topic" при первом запуске
	time.Sleep(1 * time.Second)

	var lastErr error
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
		res, err := adm.CreateTopics(ctx2, []kafka.TopicSpecification{{
			Topic:             topic,
			NumPartitions:     partitions,
			ReplicationFactor: replication,
		}})
		cancel2()
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		lastErr = nil
		for _, r := range res {
			if r.Error.Code() != kafka.ErrNoError {
				lastErr = r.Error
			}
		}
		if lastErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr != nil {
		log.Fatalf("CreateTopics: %v", lastErr)
	}
	fmt.Printf("[admin] топик %s создан (partitions=%d, rf=%d)\n", topic, partitions, replication)
}

func produce(brokers string) int {
	p, err := kafka.NewProducer(&kafka.ConfigMap{
		"bootstrap.servers": brokers,
		"acks":              "all",
		"enable.idempotence": true,
	})
	if err != nil {
		log.Fatalf("NewProducer: %v", err)
	}
	defer p.Close()

	deliveries := make(chan kafka.Event, 32)
	count := 0
	for round := 0; round < 3; round++ {
		for _, k := range keys {
			value := fmt.Sprintf("%s-evt-%d", k, round)
			err := p.Produce(&kafka.Message{
				TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
				Key:            []byte(k),
				Value:          []byte(value),
			}, deliveries)
			if err != nil {
				log.Fatalf("Produce key=%s: %v", k, err)
			}
			ev := <-deliveries
			msg := ev.(*kafka.Message)
			if msg.TopicPartition.Error != nil {
				log.Fatalf("delivery error key=%s: %v", k, msg.TopicPartition.Error)
			}
			fmt.Printf("  sent  key=%s partition=%d offset=%d\n", k, msg.TopicPartition.Partition, msg.TopicPartition.Offset)
			count++
		}
	}
	close(deliveries)
	return count
}

func consume(brokers string, expected int) []string {
	c, err := kafka.NewConsumer(&kafka.ConfigMap{
		"bootstrap.servers":  brokers,
		"group.id":           groupID,
		"auto.offset.reset":  "earliest",
		"enable.auto.commit": false,
		"partition.assignment.strategy": "cooperative-sticky",
	})
	if err != nil {
		log.Fatalf("NewConsumer: %v", err)
	}
	defer c.Close()

	if err := c.Subscribe(topic, nil); err != nil {
		log.Fatalf("Subscribe: %v", err)
	}

	type recVal struct {
		partition int32
		offset    int64
		key       string
	}
	var recs []recVal
	deadline := time.Now().Add(30 * time.Second)
	for len(recs) < expected && time.Now().Before(deadline) {
		ev := c.Poll(1000)
		if ev == nil {
			continue
		}
		switch m := ev.(type) {
		case *kafka.Message:
			recs = append(recs, recVal{
				partition: m.TopicPartition.Partition,
				offset:    int64(m.TopicPartition.Offset),
				key:       string(m.Key),
			})
			if _, err := c.CommitMessage(m); err != nil {
				log.Fatalf("CommitMessage: %v", err)
			}
		case kafka.Error:
			log.Fatalf("consumer error: %v", m)
		}
	}
	if len(recs) < expected {
		log.Fatalf("consume: таймаут, получено %d из %d", len(recs), expected)
	}

	sort.Slice(recs, func(i, j int) bool {
		if recs[i].partition != recs[j].partition {
			return recs[i].partition < recs[j].partition
		}
		return recs[i].offset < recs[j].offset
	})
	var out []string
	for _, r := range recs {
		line := fmt.Sprintf("(partition=%d, offset=%d, key=%s)", r.partition, r.offset, r.key)
		fmt.Println("  recv  " + line)
		out = append(out, line)
	}
	return out
}
