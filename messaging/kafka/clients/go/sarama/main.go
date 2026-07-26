// Command sarama — стенд #8, драйвер IBM/sarama: producer с
// ключом+идемпотентностью → consumer в group с ручным коммитом.
//
// Родословная: ЧИСТАЯ реализация протокола Kafka на Go (без CGo). Зрелый,
// но исторически более многословный API, чем franz-go (callback-style
// ConsumerGroupHandler вместо единого PollFetches).
//
// Запуск:
//
//	docker run --rm --network kafka-cookbook-net -v "$(pwd)/clients/go/sarama:/app" -w /app golang:1.25 \
//	  go run . -brokers=kafka1:9092,kafka2:9092,kafka3:9092
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/IBM/sarama"
)

const (
	topic       = "demo-clients-go-sarama"
	partitions  = 3
	replication = 3
	groupID     = "demo-clients-go-sarama-group"
)

var keys = []string{"order-1", "order-2", "order-3", "order-4"}

func main() {
	brokersFlag := flag.String("brokers", "kafka1:9092,kafka2:9092,kafka3:9092", "comma-separated bootstrap servers")
	flag.Parse()
	brokers := strings.Split(*brokersFlag, ",")

	sarama.Logger = log.New(io.Discard, "", 0) // тихо; ошибки идут через error-каналы

	ensureTopic(brokers)
	sent := produce(brokers)
	fmt.Printf("[producer] отправлено (acks=all, idempotent=true): %d\n", sent)
	recv := consume(brokers, sent)
	fmt.Printf("[consumer] получено (group=%s, manual commit): %d\n", groupID, len(recv))

	if sent != len(recv) {
		log.Fatalf("[assert] FAIL: отправлено %d != получено %d", sent, len(recv))
	}
	fmt.Println("[assert] OK: отправлено == получено")
}

func newConfig() *sarama.Config {
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V4_0_0_0 // протокол согласования; Kafka 4.3 брокер обратно совместим
	return cfg
}

func ensureTopic(brokers []string) {
	cfg := newConfig()
	adm, err := sarama.NewClusterAdmin(brokers, cfg)
	if err != nil {
		log.Fatalf("NewClusterAdmin: %v", err)
	}
	defer adm.Close()

	_ = adm.DeleteTopic(topic) // игнорируем ErrUnknownTopicOrPartition при первом запуске
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		topics, err := adm.ListTopics()
		if err == nil {
			if _, ok := topics[topic]; !ok {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}

	rf := int16(replication)
	err = adm.CreateTopic(topic, &sarama.TopicDetail{
		NumPartitions:     int32(partitions),
		ReplicationFactor: rf,
	}, false)
	if err != nil {
		log.Fatalf("CreateTopic: %v", err)
	}
	fmt.Printf("[admin] топик %s создан (partitions=%d, rf=%d)\n", topic, partitions, replication)
}

func produce(brokers []string) int {
	cfg := newConfig()
	cfg.Producer.RequiredAcks = sarama.WaitForAll
	cfg.Producer.Idempotent = true
	cfg.Net.MaxOpenRequests = 1 // требование sarama при idempotent-продюсере
	cfg.Producer.Return.Successes = true
	cfg.Producer.Retry.Max = 5

	producer, err := sarama.NewSyncProducer(brokers, cfg)
	if err != nil {
		log.Fatalf("NewSyncProducer: %v", err)
	}
	defer producer.Close()

	count := 0
	for round := 0; round < 3; round++ {
		for _, k := range keys {
			value := fmt.Sprintf("%s-evt-%d", k, round)
			msg := &sarama.ProducerMessage{
				Topic: topic,
				Key:   sarama.StringEncoder(k),
				Value: sarama.StringEncoder(value),
			}
			partition, offset, err := producer.SendMessage(msg)
			if err != nil {
				log.Fatalf("SendMessage key=%s: %v", k, err)
			}
			fmt.Printf("  sent  key=%s partition=%d offset=%d\n", k, partition, offset)
			count++
		}
	}
	return count
}

type recVal struct {
	partition int32
	offset    int64
	key       string
}

type handler struct {
	expected int
	mu       sync.Mutex
	recs     []recVal
	done     chan struct{}
}

func (h *handler) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (h *handler) Cleanup(sarama.ConsumerGroupSession) error { return nil }

func (h *handler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for msg := range claim.Messages() {
		h.mu.Lock()
		h.recs = append(h.recs, recVal{partition: msg.Partition, offset: msg.Offset, key: string(msg.Key)})
		n := len(h.recs)
		h.mu.Unlock()

		sess.MarkMessage(msg, "")
		sess.Commit() // ручной синхронный коммит offset ПОСЛЕ обработки каждой записи

		if n >= h.expected {
			close(h.done)
			return nil
		}
	}
	return nil
}

// consume: consumer group с ОТКЛЮЧЁННЫМ авто-коммитом — offset двигается
// только явным session.Commit() после MarkMessage (в отличие от franz-go,
// sarama не имеет единого PollFetches — модель callback per-claim).
func consume(brokers []string, expected int) []string {
	cfg := newConfig()
	cfg.Consumer.Offsets.AutoCommit.Enable = false
	cfg.Consumer.Offsets.Initial = sarama.OffsetOldest

	group, err := sarama.NewConsumerGroup(brokers, groupID, cfg)
	if err != nil {
		log.Fatalf("NewConsumerGroup: %v", err)
	}
	defer group.Close()

	h := &handler{expected: expected, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	go func() {
		for {
			if err := group.Consume(ctx, []string{topic}, h); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("group.Consume error: %v", err)
			}
			select {
			case <-h.done:
				return
			case <-ctx.Done():
				return
			default:
			}
		}
	}()

	select {
	case <-h.done:
	case <-ctx.Done():
		log.Fatalf("consume: таймаут, получено %d из %d", len(h.recs), expected)
	}

	h.mu.Lock()
	recs := append([]recVal(nil), h.recs...)
	h.mu.Unlock()

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
