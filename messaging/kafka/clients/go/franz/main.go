// Command franz — стенд #8 (клиенты в разных языках), драйвер franz-go:
// producer с ключом+идемпотентностью → consumer в group с ручным коммитом.
//
// Родословная: ЧИСТАЯ реализация протокола Kafka на Go (без CGo, без
// librdkafka). См. README стенда #8 за сводную таблицу по всем языкам/драйверам.
//
// Запуск (из контейнера на сети kafka-cookbook-net):
//
//	docker run --rm --network kafka-cookbook-net -v "$(pwd)/clients/go/franz:/app" -w /app golang:1.25 \
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

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	topic       = "demo-clients-go-franz"
	partitions  = 3
	replication = 3
	groupID     = "demo-clients-go-franz-group"
)

var keys = []string{"order-1", "order-2", "order-3", "order-4"}

func main() {
	brokers := flag.String("brokers", "kafka1:9092,kafka2:9092,kafka3:9092", "comma-separated bootstrap servers")
	flag.Parse()
	seeds := strings.Split(*brokers, ",")

	ensureTopic(seeds)
	sent := produce(seeds)
	fmt.Printf("[producer] отправлено (acks=all, idempotent=true): %d\n", sent)
	recv := consume(seeds, sent)
	fmt.Printf("[consumer] получено (group=%s, manual commit): %d\n", groupID, len(recv))

	if sent != len(recv) {
		log.Fatalf("[assert] FAIL: отправлено %d != получено %d", sent, len(recv))
	}
	fmt.Println("[assert] OK: отправлено == получено")
}

func ensureTopic(seeds []string) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		log.Fatalf("kgo.NewClient (admin): %v", err)
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := adm.DeleteTopics(ctx, topic); err != nil {
		log.Fatalf("DeleteTopics: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		listed, err := adm.ListTopics(ctx, topic)
		if err == nil {
			if t, ok := listed[topic]; !ok || t.Err != nil {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	resp, err := adm.CreateTopics(ctx, partitions, replication, nil, topic)
	if err != nil {
		log.Fatalf("CreateTopics: %v", err)
	}
	for _, t := range resp {
		if t.Err != nil {
			log.Fatalf("CreateTopics %s: %v", t.Topic, t.Err)
		}
	}
	fmt.Printf("[admin] топик %s создан (partitions=%d, rf=%d)\n", topic, partitions, replication)
}

// produce: idempotence в franz-go включена ПО УМОЛЧАНИЮ (нет отдельного флага
// "enable.idempotence" — это TransactionalID-независимая идемпотентность,
// живёт в kgo.Client всегда, если явно не отключена kgo.DisableIdempotentWrite()).
// Явно фиксируем acks=all для наглядности.
func produce(seeds []string) int {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(seeds...),
		kgo.RequiredAcks(kgo.AllISRAcks()),
	)
	if err != nil {
		log.Fatalf("kgo.NewClient (producer): %v", err)
	}
	defer cl.Close()

	ctx := context.Background()
	count := 0
	for round := 0; round < 3; round++ {
		for _, k := range keys {
			value := fmt.Sprintf("%s-evt-%d", k, round)
			rec := &kgo.Record{Topic: topic, Key: []byte(k), Value: []byte(value)}
			produceCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			res, err := cl.ProduceSync(produceCtx, rec).First()
			cancel()
			if err != nil {
				log.Fatalf("produce key=%s: %v", k, err)
			}
			fmt.Printf("  sent  key=%s partition=%d offset=%d\n", k, res.Partition, res.Offset)
			count++
		}
	}
	return count
}

// consume: группа с ручным коммитом (DisableAutoCommit + CommitRecords после
// обработки, а не после каждой записи по отдельности — типичный at-least-once
// паттерн: commit после обработки батча).
func consume(seeds []string, expected int) []string {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(seeds...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumerGroup(groupID),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		log.Fatalf("kgo.NewClient (consumer): %v", err)
	}
	defer cl.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	type rec struct {
		partition int32
		offset    int64
		key       string
	}
	var recs []rec
	for len(recs) < expected {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			log.Fatalf("consume: таймаут, получено %d из %d", len(recs), expected)
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				log.Fatalf("fetch error topic=%s partition=%d: %v", e.Topic, e.Partition, e.Err)
			}
		}
		fetches.EachRecord(func(r *kgo.Record) {
			recs = append(recs, rec{partition: r.Partition, offset: r.Offset, key: string(r.Key)})
		})
		// ручной коммит offset ПОСЛЕ обработки текущего батча fetch'а
		if err := cl.CommitUncommittedOffsets(ctx); err != nil {
			log.Fatalf("CommitUncommittedOffsets: %v", err)
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
