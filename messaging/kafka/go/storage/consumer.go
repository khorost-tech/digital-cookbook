package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// recvRecord — value == nil означает tombstone (Kafka null value), в отличие
// от указателя на пустую строку (value непустого, но нулевой длины — этого в
// стенде не встречается, но различие принципиально: nil ЕСТЬ маркер удаления,
// [] байт — валидное пустое значение).
type recvRecord struct {
	partition int32
	offset    int64
	key       string
	value     *string
}

// consumeAllFromStart читает topic с начала до idleTimeout без новых записей
// (число записей заранее неизвестно клиенту — ни до, ни тем более после
// compaction/retention, поэтому единственный надёжный критерий остановки —
// отсутствие прогресса, не точное expected).
func consumeAllFromStart(seeds []string, topic string, idleTimeout time.Duration) []recvRecord {
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
		if overall.Err() != nil {
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
			var v *string
			if r.Value != nil {
				s := string(r.Value)
				v = &s
			}
			out = append(out, recvRecord{partition: r.Partition, offset: r.Offset, key: string(r.Key), value: v})
			n++
		})
		if n > 0 {
			lastProgress = time.Now()
		} else if time.Since(lastProgress) > idleTimeout {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].offset < out[j].offset })
	return out
}

func valueLabel(v *string) string {
	if v == nil {
		return "<tombstone/null>"
	}
	return *v
}
