package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// newProducer собирает клиент-продюсер с явно заданными acks/идемпотентностью/
// таймаутами — все параметры продюсера в этом стенде управляются здесь, а не
// дефолтами библиотеки, потому что сам смысл сценариев — показать РАЗНИЦУ
// между настройками.
func newProducer(seeds []string, acks kgo.Acks, idempotent bool, recordRetries int, reqTimeout time.Duration) *kgo.Client {
	opts := []kgo.Opt{
		kgo.SeedBrokers(seeds...),
		kgo.RequiredAcks(acks),
		kgo.ProduceRequestTimeout(reqTimeout),
	}
	if !idempotent {
		opts = append(opts, kgo.DisableIdempotentWrite())
	}
	if recordRetries >= 0 {
		opts = append(opts, kgo.RecordRetries(recordRetries))
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		log.Fatalf("kgo.NewClient (producer): %v", err)
	}
	return cl
}

func acksFromString(s string) kgo.Acks {
	switch s {
	case "0":
		return kgo.NoAck()
	case "1":
		return kgo.LeaderAck()
	case "all", "-1":
		return kgo.AllISRAcks()
	default:
		log.Fatalf("неизвестное значение -acks=%s (ожидается 0|1|all)", s)
		return kgo.AllISRAcks()
	}
}

type produceOutcome struct {
	index     int
	value     string
	partition int32
	offset    int64
	err       error
	elapsed   time.Duration
}

// produceSequential шлёт n записей ПОСЛЕДОВАТЕЛЬНО (ждём ответ на запись N,
// прежде чем шлём N+1) — так каждая запись сама себе замер латентности, и
// порядок отправки/подтверждения строго детерминирован. Именно этот режим
// нужен для broker-kill демо: мы точно знаем, какие индексы УСПЕЛИ
// подтвердиться до момента, когда мы (или host-скрипт) убили брокер.
//
// stdout флашится построчно (fmt.Println сам по себе небуферизован для
// os.Stdout в этой программе), чтобы оркестрирующий bash-скрипт видел живой
// прогресс и мог скоординировать docker kill по факту вывода, а не по сну
// вслепую.
func produceSequential(cl *kgo.Client, topic string, n int, valuePrefix string, delay time.Duration) []produceOutcome {
	out := make([]produceOutcome, 0, n)
	for i := 0; i < n; i++ {
		if delay > 0 && i > 0 {
			time.Sleep(delay)
		}
		value := fmt.Sprintf("%s-%d", valuePrefix, i)
		rec := &kgo.Record{Topic: topic, Value: []byte(value)}
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		res := cl.ProduceSync(ctx, rec)
		cancel()
		elapsed := time.Since(start)
		r, err := res.First()
		o := produceOutcome{index: i, value: value, elapsed: elapsed, err: err}
		if err == nil {
			o.partition = r.Partition
			o.offset = r.Offset
			fmt.Printf("[produce] i=%d value=%s -> partition=%d offset=%d (%s)\n", i, value, r.Partition, r.Offset, elapsed)
		} else {
			fmt.Printf("[produce] i=%d value=%s -> ОШИБКА: %v (%s)\n", i, value, err, elapsed)
		}
		out = append(out, o)
		if err != nil {
			// дальше продолжать бессмысленно только если это фатальная ошибка
			// (брокер недоступен для этого топика вовсе); NOT_ENOUGH_REPLICAS
			// и подобные — это ожидаемый ИСХОД сценария, не баг стенда, поэтому
			// не прерываем цикл автоматически — решение прерывать/нет принимает
			// вызывающий сценарий.
		}
	}
	return out
}

// produceFireAndForget шлёт n записей АСИНХРОННО (не дожидаясь ответа перед
// следующей отправкой — в отличие от produceSequential) и даёт коллбэкам
// collectFor времени долететь, после чего перестаёт ждать (не Flush!). Это
// сознательно смоделированная "плохая" практика: приложение считает запись
// подтверждённой, как только callback сообщил успех, не дожидаясь
// репликации (acks=1) — и именно так выглядит наибольший риск потери
// неотреплицированного хвоста при падении лидера ровно в этом окне: пока
// producer шлёт пачку без бэкпрешера, несколько записей могут быть
// физически на диске лидера и уже подтверждены клиенту, но ещё не
// прочитаны фетчем фолловера, когда лидер падает.
//
// Возвращает индексы, чей callback успел сообщить успех ДО того, как истёк
// collectFor (остальные — "судьба неизвестна", как и было бы у реального
// упавшего вместе с брокером процесса).
func produceFireAndForget(cl *kgo.Client, topic string, n int, valuePrefix string, collectFor time.Duration, delay time.Duration) (acked []int, failed []int) {
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		if delay > 0 && i > 0 {
			time.Sleep(delay)
		}
		idx := i
		value := fmt.Sprintf("%s-%d", valuePrefix, idx)
		rec := &kgo.Record{Topic: topic, Value: []byte(value)}
		cl.Produce(context.Background(), rec, func(r *kgo.Record, err error) {
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				acked = append(acked, idx)
			} else {
				failed = append(failed, idx)
			}
		})
	}
	fmt.Printf("[produce-ff] выставлено %d записей асинхронно (callback), собираю ответы %s...\n", n, collectFor)
	time.Sleep(collectFor)
	mu.Lock()
	defer mu.Unlock()
	return append([]int(nil), acked...), append([]int(nil), failed...)
}

// classifyProduceErr сортирует ошибку продюсинга в одну из содержательных
// категорий, которые нам важны для отчёта: явный NOT_ENOUGH_REPLICAS
// (кластер осознанно отверг запись, зная, что ISR мал) vs обобщённый таймаут
// (клиент просто не дождался ответа — не обязательно значит то же самое, см.
// content-note про совмещённый broker+controller кворум) vs успех vs прочее.
func classifyProduceErr(err error) string {
	if err == nil {
		return "OK"
	}
	if errors.Is(err, kerr.NotEnoughReplicas) {
		return "NOT_ENOUGH_REPLICAS"
	}
	if errors.Is(err, kerr.NotEnoughReplicasAfterAppend) {
		return "NOT_ENOUGH_REPLICAS_AFTER_APPEND"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "CLIENT_TIMEOUT"
	}
	return fmt.Sprintf("ДРУГАЯ ОШИБКА: %v", err)
}
