package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	amqp "github.com/rabbitmq/amqp091-go"
	"rabbitmq-ha-demo/internal/rabbit"
)

func main() {
	urls := flag.String("urls", "amqp://demo:demo@localhost:5672/,amqp://demo:demo@localhost:5673/,amqp://demo:demo@localhost:5674/", "AMQP узлы кластера через запятую")
	queue := flag.String("queue", "demo.orders", "имя очереди")
	prefetch := flag.Int("prefetch", 10, "QoS prefetch")
	autoAck := flag.Bool("autoack", false, "auto ack (демонстрация потери)")
	nack := flag.Bool("nack", false, "симулировать падение обработчика: получить сообщение, закрыть канал без ack — quorum-очередь увеличивает x-delivery-count при каждом reconnect; после delivery-limit=5 повторов сообщение уходит в DLX (poison message protection)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	endpoints := strings.Split(*urls, ",")
	received := 0
	conns := rabbit.Connect(ctx, endpoints)
	// Failover-цикл: rabbit.Connect возвращает канал соединений.
	// При разрыве (например, убили ноду-лидер кластера) функция автоматически
	// переподключается и отдаёт новое *amqp.Connection — цикл продолжается
	// без каких-либо изменений в бизнес-логике.
	for conn := range conns {
		if err := consume(ctx, conn, *queue, *prefetch, *autoAck, *nack, &received); err != nil {
			log.Printf("consume loop ended: %v (переподключаемся)", err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	log.Printf("shutdown: total received=%d", received)
	_ = os.Stdout.Sync()
}

func consume(ctx context.Context, conn *amqp.Connection, queue string, prefetch int, autoAck bool, nack bool, received *int) error {
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()

	// Quorum-очередь: реплицируется на большинство нод кластера (quorum),
	// данные не теряются при падении одной ноды. QueueDeclare идемпотентна —
	// если очередь уже существует с теми же параметрами, ошибки не будет;
	// это позволяет безопасно вызывать её при каждом переподключении.
	if _, err := ch.QueueDeclare(queue, true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); err != nil {
		return err
	}
	// Prefetch (QoS): брокер отправит консьюмеру не более prefetch
	// неподтверждённых сообщений одновременно. Это предотвращает
	// переполнение памяти при медленной обработке и равномерно
	// распределяет нагрузку между несколькими экземплярами консьюмера.
	// В режиме -nack prefetch=1: важно закрывать канал после каждого
	// сообщения по одному, чтобы x-delivery-count рос равномерно по всем
	// сообщениям (при prefetch>1 несколько сообщений становятся unacked разом).
	effectivePrefetch := prefetch
	if nack {
		effectivePrefetch = 1
	}
	if err := ch.Qos(effectivePrefetch, 0, false); err != nil {
		return err
	}
	deliveries, err := ch.Consume(queue, "", autoAck, false, false, false, nil)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return amqp.ErrClosed
			}
			*received++
			// Manual ack (autoAck=false) — гарантия at-least-once:
			// сообщение считается обработанным только после явного Ack.
			// При autoAck=true брокер удаляет сообщение сразу при доставке —
			// если процесс упадёт до обработки, данные потеряются.
			if !autoAck {
				if nack {
					deliveryCount := 0
					if v, ok := d.Headers["x-delivery-count"]; ok && v != nil {
						if n, ok := v.(int64); ok {
							deliveryCount = int(n)
						} else if n, ok := v.(int32); ok {
							deliveryCount = int(n)
						}
					}
					log.Printf("got %q (доставка #%d) — симулируем падение обработчика, соединение закроется", d.Body, deliveryCount+1)
					// Режим poison-message: закрываем СОЕДИНЕНИЕ без ack.
					// Quorum-очередь видит незакрытое сообщение и увеличивает
					// x-delivery-count при следующей доставке. Это происходит только при
					// закрытии канала/соединения, а не при Nack+requeue на том же канале.
					// После delivery-limit=5 таких циклов сообщение автоматически
					// уходит в DLX → DLQ. Именно так работает poison message protection.
					conn.Close() // conn доступен через замыкание; закрытие вызовет NotifyClose → reconnect
					return amqp.ErrClosed
				} else {
					log.Printf("got %q (total=%d)", d.Body, *received)
					if err := d.Ack(false); err != nil {
						log.Printf("ack error: %v", err)
					}
				}
			}
		}
	}
}
