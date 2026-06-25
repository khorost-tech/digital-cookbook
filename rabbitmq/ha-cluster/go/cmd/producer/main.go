package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"strings"
	"syscall"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"rabbitmq-ha-demo/internal/rabbit"
)

func main() {
	urls := flag.String("urls", "amqp://demo:demo@localhost:5672/,amqp://demo:demo@localhost:5673/,amqp://demo:demo@localhost:5674/", "AMQP узлы кластера через запятую")
	n := flag.Int("n", 100, "число сообщений")
	useConfirms := flag.Bool("confirms", true, "publisher confirms")
	queue := flag.String("queue", "demo.orders", "имя очереди (= routing key)")
	flag.Parse()

	endpoints := strings.Split(*urls, ",")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sent := 0 // сколько успешно отправлено (и подтверждено, если confirms=true)
	conns := rabbit.Connect(ctx, endpoints)
	for conn := range conns {
		done, err := publish(ctx, conn, *queue, *useConfirms, *n, &sent)
		conn.Close()
		if err != nil {
			log.Printf("публикация прервана: %v — переподключаемся к выжившему узлу", err)
		}
		if done || ctx.Err() != nil {
			break
		}
	}
	log.Printf("итог: запланировано=%d отправлено_и_подтверждено=%d (confirms=%v); при сбое узла неподтверждённые ПЕРЕОТПРАВЛЯЮТСЯ → возможны дубликаты (at-least-once, нужна идемпотентность)", *n, sent, *useConfirms)
}

// publish публикует с текущего индекса *sent до n. Возвращает done=true, если все n
// успешно отправлены. При обрыве соединения возвращает err, НЕ продвинув *sent для
// неподтверждённого сообщения — оно будет переотправлено на новом узле.
func publish(ctx context.Context, conn *amqp.Connection, queue string, useConfirms bool, n int, sent *int) (bool, error) {
	ch, err := conn.Channel()
	if err != nil {
		return false, err
	}
	defer ch.Close()

	if _, err := ch.QueueDeclare(queue, true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); err != nil {
		return false, err
	}
	if err := ch.QueueBind(queue, queue, "demo.orders", false, nil); err != nil {
		return false, err
	}
	if useConfirms {
		if err := ch.Confirm(false); err != nil {
			return false, err
		}
	}

	for *sent < n {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}
		body := fmt.Sprintf("order-%d", *sent)
		dc, err := ch.PublishWithDeferredConfirm("demo.orders", queue, false, false, amqp.Publishing{
			DeliveryMode: amqp.Persistent, // переживает рестарт
			ContentType:  "text/plain",
			Body:         []byte(body),
		})
		if err != nil {
			return false, err // соединение порвалось — *sent не двигаем, переотправим
		}
		if useConfirms {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			ok, err := dc.WaitContext(cctx)
			cancel()
			if err != nil || !ok {
				return false, fmt.Errorf("не подтверждено %s: %v", body, err)
			}
		}
		*sent++
		select {
		case <-time.After(50 * time.Millisecond): // пауза, чтобы успеть убить ноду вживую
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return true, nil
}
