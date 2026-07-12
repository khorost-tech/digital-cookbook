// jetstream-pull демонстрирует новый пакет jetstream (github.com/nats-io/
// nats.go/jetstream) — НЕ устаревший nc.JetStream(): создание/переиспользование
// stream, publish с MsgId (дедупликация), pull-consumer через Consume(),
// ручной ack. Запускать против nats/01-jetstream или nats/02-cluster.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"nats-cookbook-go/internal/natsconn"
)

const (
	streamName    = "ORDERS"
	streamSubject = "orders.>"
	pubSubject    = "orders.new"
	consumerName  = "proc"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc, err := natsconn.Connect("jetstream-pull")
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		natsconn.Shutdown(shutCtx, nc)
	}()

	// jetstream.New — единственная точка входа в новый API. В отличие от
	// устаревшего nc.JetStream() (пакет nats, JetStreamContext) новый пакет
	// jetstream принимает context.Context в каждом сетевом вызове и разделяет
	// управление stream/consumer (JetStreamManager) и публикацию/потребление
	// на явные интерфейсы — это и есть "новый API", о котором речь в статье.
	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("jetstream.New: %v", err)
	}

	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{streamSubject},
		Storage:  jetstream.FileStorage,
	})
	if err != nil {
		log.Fatalf("create stream: %v", err)
	}
	log.Printf("stream %s готов (subjects: %v)", streamName, stream.CachedInfo().Config.Subjects)

	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:   consumerName,
		AckPolicy: jetstream.AckExplicitPolicy,
		// AckWait — сколько сервер ждёт ack, прежде чем считать сообщение
		// недоставленным и переслать его снова (redelivery). MaxDeliver —
		// сколько раз повторять, прежде чем сдаться и оставить сообщение
		// в stream без дальнейших попыток.
		AckWait:       10 * time.Second,
		MaxDeliver:    5,
		FilterSubject: streamSubject,
	})
	if err != nil {
		log.Fatalf("create consumer: %v", err)
	}
	log.Printf("pull-consumer %s готов (ack: explicit, AckWait: 10s, MaxDeliver: 5)", consumerName)

	if err := publishWithDedup(ctx, js); err != nil {
		log.Fatalf("publish: %v", err)
	}

	if err := consumeAndAck(ctx, consumer); err != nil {
		log.Fatalf("consume: %v", err)
	}
}

// publishWithDedup публикует одно и то же сообщение дважды с одинаковым
// MsgId. Второй Publish не создаёт вторую запись в stream — JetStream
// отбрасывает дубликат в пределах окна дедупликации (Duplicate Window,
// по умолчанию 2 минуты). Это защита от повторной публикации при обрыве
// подтверждения на стороне publisher'а (отправили, не дождались ack по сети,
// повторили) — не путать с идемпотентностью обработки на стороне consumer.
func publishWithDedup(ctx context.Context, js jetstream.JetStream) error {
	payload := []byte(`{"id":1}`)

	ack1, err := js.Publish(ctx, pubSubject, payload, jetstream.WithMsgID("order-1"))
	if err != nil {
		return fmt.Errorf("первая публикация: %w", err)
	}
	log.Printf("publish #1: seq=%d, duplicate=%v", ack1.Sequence, ack1.Duplicate)

	ack2, err := js.Publish(ctx, pubSubject, payload, jetstream.WithMsgID("order-1"))
	if err != nil {
		return fmt.Errorf("повторная публикация: %w", err)
	}
	log.Printf("publish #2 (тот же MsgId): seq=%d, duplicate=%v", ack2.Sequence, ack2.Duplicate)

	if !ack2.Duplicate {
		log.Printf("предупреждение: сервер не пометил повтор как duplicate — проверьте окно дедупа стрима")
	}
	return nil
}

// consumeAndAck читает через pull-consumer с Fetch — забирает фиксированный
// батч и обрабатывает его синхронно. Для долгоживущего воркера уместнее
// Consume() с колбэком (см. комментарий ниже), но Fetch проще для
// одноразового демо-прогона и явно показывает, что клиент сам управляет
// темпом чтения — в этом и разница pull vs push.
func consumeAndAck(ctx context.Context, consumer jetstream.Consumer) error {
	batch, err := consumer.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	received := 0
	for msg := range batch.Messages() {
		received++
		meta, _ := msg.Metadata()
		log.Printf("получено: %s: %s (попытка доставки: %d)", msg.Subject(), string(msg.Data()), meta.NumDelivered)

		// Ack (а не DoubleAck) достаточно в обычном пути: клиент не ждёт
		// подтверждения от сервера, что ack принят. DoubleAck пригодился бы,
		// если приложению критично убедиться, что именно этот ack долетел
		// (например, перед необратимым побочным эффектом).
		if err := msg.Ack(); err != nil {
			log.Printf("ack: %v", err)
		}
	}
	if err := batch.Error(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("batch: %w", err)
	}
	if received == 0 {
		return errors.New("не получено ни одного сообщения за FetchMaxWait")
	}

	// Consume() — альтернатива для долгоживущего сервиса: регистрирует
	// колбэк, который сервер вызывает на каждое новое сообщение,
	// с автоматическим управлением pull-запросами под капотом
	// (client приходит за новым батчем сам, приложению это не видно).
	//
	//   cc, err := consumer.Consume(func(msg jetstream.Msg) {
	//       // обработка + msg.Ack()
	//   })
	//   defer cc.Stop()
	//
	// В этом демо не используется, чтобы процесс завершался предсказуемо
	// после одного сообщения, а не работал бесконечно.

	return nil
}
