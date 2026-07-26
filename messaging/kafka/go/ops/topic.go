package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// newAdm — тот же паттерн, что во всех предыдущих стендах (replication/storage/eos):
// лёгкое kadm-соединение для админ-операций.
func newAdm(seeds []string) *kadm.Client {
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		log.Fatalf("kgo.NewClient (admin): %v", err)
	}
	return kadm.NewClient(cl)
}

// recreateTopic — идемпотентное (пере)создание топика, тот же паттерн, что в
// ../replication/topic.go / ../storage/topic.go / ../eos/topic.go.
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

// waitTopicReady ждёт, пока топик появится в метаданных со всеми партициями
// (после CreateTopics метаданные могут разойтись по кластеру не мгновенно —
// та же гонка UNKNOWN_TOPIC_OR_PARTITION, что в ../replication/topic.go).
func waitTopicReady(seeds []string, name string, expectPartitions int, timeout time.Duration) {
	adm := newAdm(seeds)
	defer adm.Close()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		listed, err := adm.ListTopics(ctx, name)
		cancel()
		if err == nil {
			if t, ok := listed[name]; ok && t.Err == nil && len(t.Partitions) == expectPartitions {
				return
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	log.Fatalf("waitTopicReady: топик %s не готов (ожидалось %d партиций) за %s", name, expectPartitions, timeout)
}
