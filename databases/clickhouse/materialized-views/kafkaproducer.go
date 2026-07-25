package main

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

var kafkaEventTypes = []string{"page_view", "click", "search", "add_to_cart", "checkout", "purchase", "signup", "logout"}
var kafkaCountries = []string{"RU", "US", "DE", "GB", "FR", "IN", "BR", "CN", "JP", "KZ"}

// ensureKafkaTopic — идемпотентно (пере)создаёт топик, тот же паттерн, что
// ../../kafka/go/log-basics/main.go ensureTopic: удаляет, если уже
// существует от предыдущего прогона, ждёт, пока пропадёт из метаданных,
// создаёт заново с чистого листа (офсеты воспроизводимо с 0).
func ensureKafkaTopic(seeds []string, topic string, partitions int32, rf int16) error {
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		return fmt.Errorf("kgo.NewClient (admin): %w", err)
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, _ = adm.DeleteTopics(ctx, topic) // не фатально, если топика ещё не было

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		listed, err := adm.ListTopics(ctx, topic)
		if err == nil {
			if t, ok := listed[topic]; !ok || t.Err != nil {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}

	resp, err := adm.CreateTopics(ctx, partitions, rf, nil, topic)
	if err != nil {
		return fmt.Errorf("CreateTopics: %w", err)
	}
	for _, t := range resp {
		if t.Err != nil {
			return fmt.Errorf("CreateTopics %s: %w", t.Topic, t.Err)
		}
	}
	return nil
}

// produceEvents — franz-go продюсер: n синтетических JSON-событий
// (совместимых с ENGINE=Kafka JSONEachRow, схема demo.mv_events_queue),
// асинхронно (Produce + callback), с финальным Flush — тот же приём
// пайплайнинга, что типичное bulk-использование franz-go (без синхронного
// ProduceSync на каждое сообщение, который был бы слишком медленным для 50k
// сообщений, см. ../../kafka/go/log-basics для контраста — там
// ProduceSync на 30 сообщений, здесь пайплайн на 50 000).
func produceEvents(seeds []string, topic string, n int, seed int64) (sent int, elapsed time.Duration, err error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		return 0, 0, fmt.Errorf("kgo.NewClient (producer): %w", err)
	}
	defer cl.Close()

	rng := rand.New(rand.NewSource(seed))
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var success int64
	var firstErr error
	start := time.Now()
	for i := 0; i < n; i++ {
		msg := syntheticKafkaEvent(rng)
		rec := &kgo.Record{Topic: topic, Value: []byte(msg)}
		cl.Produce(ctx, rec, func(_ *kgo.Record, produceErr error) {
			if produceErr != nil {
				if firstErr == nil {
					firstErr = produceErr
				}
				return
			}
			atomic.AddInt64(&success, 1)
		})
	}
	if flushErr := cl.Flush(ctx); flushErr != nil {
		return int(atomic.LoadInt64(&success)), time.Since(start), fmt.Errorf("flush: %w", flushErr)
	}
	if firstErr != nil {
		return int(atomic.LoadInt64(&success)), time.Since(start), fmt.Errorf("produce error: %w", firstErr)
	}
	return int(atomic.LoadInt64(&success)), time.Since(start), nil
}

// syntheticKafkaEvent — JSON-строка, совместимая с ENGINE=Kafka
// (kafka_format='JSONEachRow') + схемой demo.mv_events_queue. event_time —
// случайный сдвиг в пределах последних 2 часов от текущего момента
// (имитация "живого" потока с разбросом по нескольким
// toStartOfHour-бакетам, не единая точка времени).
func syntheticKafkaEvent(rng *rand.Rand) string {
	userID := rng.Intn(50000) + 1
	eventType := kafkaEventTypes[rng.Intn(len(kafkaEventTypes))]
	country := kafkaCountries[rng.Intn(len(kafkaCountries))]
	durationMs := rng.Intn(7950) + 50
	revenue := 0.0
	if rng.Intn(10) == 0 { // 10% событий — платёжные, revenue>0 (тот же принцип, что dataset/main.go)
		revenue = 5 + rng.Float64()*295
	}
	eventTime := time.Now().UTC().Add(-time.Duration(rng.Intn(120)) * time.Minute).Truncate(time.Second)
	url := fmt.Sprintf("/product/%d", rng.Intn(100000)+1)

	var b strings.Builder
	b.WriteString(`{"event_time":"`)
	b.WriteString(eventTime.Format(csvTimeLayout))
	b.WriteString(`","user_id":`)
	fmt.Fprintf(&b, "%d", userID)
	b.WriteString(`,"event_type":"`)
	b.WriteString(eventType)
	b.WriteString(`","url":"`)
	b.WriteString(url)
	b.WriteString(`","duration_ms":`)
	fmt.Fprintf(&b, "%d", durationMs)
	b.WriteString(`,"country":"`)
	b.WriteString(country)
	b.WriteString(`","revenue":`)
	fmt.Fprintf(&b, "%.2f", revenue)
	b.WriteString(`}`)
	return b.String()
}
