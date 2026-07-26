package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// (3) Атомарный consume-process-produce — ядро EOS. Читаем из input-топика в
// составе consumer-группы, обрабатываем, пишем в output + коммитим офсет
// input в ОДНОЙ транзакции. franz-go делает это через GroupTransactSession —
// НЕТ отдельного вызова, аналогичного Java sendOffsetsToTransaction(): вместо
// явной передачи map офсетов, session САМА отслеживает, что было
// вычитано этим клиентом с последнего Begin() (через внутренний
// UncommittedOffsets()/CommittedOffsets()), и коммитит именно эту дельту
// внутри session.End(ctx, kgo.TryCommit) — та же гарантия (офсет input и
// запись в output в одном EndTxnRequest), другая форма API: Java явно строит
// Map<TopicPartition,OffsetAndMetadata> и передаёт её в
// producer.sendOffsetsToTransaction(offsets, groupMetadata) ПЕРЕД
// commitTransaction(); franz-go делает то же самое неявно внутри одного
// вызова End().
func newCppProducerFn() func(string) string {
	return func(v string) string { return "processed:" + v }
}

// cppSeed — засевает input-топик n записями (обычный идемпотентный, НЕ
// транзакционный producer — вход конвейера сам по себе не обязан быть
// транзакционным, транзакция нужна на стороне consume-process-produce).
func cppSeed(seeds []string, topic string, n int, prefix string) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		log.Fatalf("kgo.NewClient (cpp-seed): %v", err)
	}
	defer cl.Close()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		rec := &kgo.Record{Topic: topic, Key: []byte(fmt.Sprintf("%s-key-%d", prefix, i)), Value: []byte(fmt.Sprintf("%s-%d", prefix, i))}
		res := cl.ProduceSync(ctx, rec)
		if err := res.FirstErr(); err != nil {
			log.Fatalf("ProduceSync (cpp-seed, i=%d): %v", i, err)
		}
	}
	fmt.Printf("[cpp-seed] topic=%s: засеяно %d записей\n", topic, n)
}

// cppAttempt — ОДНА попытка consume-process-produce: собрать n записей из
// input, обработать, записать в output + закоммитить офсет input в одной
// транзакции. pause>0 — после ProduceSync (записи УЖЕ физически на диске
// output-партиции под открытой транзакцией), НО ДО session.End(commit),
// печатает маркер readyMarker и спит pause — окно, в которое host-скрипт
// (../../ops/eos-kill.sh) убивает процесс SIGKILL, эмулируя крах ровно
// между "записали" и "закоммитили" (офсет input НЕ продвинут, потому что
// commitTransactionOffsets происходит только внутри End()).
//
// Один и тот же код используется и для "убиваемой" попытки (pause>0, host
// её обрывает) и для "успешного повтора" (pause=0, отрабатывает штатно
// до конца) — именно повтор с pause=0 и демонстрирует восстановление: то же
// TransactionalID, тот же consumer group — предыдущая незакоммиченная
// транзакция (если была) фенсится/абортится брокером на стороне
// InitProducerId (см. newCppSession) ДО начала нового чтения.
func cppAttempt(seeds []string, group, txnID, inputTopic, outputTopic string, n int, pause time.Duration, readyMarker string) {
	session, err := kgo.NewGroupTransactSession(
		kgo.SeedBrokers(seeds...),
		kgo.ConsumeTopics(inputTopic),
		kgo.ConsumerGroup(group),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.TransactionalID(txnID),
		kgo.TransactionTimeout(30*time.Second),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.Balancers(kgo.CooperativeStickyBalancer()),
		kgo.DisableAutoCommit(), // явно; фактически franz-go и сам не автокоммитит при заданном TransactionalID
	)
	if err != nil {
		log.Fatalf("kgo.NewGroupTransactSession: %v", err)
	}
	defer session.Close()

	// ⚠️ Явного initTransactions()-аналога тоже нет — Begin() вызывает
	// cl.BeginTransaction(), которая сама восстанавливает/инициализирует
	// producer ID при первом вызове. Если ПРЕДЫДУЩИЙ процесс с тем же
	// TransactionalID оставил транзакцию открытой (был убит до commit),
	// именно ЭТОТ вызов на стороне брокера бампает producer epoch и
	// принудительно абортит зависшую транзакцию (KIP-98 fencing) —
	// синхронно, до того, как Begin() вернёт управление. Java делает
	// РОВНО то же самое одним явным вызовом producer.initTransactions()
	// (обычно один раз при старте приложения, до цикла обработки).
	if err := session.Begin(); err != nil {
		log.Fatalf("session.Begin: %v", err)
	}

	process := newCppProducerFn()

	var input []*kgo.Record
	deadline := time.Now().Add(30 * time.Second)
	for len(input) < n && time.Now().Before(deadline) {
		pollCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		fetches := session.PollFetches(pollCtx)
		cancel()
		fetches.EachRecord(func(r *kgo.Record) { input = append(input, r) })
	}
	if len(input) < n {
		log.Fatalf("[cpp-attempt] не набрал ожидаемое число входных записей: %d из %d (за 30с) — группа=%s топик=%s", len(input), n, group, inputTopic)
	}
	fmt.Printf("[cpp-attempt txn=%s] вычитано из %s: %d записей\n", txnID, inputTopic, len(input))

	outs := make([]*kgo.Record, 0, len(input))
	for _, r := range input {
		outs = append(outs, &kgo.Record{Topic: outputTopic, Key: r.Key, Value: []byte(process(string(r.Value)))})
	}
	res := session.ProduceSync(context.Background(), outs...)
	if err := res.FirstErr(); err != nil {
		log.Fatalf("session.ProduceSync: %v", err)
	}
	fmt.Printf("[cpp-attempt txn=%s] обработано и записано в %s: %d записей (ФИЗИЧЕСКИ на диске, транзакция ещё ОТКРЫТА — офсет %s ещё НЕ закоммичен)\n",
		txnID, outputTopic, len(outs), inputTopic)

	if pause > 0 {
		fmt.Printf("%s n=%d\n", readyMarker, len(outs))
		time.Sleep(pause)
	}

	committed, err := session.End(context.Background(), kgo.TryCommit)
	if err != nil {
		log.Fatalf("session.End: %v", err)
	}
	fmt.Printf("[cpp-attempt txn=%s] session.End(TryCommit) -> committed=%v — output И committed-офсет input продвинуты АТОМАРНО одной транзакцией\n", txnID, committed)
}

