package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// newAdm — лёгкое kadm-соединение для админ-операций (топики, офсеты группы).
// Тот же паттерн, что в ../replication/topic.go и ../storage/topic.go.
func newAdm(seeds []string) *kadm.Client {
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		log.Fatalf("kgo.NewClient (admin): %v", err)
	}
	return kadm.NewClient(cl)
}

// recreateTopic идемпотентно (пере)создаёт топик: удалить если есть, подождать
// пока пропадёт из метаданных, создать заново. Каждый прогон стенда стартует
// с чистого состояния (офсеты с нуля, группа без committed-офсетов).
func recreateTopic(seeds []string, name string, partitions int32, rf int16) {
	adm := newAdm(seeds)
	defer adm.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := adm.DeleteTopics(ctx, name); err != nil {
		log.Fatalf("DeleteTopics %s: %v", name, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		listed, err := adm.ListTopics(ctx, name)
		if err == nil {
			if t, ok := listed[name]; !ok || t.Err != nil {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}

	resp, err := adm.CreateTopics(ctx, partitions, rf, nil, name)
	if err != nil {
		log.Fatalf("CreateTopics %s: %v", name, err)
	}
	for _, t := range resp {
		if t.Err != nil {
			log.Fatalf("CreateTopics %s: %v", t.Topic, t.Err)
		}
	}
	fmt.Printf("[admin] топик %s создан (partitions=%d, rf=%d)\n", name, partitions, rf)
}

// deleteGroup удаляет consumer group, если она существует — используется в
// cpp-setup, чтобы каждый прогон consume-process-produce стартовал с
// абсолютно чистого состояния группы (committed-офсетов нет вовсе), а не
// полагался на то, что пересоздание топика само по себе стирает group offsets
// (в общем случае НЕ стирает — офсеты группы живут в __consumer_offsets
// независимо от топика-источника).
func deleteGroup(seeds []string, group string) {
	adm := newAdm(seeds)
	defer adm.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := adm.DeleteGroup(ctx, group)
	if err != nil && resp.Err == nil {
		// группа могла просто не существовать — это ожидаемо на первом прогоне
		fmt.Printf("[admin] DeleteGroup %s: %v (ок, если группа ещё не создавалась)\n", group, err)
		return
	}
	fmt.Printf("[admin] группа %s удалена (или не существовала)\n", group)
}

// groupCommittedTotal — сумма committed-офсетов группы по всем партициям
// топика (для партиций без committed-офсета At=-1, в сумму не идёт). Используется
// как "логическая позиция чтения" consumer-группы в cpp-verify — должна либо
// остаться НЕТРОНУТОЙ (0 новых committed-офсетов), либо продвинуться РОВНО на
// n (число обработанных записей) — никогда не оказаться где-то посередине.
func groupCommittedTotal(seeds []string, group, topic string) int64 {
	adm := newAdm(seeds)
	defer adm.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	offsets, err := adm.FetchOffsetsForTopics(ctx, group, topic)
	if err != nil {
		log.Fatalf("FetchOffsetsForTopics group=%s topic=%s: %v", group, topic, err)
	}
	var total int64
	offsets.Each(func(o kadm.OffsetResponse) {
		if o.At >= 0 {
			total += o.At
		}
	})
	return total
}

func strPtr(s string) *string { return &s }
