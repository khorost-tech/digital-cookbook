package main

import (
	"context"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	qPersistent = "tx-demo"           // durable-очередь + persistent-сообщения
	qTransient  = "tx-demo-transient" // durable-очередь + transient-сообщения (контраст)
	nMsgs       = 100
)

// rabbitDial подключается и объявляет обе durable-очереди (durable=true).
func rabbitDial() (*amqp.Connection, *amqp.Channel, error) {
	conn, err := amqp.Dial(envOr("RABBIT_URL", "amqp://guest:guest@localhost:5673/"))
	if err != nil {
		return nil, nil, fmt.Errorf("dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("channel: %w", err)
	}
	for _, q := range []string{qPersistent, qTransient} {
		if _, err := ch.QueueDeclare(q, true, false, false, false, nil); err != nil {
			_ = ch.Close()
			_ = conn.Close()
			return nil, nil, fmt.Errorf("declare %s: %w", q, err)
		}
	}
	return conn, ch, nil
}

// runRabbit: publisher confirms. Брокер подтверждает КАЖДУЮ публикацию (ack/nack) — это
// предпочтительный механизм надёжной публикации в RabbitMQ. AMQP-транзакции
// (tx.select/tx.commit) дают ту же гарантию, но дороже: синхронный round-trip и блокировка
// канала на каждый commit, поэтому в проде их почти не используют.
//
// Ключевой нюанс, который демо показывает явно: durable-очередь и persistent-сообщение — это
// ДВЕ РАЗНЫЕ настройки. durable=true у очереди означает, что рестарт переживёт сама очередь.
// Чтобы рестарт пережили СООБЩЕНИЯ, публиковать надо с DeliveryMode: amqp.Persistent. Без него
// ack подтверждает только то, что брокер сообщение принял, — на диск оно не ляжет и при
// рестарте исчезнет, хотя очередь останется.
//
// Проверить живьём (рестарт — через stop/start: `docker compose restart` возвращается раньше,
// чем брокер реально упал, и проверка опросит старый инстанс — см. README):
//
//	go run . rabbit
//	docker compose stop rabbitmq && docker compose start rabbitmq
//	go run . rabbit-verify
func runRabbit() {
	conn, ch, err := rabbitDial()
	if err != nil {
		fmt.Println("rabbit:", err)
		return
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = ch.Close() }()

	// Чистим прошлый прогон — числа должны быть воспроизводимы.
	for _, q := range []string{qPersistent, qTransient} {
		if _, err := ch.QueuePurge(q, false); err != nil {
			fmt.Println("purge:", err)
			return
		}
	}

	if err := ch.Confirm(false); err != nil {
		fmt.Println("confirm mode:", err)
		return
	}
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 2*nMsgs))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ackP, nackP, err := publishN(ctx, ch, confirms, qPersistent, amqp.Persistent)
	if err != nil {
		fmt.Println("publish persistent:", err)
		return
	}
	ackT, nackT, err := publishN(ctx, ch, confirms, qTransient, amqp.Transient)
	if err != nil {
		fmt.Println("publish transient:", err)
		return
	}

	fmt.Println("rabbit (publisher confirms):")
	fmt.Printf("  %-18s (persistent): опубликовано %d, ack=%d nack=%d\n", qPersistent, nMsgs, ackP, nackP)
	fmt.Printf("  %-18s (transient):  опубликовано %d, ack=%d nack=%d\n", qTransient, nMsgs, ackT, nackT)
	fmt.Printf("Подтверждены все %d публикаций. Но ack transient-сообщения означает лишь «брокер принял»,\n", ackP+ackT)
	fmt.Println("а не «переживёт рестарт». Проверка: docker compose restart rabbitmq && go run . rabbit-verify")
}

// publishN публикует nMsgs сообщений с заданным delivery mode и дожидается ровно nMsgs
// подтверждений. Ошибку публикации не глушим: в демо про надёжную доставку это было бы иронично.
func publishN(ctx context.Context, ch *amqp.Channel, confirms <-chan amqp.Confirmation,
	queue string, mode uint8,
) (acked, nacked int, err error) {
	for i := 0; i < nMsgs; i++ {
		if err := ch.PublishWithContext(ctx, "", queue, false, false, amqp.Publishing{
			DeliveryMode: mode, // amqp.Persistent (2) — на диск; amqp.Transient (1) — только в памяти
			Body:         []byte(fmt.Sprintf("msg-%d", i)),
		}); err != nil {
			return acked, nacked, fmt.Errorf("publish %d в %s: %w", i, queue, err)
		}
	}
	for i := 0; i < nMsgs; i++ {
		select {
		case c := <-confirms:
			if c.Ack {
				acked++
			} else {
				nacked++
			}
		case <-ctx.Done():
			return acked, nacked, fmt.Errorf("ждали %d подтверждений от %s, получили %d: %w",
				nMsgs, queue, acked+nacked, ctx.Err())
		}
	}
	return acked, nacked, nil
}

// runRabbitVerify: сколько сообщений реально пережило рестарт брокера. Запускать ПОСЛЕ
// `docker compose restart rabbitmq`. Ожидаемо: persistent-очередь полна, transient — пуста,
// хотя ack пришёл на обе и обе очереди durable.
func runRabbitVerify() {
	conn, ch, err := rabbitDial()
	if err != nil {
		fmt.Println("rabbit:", err)
		return
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = ch.Close() }()

	for _, q := range []string{qPersistent, qTransient} {
		// Повторный QueueDeclare с теми же параметрами идемпотентен и возвращает глубину очереди.
		st, err := ch.QueueDeclare(q, true, false, false, false, nil)
		if err != nil {
			fmt.Println("declare:", err)
			return
		}
		fmt.Printf("после рестарта: %-18s осталось %3d из %d\n", q, st.Messages, nMsgs)
	}
}
