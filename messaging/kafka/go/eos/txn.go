package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// (1)/(2) ядро транзакций: транзакционный producer (BeginTransaction/
// EndTransaction), батч A коммитится штатно, батч B на первой попытке
// абортится (симулированный сбой обработки в середине батча), затем
// пересылается повторно и коммитится. Зеркало
// ../../../java-deep-dive/messaging/.../EosDemo.java — тот же сценарий,
// franz-go API вместо KafkaProducer.
//
// ⚠️ В franz-go НЕТ отдельного вызова initTransactions() — производитель с
// TransactionalID(...) восстанавливает/инициализирует producer ID неявно
// внутри первого BeginTransaction() (maybeRecoverProducerID -> InitProducerId
// под капотом). В Java этот шаг явный (producer.initTransactions()) и
// вызывается один раз до первой транзакции. Функционально эквивалентно —
// разница чисто в форме API, framework-level протокол (InitProducerIdRequest)
// один и тот же.
func newTxnProducer(seeds []string, txnID string) *kgo.Client {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(seeds...),
		kgo.TransactionalID(txnID),
		kgo.TransactionTimeout(30*time.Second),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		// ⚠️ Идемпотентность НЕЛЬЗЯ выключить при заданном TransactionalID —
		// franz-go возвращает ошибку конфигурации при попытке совместить
		// TransactionalID(...) с DisableIdempotentWrite() ("cannot both
		// disable idempotent writes and use transactional IDs", см.
		// pkg/kgo/config.go). Идемпотентный producer — это подмножество
		// транзакционного, а не отдельная настройка, которую можно забыть
		// включить (в отличие от Java, где enable.idempotence нужно явно
		// выставить true — хотя с 3.0+ там тоже дефолт true).
	)
	if err != nil {
		log.Fatalf("kgo.NewClient (txn producer): %v", err)
	}
	return cl
}

type txnBatchResult struct {
	sentPhysically   int // сколько записей реально ушло в лог партиций (включая абортнутые)
	committedLogical int // сколько сообщений логически подтверждено
}

// sendBatch — один батч в одной транзакции: begin, n записей (опционально
// падаем в середине), commit либо abort. failMidway=true эмулирует сбой
// обработки на 3-й записи батча (индекс 2 из batchSize=5, как в
// java-deep-dive EosDemo) — записи 0,1,2 уже отправлены в кластер (физически
// попадут в лог партиций), но помечаются control-record'ом ABORT.
func sendBatch(cl *kgo.Client, topic, prefix string, batchSize int, failMidway bool) (sent int, committed bool) {
	if err := cl.BeginTransaction(); err != nil {
		log.Fatalf("BeginTransaction (%s): %v", prefix, err)
	}
	ctx := context.Background()
	var produced int
	var midwayErr error
	for i := 0; i < batchSize; i++ {
		rec := &kgo.Record{Topic: topic, Key: []byte(fmt.Sprintf("%s-key-%d", prefix, i)), Value: []byte(fmt.Sprintf("%s-%d", prefix, i))}
		res := cl.ProduceSync(ctx, rec)
		if err := res.FirstErr(); err != nil {
			log.Fatalf("ProduceSync (%s, i=%d): %v", prefix, i, err)
		}
		produced++
		if failMidway && i == 2 {
			midwayErr = fmt.Errorf("симулированный сбой обработки в середине батча %s", prefix)
			break
		}
	}
	if midwayErr != nil {
		if err := cl.AbortBufferedRecords(ctx); err != nil {
			log.Fatalf("AbortBufferedRecords (%s): %v", prefix, err)
		}
		if err := cl.EndTransaction(ctx, kgo.TryAbort); err != nil {
			log.Fatalf("EndTransaction(abort) (%s): %v", prefix, err)
		}
		fmt.Printf("  [txn] батч %s: сбой (%s) -> EndTransaction(TryAbort) — %d записей физически ушли, логически ничего не подтверждено\n", prefix, midwayErr, produced)
		return produced, false
	}
	// EndTransaction(TryCommit) само по себе не флашит — по документации
	// библиотеки Flush должен быть вызван до него явно (ProduceSync уже
	// гарантирует, что каждая отдельная запись подтверждена брокером, но
	// явный Flush — рекомендованная библиотекой практика перед commit).
	if err := cl.Flush(ctx); err != nil {
		log.Fatalf("Flush (%s): %v", prefix, err)
	}
	if err := cl.EndTransaction(ctx, kgo.TryCommit); err != nil {
		log.Fatalf("EndTransaction(commit) (%s): %v", prefix, err)
	}
	fmt.Printf("  [txn] батч %s: EndTransaction(TryCommit) — %d сообщений подтверждено\n", prefix, produced)
	return produced, true
}

