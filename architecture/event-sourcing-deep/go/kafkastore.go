package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// kafkaEventsTopic — топик нагрузочного теста append/point-read. Один на
// весь прогон 01-store.sh: A и B работают с одними и теми же данными.
const kafkaEventsTopic = "es-events"

// kafkaPollIdle — сколько ждать короткий опрос, прежде чем считать, что
// в топике/партиции больше нет буферизованных записей. У kgo.PollFetches
// нет сигнала «конец лога»: вызов блокируется до появления данных или
// истечения переданного контекста. Поэтому «конец» определяем через
// короткий контекст на КАЖДЫЙ опрос отдельно от общего дедлайна команды:
// иначе элапсед чтения включал бы весь оставшийся -duration, а не
// реальное время чтения.
const kafkaPollIdle = 250 * time.Millisecond

// NewKafkaProducerClient — клиент для продюсера. AllowAutoTopicCreation
// нужен для topic'ов catch-up-демо: они создаются с уникальным именем на
// каждый прогон -mode catchup-compare, заранее их никто не создаёт.
func NewKafkaProducerClient(brokers string, defaultTopic string) (*kgo.Client, error) {
	return kgo.NewClient(
		kgo.SeedBrokers(brokers),
		kgo.DefaultProduceTopic(defaultTopic),
		kgo.AllowAutoTopicCreation(),
	)
}

// NewKafkaConsumerClient — клиент для чтения топика С НАЧАЛА, без группы:
// групповая балансировка тут не нужна и даже мешает — читаем всё сами,
// от начала партиции.
func NewKafkaConsumerClient(brokers string, topic string) (*kgo.Client, error) {
	return kgo.NewClient(
		kgo.SeedBrokers(brokers),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.AllowAutoTopicCreation(),
	)
}

// AppendKafka — запись события в топик. Обратите внимание, чего здесь НЕТ:
// параметра expectedVersion. Producer API условной записи по версии ключа не
// предоставляет, поэтому оптимистичная блокировка на уровне стрима на Kafka
// не строится. Это не мнение — это отсутствующий параметр.
func AppendKafka(ctx context.Context, cl *kgo.Client, topic, streamID string, e Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	rec := &kgo.Record{
		Topic: topic,
		Key:   []byte(streamID),
		Value: payload,
	}
	return cl.ProduceSync(ctx, rec).FirstErr()
}

// consumeAllAvailable читает всё, что сейчас буферизовано в топике, пока
// короткий опрос (kafkaPollIdle) не истечёт без новых записей. Возвращает
// прочитанные записи и время до ПОСЛЕДНЕЙ полученной записи — то есть без
// хвоста ожидания, которым мы отличаем «данные кончились» от «данных пока
// нет». Хвост в измеряемое время не включается умышленно: иначе цена
// чтения оказалась бы кафкаPollIdle-зависимой величиной, а не свойством
// самого чтения.
func consumeAllAvailable(parent context.Context, cl *kgo.Client) ([]*kgo.Record, time.Duration, error) {
	var out []*kgo.Record
	start := time.Now()
	last := start
	for {
		if parent.Err() != nil {
			return out, last.Sub(start), parent.Err()
		}
		pctx, cancel := context.WithTimeout(parent, kafkaPollIdle)
		fetches := cl.PollFetches(pctx)
		cancel()
		if fetches.IsClientClosed() {
			break
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			onlyTimeout := true
			for _, e := range errs {
				if !errors.Is(e.Err, context.DeadlineExceeded) {
					onlyTimeout = false
				}
			}
			if onlyTimeout {
				break // короткий опрос истёк без данных — партиция вычитана
			}
			return out, last.Sub(start), fmt.Errorf("fetch: %v", errs[0].Err)
		}
		got := false
		fetches.EachRecord(func(r *kgo.Record) {
			got = true
			out = append(out, r)
		})
		if got {
			last = time.Now()
		} else {
			break
		}
	}
	return out, last.Sub(start), nil
}

// consumeOne блокируется до первой новой записи или истечения ctx. Общий
// (не короткий) контекст здесь уместен: мы ХОТИМ ждать реальное появление
// записи, это и есть измеряемая величина «живой» задержки.
func consumeOne(ctx context.Context, cl *kgo.Client) (*kgo.Record, error) {
	for {
		fetches := cl.PollFetches(ctx)
		if fetches.IsClientClosed() {
			return nil, fmt.Errorf("client closed")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			return nil, fmt.Errorf("fetch: %v", errs[0].Err)
		}
		var rec *kgo.Record
		fetches.EachRecord(func(r *kgo.Record) {
			if rec == nil {
				rec = r
			}
		})
		if rec != nil {
			return rec, nil
		}
	}
}

// ReadStreamKafka — точечное чтение одного агрегата. В Postgres и KurrentDB
// это индексный доступ по стриму; здесь приходится читать топик целиком и
// отбрасывать чужие ключи. Функция возвращает и число ПРОСМОТРЕННЫХ записей,
// чтобы цена была видна числом, а не на словах.
func ReadStreamKafka(ctx context.Context, cl *kgo.Client, streamID string) (Tank, int, int, time.Duration, error) {
	var tank Tank
	scanned, matched := 0, 0

	records, elapsed, err := consumeAllAvailable(ctx, cl)
	if err != nil {
		return tank, scanned, matched, elapsed, err
	}
	for _, r := range records {
		scanned++
		if string(r.Key) != streamID {
			continue
		}
		var e Event
		if json.Unmarshal(r.Value, &e) == nil {
			tank = tank.Apply(e)
			matched++
		}
	}
	return tank, scanned, matched, elapsed, nil
}
