// delayed-retry-demo демонстрирует нативный механизм отложенного redelivery (RabbitMQ 4.3).
//
// Выполняет N итераций: получает сообщение, закрывает соединение (implicit nack),
// затем ждёт следующей доставки и измеряет фактическую задержку.
// На последней итерации — Ack.
//
// Использование:
//
//	go run ./cmd/delayed-retry-demo -urls amqp://demo:demo@localhost:5672/ -queue demo.orders -n 3
package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func main() {
	url := flag.String("urls", "amqp://demo:demo@localhost:5672/", "AMQP URL")
	queue := flag.String("queue", "demo.orders", "Имя очереди")
	n := flag.Int("n", 3, "Число итераций implicit nack (затем ack)")
	flag.Parse()

	fmt.Printf("=== Native Delayed Retry Demo ===\n")
	fmt.Printf("Queue: %s, iterations (nack): %d\n\n", *queue, *n)

	var tPrev time.Time

	for iter := 0; iter <= *n; iter++ {
		conn, err := amqp.Dial(*url)
		if err != nil {
			log.Fatalf("iter %d: dial: %v", iter, err)
		}
		ch, err := conn.Channel()
		if err != nil {
			conn.Close()
			log.Fatalf("iter %d: channel: %v", iter, err)
		}
		_ = ch.Qos(1, 0, false)
		dels, err := ch.Consume(*queue, "", false, false, false, false, nil)
		if err != nil {
			conn.Close()
			log.Fatalf("iter %d: consume: %v", iter, err)
		}

		select {
		case d := <-dels:
			dc := int64(0)
			if v, ok := d.Headers["x-delivery-count"]; ok {
				dc, _ = v.(int64)
			}
			now := time.Now()

			if iter == 0 {
				fmt.Printf("[%s] Iter %d/%d: delivery_count=%d — FIRST DELIVERY\n",
					now.Format("15:04:05.000"), iter+1, *n+1, dc)
			} else {
				delay := now.Sub(tPrev).Round(time.Millisecond)
				// expected delay = min_delay * dc (dc уже обновлён брокером)
				expectedDelay := dc * 2000
				fmt.Printf("[%s] Iter %d/%d: delivery_count=%d — redelivery delay=%v (expected ~%dms)\n",
					now.Format("15:04:05.000"), iter+1, *n+1, dc, delay,
					expectedDelay)
			}

			tPrev = now

			if iter < *n {
				fmt.Printf("  -> Closing connection (implicit nack). Next retry in ~%dms\n", (dc+1)*2000)
				conn.Close()
			} else {
				_ = d.Ack(false)
				conn.Close()
				fmt.Printf("  -> Ack sent. Done.\n\nSUCCESS: native delayed retry demonstrated.\n")
				return
			}

		case <-time.After(30 * time.Second):
			conn.Close()
			fmt.Printf("TIMEOUT iter %d: no delivery in 30s\n", iter+1)
			return
		}
	}
}
