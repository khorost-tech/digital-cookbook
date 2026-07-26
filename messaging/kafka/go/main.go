// Command kafka-cookbook — каркас Go-стенда к серии статей "Kafka: глубокое
// погружение". Демо-программы (producer/consumer/consumer-groups/EOS/...)
// добавляются последующими задачами этой серии как отдельные подкоманды/файлы;
// здесь — только smoke-check подключения к 3-брокерному KRaft-кластеру из
// compose/compose.yml (franz-go, без CGo).
//
// Запуск с хоста (после `docker compose -f compose/compose.yml up -d`):
//
//	go run . -brokers=localhost:19092,localhost:19093,localhost:19094
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

func main() {
	brokers := flag.String("brokers", "localhost:19092,localhost:19093,localhost:19094",
		"comma-separated bootstrap servers")
	flag.Parse()

	cl, err := kgo.NewClient(
		kgo.SeedBrokers(strings.Split(*brokers, ",")...),
	)
	if err != nil {
		log.Fatalf("kgo.NewClient: %v", err)
	}
	defer cl.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	adm := kadm.NewClient(cl)
	meta, err := adm.BrokerMetadata(ctx)
	if err != nil {
		log.Fatalf("BrokerMetadata: %v", err)
	}

	fmt.Printf("cluster id: %s\n", meta.Cluster)
	for _, b := range meta.Brokers {
		rack := "-"
		if b.Rack != nil {
			rack = *b.Rack
		}
		fmt.Printf("  broker %d  host=%s:%d  rack=%s\n", b.NodeID, b.Host, b.Port, rack)
	}
}
