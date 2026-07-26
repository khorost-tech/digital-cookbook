// Command log-basics — стенд #1 серии "Kafka: глубокое погружение":
// ментальная модель лога — топик, партиции, ключ → партиция, порядок в
// пределах партиции.
//
// Сценарий (одинаковый на Go и Java, см. ../../java/log-basics):
//  1. (Пере)создаёт топик demo-log (3 партиции, RF=3).
//  2. Producer отправляет messagesPerKey сообщений на каждый из ключей,
//     чередуя ключи (round-robin по раундам), а не пачками подряд — чтобы
//     детерминированность key→partition была видна не как побочный эффект
//     батчинга одного ключа, а как свойство партиционера.
//  3. Consumer читает весь топик с начала, печатает (partition, offset,
//     key, value) по каждой записи.
//  4. Ассерты (падают при расхождении):
//     - все сообщения одного ключа лежат в одной партиции;
//     - offset в пределах каждой партиции строго монотонно растёт;
//     - отправлено == получено.
//  5. Печатает фактическое распределение ключей по партициям (murmur2 %
//     partitions — см. README.md стенда за content-note про коллизии).
//
// Запуск (из контейнера на сети kafka-cookbook-net, см. ../../README.md):
//
//	docker run --rm --network kafka-cookbook-net -v "$(pwd)/go:/app" -w /app golang:1.25 \
//	  go run ./log-basics -brokers=kafka1:9092,kafka2:9092,kafka3:9092
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
	topic          = "demo-log"
	partitions     = 3
	replication    = 3
	messagesPerKey = 5
)

// Ключи намеренно избыточны относительно числа партиций (6 ключей на 3
// партиции) — чтобы почти наверняка увидеть коллизию murmur2 % 3 (два разных
// ключа в одной партиции) и явно её задокументировать, а не полагаться на
// то, что читатель поверит на слово.
var keys = []string{"user-1", "user-2", "user-3", "user-4", "user-5", "user-6"}

type sentRecord struct {
	key       string
	value     string
	partition int32
	offset    int64
}

type recvRecord struct {
	partition int32
	offset    int64
	key       string
	value     string
}

func main() {
	brokers := flag.String("brokers", "kafka1:9092,kafka2:9092,kafka3:9092",
		"comma-separated bootstrap servers (compose-net internal listeners)")
	flag.Parse()
	seeds := strings.Split(*brokers, ",")

	ensureTopic(seeds)

	sent := produce(seeds)
	fmt.Printf("[producer] всего отправлено: %d\n", len(sent))

	recv := consume(seeds, len(sent))
	fmt.Printf("[consumer] всего получено: %d\n", len(recv))

	printDistribution(sent)
	printRecords(recv)
	runAsserts(sent, recv)

	fmt.Println("[assert] все проверки пройдены (key→partition, монотонность offset, sent==received)")
}

// ensureTopic идемпотентно (пере)создаёт топик: если он уже существует от
// предыдущего прогона — удаляет и создаёт заново, чтобы офсеты в выводе
// начинались с чистого листа и были воспроизводимы.
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
	// после удаления контроллеру нужно немного времени, чтобы топик
	// пропал из метаданных, прежде чем создавать его заново
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
		fmt.Printf("[admin] топик %s создан (partitions=%d, rf=%d)\n", t.Topic, partitions, replication)
	}
}

// produce отправляет messagesPerKey сообщений на каждый ключ из keys,
// чередуя ключи по раундам (round-0: все ключи по одному разу, round-1: все
// ключи ещё по разу, ...), синхронно дожидаясь метаданных каждой отправки —
// так порядок отправки полностью детерминирован и легко проверяем.
func produce(seeds []string) []sentRecord {
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		log.Fatalf("kgo.NewClient (producer): %v", err)
	}
	defer cl.Close()

	ctx := context.Background()
	var results []sentRecord

	for round := 0; round < messagesPerKey; round++ {
		for _, k := range keys {
			value := fmt.Sprintf("%s-msg-%d", k, round)
			rec := &kgo.Record{Topic: topic, Key: []byte(k), Value: []byte(value)}
			produceCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			produced, err := cl.ProduceSync(produceCtx, rec).First()
			cancel()
			if err != nil {
				log.Fatalf("produce key=%s: %v", k, err)
			}
			results = append(results, sentRecord{
				key:       k,
				value:     value,
				partition: produced.Partition,
				offset:    produced.Offset,
			})
		}
	}
	return results
}

