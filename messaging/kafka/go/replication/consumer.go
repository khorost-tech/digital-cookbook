package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

type recvRecord struct {
	partition int32
	offset    int64
	value     string
}

// consumeFromStart читает topic с начала. Если expected > 0 — останавливается,
// как только получено ровно expected записей (или падает по общему таймауту —
// это баг сценария/кластера, не штатный исход). Если expected == 0 — читает,
// пока не пройдёт idleTimeout без новых записей (используется, когда точное
// число подтверждённых записей заранее неизвестно, например после
// broker-kill с acks=1, где мы намеренно не знаем, сколько "долетело").
func consumeFromStart(seeds []string, topic string, expected int, idleTimeout time.Duration) []recvRecord {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(seeds...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		log.Fatalf("kgo.NewClient (consumer): %v", err)
	}
	defer cl.Close()

	overall, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var out []recvRecord
	lastProgress := time.Now()
	for {
		if expected > 0 && len(out) >= expected {
			break
		}
		if overall.Err() != nil {
			if expected > 0 {
				log.Fatalf("consume: общий таймаут, получено %d из %d ожидаемых", len(out), expected)
			}
			break
		}
		pollCtx, pollCancel := context.WithTimeout(overall, 2*time.Second)
		fetches := cl.PollFetches(pollCtx)
		pollCancel()
		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				if e.Err != nil && e.Err != context.DeadlineExceeded {
					fmt.Printf("[consume] fetch error topic=%s partition=%d: %v\n", e.Topic, e.Partition, e.Err)
				}
			}
		}
		n := 0
		fetches.EachRecord(func(r *kgo.Record) {
			out = append(out, recvRecord{partition: r.Partition, offset: r.Offset, value: string(r.Value)})
			n++
		})
		if n > 0 {
			lastProgress = time.Now()
		} else if expected == 0 && time.Since(lastProgress) > idleTimeout {
			break
		}
	}
	return out
}

func printRecvRecords(recv []recvRecord) {
	sort.Slice(recv, func(i, j int) bool {
		if recv[i].partition != recv[j].partition {
			return recv[i].partition < recv[j].partition
		}
		return recv[i].offset < recv[j].offset
	})
	for _, r := range recv {
		fmt.Printf("  partition=%d offset=%d value=%s\n", r.partition, r.offset, r.value)
	}
}

// extractIndex парсит "<prefix>-<i>" -> i, паникует (Fatalf) на неожиданном
// формате — используется для проверки идемпотентности (нет дублей индексов)
// и для сравнения acked vs фактически прочитанных индексов в acks=1 демо.
func extractIndex(value string) int {
	parts := strings.Split(value, "-")
	last := parts[len(parts)-1]
	var i int
	if _, err := fmt.Sscanf(last, "%d", &i); err != nil {
		log.Fatalf("extractIndex: не удалось распарсить индекс из %q: %v", value, err)
	}
	return i
}

// checkNoDuplicateIndices — ассерт для идемпотентности: набор индексов в
// value должен быть множеством БЕЗ повторов. Возвращает (уникальных,
// дублей-индексов-с-повторами) — вызывающий (runVerify) падает через
// log.Fatalf при dupCount > 0, это единственная механическая проверка
// отсутствия дублей в стенде, поэтому она fail-loud, а не просто печать.
func checkNoDuplicateIndices(recv []recvRecord) (unique int, dupCount int, dupSamples []int) {
	seen := map[int]int{}
	for _, r := range recv {
		i := extractIndex(r.value)
		seen[i]++
	}
	for idx, c := range seen {
		if c > 1 {
			dupCount += c - 1
			if len(dupSamples) < 10 {
				dupSamples = append(dupSamples, idx)
			}
		}
	}
	sort.Ints(dupSamples)
	return len(seen), dupCount, dupSamples
}
