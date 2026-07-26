package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// newAdm — как в replication/topic.go: лёгкое kadm-соединение для
// admin-операций (создание топика, offsets).
func newAdm(seeds []string) *kadm.Client {
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		log.Fatalf("kgo.NewClient (admin): %v", err)
	}
	return kadm.NewClient(cl)
}

// recreateTopic — идемпотентное (пере)создание топика с конфигами (тот же
// паттерн, что в log-basics/replication): удалить если есть, дождаться
// исчезновения из метаданных, создать заново с partitions/rf/configs.
func recreateTopic(seeds []string, name string, partitions int32, rf int16, configs map[string]*string) {
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

	resp, err := adm.CreateTopics(ctx, partitions, rf, configs, name)
	if err != nil {
		log.Fatalf("CreateTopics %s: %v", name, err)
	}
	for _, t := range resp {
		if t.Err != nil {
			log.Fatalf("CreateTopics %s: %v", t.Topic, t.Err)
		}
	}
	fmt.Printf("[admin] топик %s создан (partitions=%d, rf=%d, configs=%v)\n", name, partitions, rf, flattenConfigs(configs))
}

func flattenConfigs(configs map[string]*string) map[string]string {
	out := map[string]string{}
	for k, v := range configs {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

func strPtr(s string) *string { return &s }

// waitForLeader — ждёт, пока у партиции 0 появится валидный лидер (после
// CreateTopics метаданные расходятся по кластеру не мгновенно).
func waitForLeader(seeds []string, name string, timeout time.Duration) {
	adm := newAdm(seeds)
	defer adm.Close()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		listed, err := adm.ListTopics(ctx, name)
		cancel()
		if err == nil {
			if t, ok := listed[name]; ok && t.Err == nil {
				if p, ok := t.Partitions[0]; ok && p.Leader >= 0 {
					fmt.Printf("[admin] топик %s: лидер партиции 0 = %d\n", name, p.Leader)
					return
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	log.Fatalf("waitForLeader: топик %s не получил лидера за %s", name, timeout)
}

// offsets — earliest (log start offset) и latest (log end offset) партиции 0.
type offsets struct {
	earliest int64
	latest   int64
}

// reportOffsets печатает и возвращает earliest/latest offset партиции 0 —
// это единственный способ клиента увидеть эффект retention-чистки (без
// доступа к docker socket): earliest сдвигается вперёд, когда брокер
// физически удаляет старые сегменты.
func reportOffsets(seeds []string, topic string, label string) offsets {
	adm := newAdm(seeds)
	defer adm.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start, err := adm.ListStartOffsets(ctx, topic)
	if err != nil {
		log.Fatalf("ListStartOffsets %s: %v", topic, err)
	}
	end, err := adm.ListEndOffsets(ctx, topic)
	if err != nil {
		log.Fatalf("ListEndOffsets %s: %v", topic, err)
	}
	so, ok := start.Lookup(topic, 0)
	if !ok || so.Err != nil {
		log.Fatalf("ListStartOffsets %s: партиция 0 не найдена или ошибка %v", topic, so.Err)
	}
	eo, ok := end.Lookup(topic, 0)
	if !ok || eo.Err != nil {
		log.Fatalf("ListEndOffsets %s: партиция 0 не найдена или ошибка %v", topic, eo.Err)
	}
	fmt.Printf("[offsets] %s: topic=%s earliest=%d latest=%d (записей сейчас читаемо: %d)\n",
		label, topic, so.Offset, eo.Offset, eo.Offset-so.Offset)
	return offsets{earliest: so.Offset, latest: eo.Offset}
}
