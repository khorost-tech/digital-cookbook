package main

import (
	"context"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// runRabbit: publisher confirms. Брокер подтверждает КАЖДУЮ публикацию (ack/nack) — это
// предпочтительный механизм надёжной публикации в RabbitMQ. AMQP-транзакции
// (tx.select/tx.commit) тоже есть, но дороги: синхронный round-trip и блокировка канала на
// каждый commit, поэтому в проде их почти не используют — confirms дают ту же гарантию дешевле.
func runRabbit() {
	conn, err := amqp.Dial(envOr("RABBIT_URL", "amqp://guest:guest@localhost:5673/"))
	if err != nil {
		fmt.Println("rabbit dial:", err)
		return
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		fmt.Println("channel:", err)
		return
	}
	defer ch.Close()

	q, _ := ch.QueueDeclare("tx-demo", true, false, false, false, nil)

	// включаем confirm mode на канале
	if err := ch.Confirm(false); err != nil {
		fmt.Println("confirm mode:", err)
		return
	}
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 200))

	const n = 100
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < n; i++ {
		_ = ch.PublishWithContext(ctx, "", q.Name, false, false,
			amqp.Publishing{Body: []byte(fmt.Sprintf("msg-%d", i))})
	}
	acked, nacked := 0, 0
	for i := 0; i < n; i++ {
		c := <-confirms
		if c.Ack {
			acked++
		} else {
			nacked++
		}
	}
	fmt.Printf("rabbit (publisher confirms): опубликовано %d, ack=%d nack=%d — каждая публикация подтверждена брокером\n",
		n, acked, nacked)
}