// consume читает demo-log с начала до тех пор, пока не получит ровно
// expected записей (или не истечёт таймаут — тогда это баг стенда/кластера,
// а не ожидаемый исход, поэтому падаем).
func consume(seeds []string, expected int) []recvRecord {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(seeds...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		log.Fatalf("kgo.NewClient (consumer): %v", err)
	}
	defer cl.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var out []recvRecord
	for len(out) < expected {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			log.Fatalf("consume: таймаут, получено %d из %d ожидаемых", len(out), expected)
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				log.Fatalf("fetch error topic=%s partition=%d: %v", e.Topic, e.Partition, e.Err)
			}
		}
		fetches.EachRecord(func(r *kgo.Record) {
			out = append(out, recvRecord{
				partition: r.Partition,
				offset:    r.Offset,
				key:       string(r.Key),
				value:     string(r.Value),
			})
		})
	}
	return out
}

func printDistribution(sent []sentRecord) {
	byPartition := map[int32]map[string]bool{}
	for _, s := range sent {
		if byPartition[s.partition] == nil {
			byPartition[s.partition] = map[string]bool{}
		}
		byPartition[s.partition][s.key] = true
	}

	fmt.Println("\n[распределение] ключ → партиция (murmur2(key) % partitions, franz-go default partitioner):")
	var parts []int32
	for p := range byPartition {
		parts = append(parts, p)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i] < parts[j] })
	for _, p := range parts {
		var ks []string
		for k := range byPartition[p] {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		fmt.Printf("  partition %d: %s\n", p, strings.Join(ks, ", "))
	}
}

func printRecords(recv []recvRecord) {
	sort.Slice(recv, func(i, j int) bool {
		if recv[i].partition != recv[j].partition {
			return recv[i].partition < recv[j].partition
		}
		return recv[i].offset < recv[j].offset
	})
	fmt.Println("\n[consumer] записи по (partition, offset):")
	for _, r := range recv {
		fmt.Printf("  partition=%d offset=%d key=%s value=%s\n", r.partition, r.offset, r.key, r.value)
	}
}

// runAsserts падает (log.Fatalf) при малейшем расхождении — это не "мягкая"
// проверка для отчёта, а условие честности стенда.
func runAsserts(sent []sentRecord, recv []recvRecord) {
	if len(sent) != len(recv) {
		log.Fatalf("[assert] FAIL: отправлено %d != получено %d", len(sent), len(recv))
	}

	// 1) один и тот же ключ — всегда одна партиция (проверяем и по данным
	// продюсера, и независимо по данным консьюмера).
	checkKeyPinned := func(label string, keyPart map[string]int32, add func(func(key string, partition int32))) {
		add(func(key string, partition int32) {
			if prev, ok := keyPart[key]; ok {
				if prev != partition {
					log.Fatalf("[assert] FAIL (%s): ключ %s встречен в разных партициях: %d и %d", label, key, prev, partition)
				}
			} else {
				keyPart[key] = partition
			}
		})
	}
	sentKeyPart := map[string]int32{}
	checkKeyPinned("producer", sentKeyPart, func(f func(string, int32)) {
		for _, s := range sent {
			f(s.key, s.partition)
		}
	})
	recvKeyPart := map[string]int32{}
	checkKeyPinned("consumer", recvKeyPart, func(f func(string, int32)) {
		for _, r := range recv {
			f(r.key, r.partition)
		}
	})
	for k, p := range sentKeyPart {
		if recvKeyPart[k] != p {
			log.Fatalf("[assert] FAIL: ключ %s — партиция у producer=%d, у consumer=%d", k, p, recvKeyPart[k])
		}
	}
	fmt.Println("[assert] OK: каждый ключ — в ровно одной партиции (producer и consumer согласны)")

	// 2) offset строго монотонно растёт в пределах партиции (по порядку,
	// в котором записи реально лежат в логе).
	byPartition := map[int32][]recvRecord{}
	for _, r := range recv {
		byPartition[r.partition] = append(byPartition[r.partition], r)
	}
	for p, rs := range byPartition {
		sort.Slice(rs, func(i, j int) bool { return rs[i].offset < rs[j].offset })
		for i := 1; i < len(rs); i++ {
			if rs[i].offset != rs[i-1].offset+1 {
				log.Fatalf("[assert] FAIL: партиция %d — offset не монотонен подряд: %d затем %d", p, rs[i-1].offset, rs[i].offset)
			}
		}
	}
	fmt.Println("[assert] OK: offset монотонно растёт (шаг 1) в пределах каждой партиции")

	fmt.Printf("[assert] OK: отправлено == получено == %d\n", len(sent))
}
