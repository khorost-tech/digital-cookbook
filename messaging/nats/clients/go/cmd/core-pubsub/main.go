// core-pubsub демонстрирует Core NATS: async subscribe, sync subscribe и
// queue group на nats.go. Запускать против nats/00-core или nats/02-cluster
// (см. README.md в корне clients/go).
package main

import (
	"context"
	"log"
	"time"

	"github.com/nats-io/nats.go"

	"nats-cookbook-go/internal/natsconn"
)

func main() {
	nc, err := natsconn.Connect("core-pubsub")
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		natsconn.Shutdown(ctx, nc)
	}()

	// Async subscribe — сервер сам вызывает обработчик в отдельной горутине
	// на каждое входящее сообщение. Обычный выбор для долгоживущих подписчиков.
	subAsync, err := nc.Subscribe("orders.>", func(msg *nats.Msg) {
		log.Printf("[async] %s: %s", msg.Subject, string(msg.Data))
	})
	if err != nil {
		log.Fatalf("subscribe async: %v", err)
	}
	defer subAsync.Unsubscribe()

	// Sync subscribe — клиент сам забирает сообщения вызовом NextMsg,
	// без фонового обработчика. Полезно, когда обработка должна идти строго
	// последовательно в известном месте кода (например, в цикле теста или
	// в сценарии, где порядок важнее параллелизма).
	subSync, err := nc.SubscribeSync("orders.sync.>")
	if err != nil {
		log.Fatalf("subscribe sync: %v", err)
	}
	defer subSync.Unsubscribe()

	// Queue subscribe — несколько подписчиков с одинаковым именем группы
	// делят входящий поток: каждое сообщение достаётся ровно одному участнику.
	// Здесь поднимаем двух воркеров в одном процессе, чтобы демо было видно
	// одним запуском; на практике это отдельные экземпляры сервиса.
	queue := "workers"
	for i := 1; i <= 2; i++ {
		workerID := i
		sub, err := nc.QueueSubscribe("jobs.*", queue, func(msg *nats.Msg) {
			log.Printf("[queue worker-%d] %s: %s", workerID, msg.Subject, string(msg.Data))
		})
		if err != nil {
			log.Fatalf("queue subscribe: %v", err)
		}
		defer sub.Unsubscribe()
	}

	log.Println("core-pubsub: подписки активны, публикуем демо-сообщения")

	if err := nc.Publish("orders.eu.new", []byte(`{"id":1}`)); err != nil {
		log.Printf("publish: %v", err)
	}
	if err := nc.Publish("orders.sync.new", []byte(`{"id":2}`)); err != nil {
		log.Printf("publish: %v", err)
	}
	if msg, err := subSync.NextMsg(2 * time.Second); err != nil {
		log.Printf("sync next: %v", err)
	} else {
		log.Printf("[sync] %s: %s", msg.Subject, string(msg.Data))
	}

	for i := 0; i < 3; i++ {
		if err := nc.Publish("jobs.a", []byte("task")); err != nil {
			log.Printf("publish: %v", err)
		}
	}

	// Flush — дождаться, что все Publish выше реально ушли на сервер, прежде
	// чем дать обработчикам время отработать. Publish буферизует запись
	// на клиенте; без Flush (или ожидания ниже) процесс может завершиться
	// раньше, чем данные покинут буфер.
	if err := nc.Flush(); err != nil {
		log.Printf("flush: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	log.Println("core-pubsub: демо завершено, дренируем соединение (Drain, не Close) и выходим")
}