func runTxnBatches(seeds []string, topic string, batchSize int) txnBatchResult {
	cl := newTxnProducer(seeds, "cookbook-eos-txn-producer")
	defer cl.Close()

	var result txnBatchResult

	// Батч A: штатный коммит.
	n, ok := sendBatch(cl, topic, "batchA", batchSize, false)
	result.sentPhysically += n
	if ok {
		result.committedLogical += n
	}

	// Батч B, попытка 1: падает в середине -> abort.
	n, ok = sendBatch(cl, topic, "batchB", batchSize, true)
	result.sentPhysically += n
	if ok {
		result.committedLogical += n
	}

	// Батч B, попытка 2 (повтор после "устранения сбоя"): штатный коммит.
	n, ok = sendBatch(cl, topic, "batchB", batchSize, false)
	result.sentPhysically += n
	if ok {
		result.committedLogical += n
	}

	fmt.Printf("[txn] физически отправлено записей: %d, логически подтверждено: %d\n",
		result.sentPhysically, result.committedLogical)
	return result
}

// consumeIsolation читает topic с начала указанным isolation level, пока не
// пройдёт idleTimeout без новых записей. Используется дважды в txn-verify:
// read_committed (должен видеть ровно committedLogical) и read_uncommitted
// (должен видеть sentPhysically, включая абортнутый батч).
func consumeIsolation(seeds []string, topic string, level kgo.IsolationLevel, idleTimeout time.Duration) int {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(seeds...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.FetchIsolationLevel(level),
	)
	if err != nil {
		log.Fatalf("kgo.NewClient (consumer isolation): %v", err)
	}
	defer cl.Close()

	overall, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	count := 0
	lastProgress := time.Now()
	for {
		if overall.Err() != nil {
			break
		}
		pollCtx, pollCancel := context.WithTimeout(overall, 2*time.Second)
		fetches := cl.PollFetches(pollCtx)
		pollCancel()
		n := 0
		fetches.EachRecord(func(r *kgo.Record) { n++ })
		count += n
		if n > 0 {
			lastProgress = time.Now()
		} else if time.Since(lastProgress) > idleTimeout {
			break
		}
	}
	return count
}

func runTxnVerify(seeds []string, topic string, expectCommitted, expectPhysical int) {
	committedCount := consumeIsolation(seeds, topic, kgo.ReadCommitted(), 5*time.Second)
	uncommittedCount := consumeIsolation(seeds, topic, kgo.ReadUncommitted(), 5*time.Second)
	fmt.Printf("[txn-verify] read_committed=%d read_uncommitted=%d (ожидалось: committed=%d physical=%d)\n",
		committedCount, uncommittedCount, expectCommitted, expectPhysical)

	if committedCount != expectCommitted {
		log.Fatalf("[txn-verify] РАСХОЖДЕНИЕ: read_committed=%d, ожидалось %d", committedCount, expectCommitted)
	}
	if uncommittedCount != expectPhysical {
		log.Fatalf("[txn-verify] РАСХОЖДЕНИЕ: read_uncommitted=%d, ожидалось %d", uncommittedCount, expectPhysical)
	}
	if !(uncommittedCount > committedCount) {
		log.Fatalf("[txn-verify] РАСХОЖДЕНИЕ: read_uncommitted (%d) должен быть строго больше read_committed (%d) — абортнутый батч физически на диске, но невидим read_committed", uncommittedCount, committedCount)
	}
	fmt.Printf("[txn-verify] OK: read_committed == закоммиченное (%d), read_uncommitted (%d) строго больше — абортнутый батч физически записан, но невидим read_committed-консьюмеру\n", committedCount, uncommittedCount)
}