// cppVerify читает output читает обоими isolation level + проверяет
// committed-офсет consumer-группы на input. label — префикс для вывода
// (например "после-kill-до-повтора" / "после-повтора"), чтобы отчёт мог
// сопоставить два снимка состояния до/после восстановления.
func cppVerify(seeds []string, group, inputTopic, outputTopic, label string, expectCommittedOutput int64, expectGroupOffset int64) {
	committedCount := int64(consumeIsolation(seeds, outputTopic, kgo.ReadCommitted(), 5*time.Second))
	uncommittedCount := int64(consumeIsolation(seeds, outputTopic, kgo.ReadUncommitted(), 5*time.Second))
	groupOffset := groupCommittedTotal(seeds, group, inputTopic)

	fmt.Printf("[cpp-verify %s] output read_committed=%d read_uncommitted=%d(физически) input-group(%s) committed-offset=%d\n",
		label, committedCount, uncommittedCount, group, groupOffset)

	if committedCount != expectCommittedOutput {
		log.Fatalf("[cpp-verify %s] РАСХОЖДЕНИЕ: output read_committed=%d, ожидалось %d", label, committedCount, expectCommittedOutput)
	}
	if groupOffset != expectGroupOffset {
		log.Fatalf("[cpp-verify %s] РАСХОЖДЕНИЕ: committed-offset группы %s на %s = %d, ожидалось %d — обнаружено ЧАСТИЧНОЕ состояние (нарушена атомарность consume-process-produce)",
			label, group, inputTopic, groupOffset, expectGroupOffset)
	}
	fmt.Printf("[cpp-verify %s] OK: output read_committed == ожидалось (%d), committed-offset группы == ожидалось (%d) — частичного состояния нет\n",
		label, committedCount, groupOffset)
}

// cppInspectOutputSamples — дополнительная диагностика: печатает первые
// значения из output read_uncommitted, чтобы в отчёте было видно ФИЗИЧЕСКОЕ
// присутствие processed:-записей от убитой (абортнутой) попытки наряду с
// записями от успешного повтора — не только счётчик, а действительно разные
// значения под одним и тем же ключом (доказательство дублирования на физическом
// уровне при полном отсутствии дублирования на логическом).
func cppInspectOutputSamples(seeds []string, topic string, limit int) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(seeds...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.FetchIsolationLevel(kgo.ReadUncommitted()),
	)
	if err != nil {
		log.Fatalf("kgo.NewClient (cpp-inspect): %v", err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	printed := 0
	for printed < limit {
		pollCtx, pcancel := context.WithTimeout(ctx, 3*time.Second)
		fetches := cl.PollFetches(pollCtx)
		pcancel()
		if ctx.Err() != nil {
			break
		}
		stop := false
		fetches.EachRecord(func(r *kgo.Record) {
			if printed >= limit {
				stop = true
				return
			}
			fmt.Printf("  [read_uncommitted] partition=%d offset=%d key=%s value=%s\n", r.Partition, r.Offset, string(r.Key), string(r.Value))
			printed++
		})
		if stop || fetches.Empty() {
			break
		}
	}
}
