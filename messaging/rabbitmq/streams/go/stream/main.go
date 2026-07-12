// Native stream-протокол: объявление, producer с publish confirmations,
// consumer с First-offset и серверным offset tracking.
package main

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
)

const streamName = "events"

func main() {
	env, err := stream.NewEnvironment(stream.NewEnvironmentOptions().
		SetHost("localhost").SetPort(5552).
		SetUser("guest").SetPassword("guest"))
	fatal(err)
	defer env.Close()

	// объявляем stream с ретенцией по размеру (идемпотентно)
	fatal(env.DeclareStream(streamName, stream.NewStreamOptions().
		SetMaxLengthBytes(stream.ByteCapacity{}.MB(200))))

	const n = 100
	produce(env, n)
	consume(env, n)
}

func produce(env *stream.Environment, n int) {
	p, err := env.NewProducer(streamName, nil)
	fatal(err)

	// Send асинхронный и батчится: ждём publish confirmations, иначе часть
	// сообщений может не флашнуться до Close и потеряться.
	var confirmed int64
	done := make(chan struct{})
	go func() {
		for statuses := range p.NotifyPublishConfirmation() {
			for _, s := range statuses {
				if s.IsConfirmed() && atomic.AddInt64(&confirmed, 1) == int64(n) {
					close(done)
				}
			}
		}
	}()

	for i := 0; i < n; i++ {
		fatal(p.Send(amqp.NewMessage([]byte(fmt.Sprintf("event-%d", i)))))
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Fatalf("не дождались подтверждений: %d/%d", atomic.LoadInt64(&confirmed), n)
	}
	fatal(p.Close())
	fmt.Printf("produced+confirmed %d\n", n)
}

func consume(env *stream.Environment, n int) {
	var got int64
	done := make(chan struct{})
	handler := func(ctx stream.ConsumerContext, _ *amqp.Message) {
		c := atomic.AddInt64(&got, 1)
		// Сохраняем offset на сервере ПЕРИОДИЧЕСКИ, а не на каждое сообщение:
		// частый StoreOffset бьёт по производительности (см. README stream-клиента).
		if c%50 == 0 {
			_ = ctx.Consumer.StoreOffset()
		}
		if c == int64(n) {
			close(done)
		}
	}
	c, err := env.NewConsumer(streamName, handler, stream.NewConsumerOptions().
		SetConsumerName("worker"). // имя нужно для серверного offset tracking
		SetOffset(stream.OffsetSpecification{}.First()))
	fatal(err)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Fatalf("не дождались сообщений: %d/%d", atomic.LoadInt64(&got), n)
	}
	_ = c.StoreOffset()
	time.Sleep(300 * time.Millisecond)
	off, err := env.QueryOffset("worker", streamName)
	fatal(err)
	fmt.Printf("consumed %d from First; сохранённый server-side offset = %d\n", n, off)
	_ = c.Close()
}

func fatal(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
