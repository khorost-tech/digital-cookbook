// Super stream: партиционирование на N обычных стримов + hash-роутинг по ключу.
package main

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/message"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
)

const superName = "orders"

func main() {
	env, err := stream.NewEnvironment(stream.NewEnvironmentOptions().
		SetHost("localhost").SetPort(5552).
		SetUser("guest").SetPassword("guest"))
	fatal(err)
	defer env.Close()

	// super stream из 3 партиций: создаст orders-0, orders-1, orders-2
	fatal(env.DeclareSuperStream(superName, stream.NewPartitionsOptions(3)))
	parts, err := env.QueryPartitions(superName)
	fatal(err)
	fmt.Printf("partitions (%d): %v\n", len(parts), parts)

	// роутинг по application property "key": сообщения одного ключа — в одну партицию
	routing := stream.NewHashRoutingStrategy(func(m message.StreamMessage) string {
		return m.GetApplicationProperties()["key"].(string)
	})
	p, err := env.NewSuperStreamProducer(superName, stream.NewSuperStreamProducerOptions(routing))
	fatal(err)

	const n = 60
	for i := 0; i < n; i++ {
		msg := amqp.NewMessage([]byte(fmt.Sprintf("order-%d", i)))
		msg.ApplicationProperties = map[string]any{"key": fmt.Sprintf("user-%d", i%10)}
		fatal(p.Send(msg))
	}
	time.Sleep(2 * time.Second) // дать батчам флашнуться
	fatal(p.Close())
	fmt.Printf("produced %d в super stream\n", n)

	// super consumer читает со всех партиций сразу
	var got int64
	done := make(chan struct{})
	h := func(_ stream.ConsumerContext, _ *amqp.Message) {
		if atomic.AddInt64(&got, 1) == int64(n) {
			close(done)
		}
	}
	_, err = env.NewSuperStreamConsumer(superName, h, stream.NewSuperStreamConsumerOptions().
		SetOffset(stream.OffsetSpecification{}.First()))
	fatal(err)
	select {
	case <-done:
		fmt.Printf("super-consumed all %d across %d partitions\n", n, len(parts))
	case <-time.After(12 * time.Second):
		log.Fatalf("не дождались: got %d", atomic.LoadInt64(&got))
	}
}

func fatal(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
