// Доступ к тому же stream через обычный AMQP 0.9.1:
// x-queue-type=stream + ОБЯЗАТЕЛЬНЫЕ basic.qos (prefetch) и x-stream-offset.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const queueName = "events-amqp"

func main() {
	conn, err := amqp.Dial("amqp://guest:guest@localhost:5672/")
	fatal(err)
	defer conn.Close()
	ch, err := conn.Channel()
	fatal(err)
	defer ch.Close()

	// stream объявляется как очередь особого типа
	_, err = ch.QueueDeclare(queueName, true, false, false, false,
		amqp.Table{"x-queue-type": "stream"})
	fatal(err)

	ctx := context.Background()
	const n = 20
	for i := 0; i < n; i++ {
		fatal(ch.PublishWithContext(ctx, "", queueName, false, false,
			amqp.Publishing{Body: []byte(fmt.Sprintf("event-%d", i))}))
	}

	// Для стрима через AMQP ОБЯЗАТЕЛЬНЫ prefetch (basic.qos) и стартовый offset,
	// иначе потребитель ничего не получит.
	fatal(ch.Qos(10, 0, false))
	msgs, err := ch.Consume(queueName, "", false, false, false, false,
		amqp.Table{"x-stream-offset": "first"}) // "last", "next", число или timestamp
	fatal(err)

	got := 0
	timeout := time.After(8 * time.Second)
	for got < n {
		select {
		case d := <-msgs:
			got++
			d.Ack(false)
		case <-timeout:
			log.Fatalf("timeout, got %d/%d", got, n)
		}
	}
	fmt.Printf("AMQP: consumed %d from x-queue-type=stream (x-stream-offset=first)\n", got)
}

func fatal(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
