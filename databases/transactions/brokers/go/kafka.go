package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// runKafka: транзакционный producer + exactly-once semantics. Записи, отправленные в
// прерванной (aborted) транзакции, консьюмер с isolation.level=read_committed НЕ видит;
// закоммиченные — видит. Это и есть атомарность публикации в лог: всё или ничего.
func runKafka() {
	broker := envOr("KAFKA", "localhost:9095")
	topic := "tx-topic"
	ctx := context.Background()

	prod, err := kgo.NewClient(
		kgo.SeedBrokers(broker),
		kgo.TransactionalID("tx-demo-producer"), // включает транзакции + idempotent producer
		kgo.DefaultProduceTopic(topic),
		kgo.AllowAutoTopicCreation(),
	)
	if err != nil {
		fmt.Println("kafka producer:", err)
		return
	}
	defer prod.Close()

	produceTx := func(prefix string, commit bool) {
		if err := prod.BeginTransaction(); err != nil {
			fmt.Println("begin tx:", err)
			return
		}
		for i := 0; i < 10; i++ {
			prod.Produce(ctx, &kgo.Record{Value: []byte(fmt.Sprintf("%s-%d", prefix, i))}, nil)
		}
		_ = prod.Flush(ctx)
		kind := kgo.TryCommit
		if !commit {
			kind = kgo.TryAbort
		}
		if err := prod.EndTransaction(ctx, kind); err != nil {
			fmt.Println("end tx:", err)
		}
	}
	produceTx("aborted", false)  // 10 записей в прерванной транзакции
	produceTx("committed", true) // 10 записей в закоммиченной

	cons, err := kgo.NewClient(
		kgo.SeedBrokers(broker),
		kgo.ConsumeTopics(topic),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()), // ключевое: не видим aborted
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		fmt.Println("kafka consumer:", err)
		return
	}
	defer cons.Close()

	committed, aborted := 0, 0
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	for committed < 10 && cctx.Err() == nil {
		fs := cons.PollFetches(cctx)
		if fs.IsClientClosed() {
			break
		}
		fs.EachRecord(func(r *kgo.Record) {
			if strings.HasPrefix(string(r.Value), "aborted") {
				aborted++
			} else {
				committed++
			}
		})
	}
	fmt.Printf("kafka (EOS): продюсер отправил 10 aborted + 10 committed. read_committed consumer увидел committed=%d aborted=%d — прерванная транзакция невидима\n",
		committed, aborted)
}
