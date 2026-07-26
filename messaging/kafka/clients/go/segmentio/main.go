// Command segmentio — стенд #8, драйвер segmentio/kafka-go: producer с
// ключом → consumer в group с ручным коммитом.
//
// Родословная: ЧИСТАЯ реализация протокола Kafka на Go (без CGo). Чистый,
// простой API (io.Reader/Writer-подобный), но беднее по фичам, чем
// franz-go/sarama — см. content-note ниже про идемпотентность.
//
// ⚠️ Проверено живьём по исходникам библиотеки (go doc kafka.Writer,
// v0.4.49): у Writer НЕТ поля/опции идемпотентного продюсера (в отличие от
// franz-go, где идемпотентность включена по умолчанию, и sarama, где есть
// Producer.Idempotent). Постановке стенда ("producer с ключом и идемпотентностью")
// эта библиотека соответствует ЧАСТИЧНО: ключ — да, идемпотентность — нет
// такой ручки в публичном API. Честно зафиксировано, а не сымитировано.
//
// Запуск:
//
//	docker run --rm --network kafka-cookbook-net -v "$(pwd)/clients/go/segmentio:/app" -w /app golang:1.25 \
//	  go run . -brokers=kafka1:9092,kafka2:9092,kafka3:9092
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	kafka "github.com/segmentio/kafka-go"
)

const (
	topic       = "demo-clients-go-segmentio"
	partitions  = 3
	replication = 3
	groupID     = "demo-clients-go-segmentio-group"
)

var keys = []string{"order-1", "order-2", "order-3", "order-4"}

func main() {
	brokersFlag := flag.String("brokers", "kafka1:9092,kafka2:9092,kafka3:9092", "comma-separated bootstrap servers")
	flag.Parse()
	brokers := strings.Split(*brokersFlag, ",")

	ensureTopic(brokers)
	sent := produce(brokers)
	fmt.Printf("[producer] отправлено (acks=all, БЕЗ идемпотентности — не поддерживается драйвером): %d\n", sent)
	recv := consume(brokers, sent)
	fmt.Printf("[consumer] получено (group=%s, manual commit): %d\n", groupID, len(recv))

	if sent != len(recv) {
		log.Fatalf("[assert] FAIL: отправлено %d != получено %d", sent, len(recv))
	}
	fmt.Println("[assert] OK: отправлено == получено")
}

func ensureTopic(brokers []string) {
	conn, err := kafka.Dial("tcp", brokers[0])
	if err != nil {
		log.Fatalf("kafka.Dial: %v", err)
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		log.Fatalf("Controller: %v", err)
	}
	ctrlConn, err := kafka.Dial("tcp", fmt.Sprintf("%s:%d", controller.Host, controller.Port))
	if err != nil {
		log.Fatalf("Dial controller: %v", err)
	}
	defer ctrlConn.Close()

	// ⚠️ Живая находка: Conn.ReadPartitions() ВСЕГДА шлёт метаданные-запрос с
	// AllowAutoTopicCreation:true в самом протоколе (жёстко зашито в conn.go,
	// не настраивается) — использование его как "проверка, исчез ли топик
	// после delete" тихо АВТОСОЗДАЁТ топик с дефолтным num.partitions брокера
	// (=1) РАНЬШЕ, чем успевает отработать наш явный CreateTopics(partitions=3).
	// Поймано живьём: первый прогон этого стенда создал топик с 1 партицией
	// вместо 3 именно так — DeleteTopics(topic) → ReadPartitions(topic) как
	// "опрос" → тихий autocreate с 1 партицией → CreateTopics затем видит
	// "уже существует" (или не находит расхождения) и не чинит partition count.
	// Исправление — НЕ дергать ReadPartitions вообще; просто ретраить
	// CreateTopics с фиксированным интервалом, пока DeleteTopics не
	// пропагируется по кластеру.
	_ = ctrlConn.DeleteTopics(topic) // игнорируем "unknown topic" при первом запуске
	time.Sleep(1 * time.Second)

	var createErr error
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		createErr = ctrlConn.CreateTopics(kafka.TopicConfig{
			Topic:             topic,
			NumPartitions:     partitions,
			ReplicationFactor: replication,
		})
		if createErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	err = createErr
	if err != nil {
		log.Fatalf("CreateTopics: %v", err)
	}
	fmt.Printf("[admin] топик %s создан (partitions=%d, rf=%d)\n", topic, partitions, replication)
}

func produce(brokers []string) int {
	w := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafka.Murmur2Balancer{}, // тот же партиционер по ключу, что и default Java/franz-go
		RequiredAcks: kafka.RequireAll,
		Async:        false,
	}
	defer w.Close()

	ctx := context.Background()
	count := 0
	for round := 0; round < 3; round++ {
		for _, k := range keys {
			value := fmt.Sprintf("%s-evt-%d", k, round)
			msg := kafka.Message{Key: []byte(k), Value: []byte(value)}
			if err := w.WriteMessages(ctx, msg); err != nil {
				log.Fatalf("WriteMessages key=%s: %v", k, err)
			}
			fmt.Printf("  sent  key=%s (partition/offset не возвращаются WriteMessages в этой версии API)\n", k)
			count++
		}
	}
	return count
}

func consume(brokers []string, expected int) []string {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        brokers,
		Topic:          topic,
		GroupID:        groupID,
		StartOffset:    kafka.FirstOffset,
		CommitInterval: 0, // 0 = синхронный ручной коммит через r.CommitMessages
	})
	defer r.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	type recVal struct {
		partition int
		offset    int64
		key       string
	}
	var recs []recVal
	for len(recs) < expected {
		m, err := r.FetchMessage(ctx)
		if err != nil {
			log.Fatalf("FetchMessage: получено %d из %d, ошибка: %v", len(recs), expected, err)
		}
		recs = append(recs, recVal{partition: m.Partition, offset: m.Offset, key: string(m.Key)})
		if err := r.CommitMessages(ctx, m); err != nil {
			log.Fatalf("CommitMessages: %v", err)
		}
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
