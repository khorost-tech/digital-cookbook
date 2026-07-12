// request-reply демонстрирует встроенный request/reply nats.go: сервис-
// responder (сам входит в очередь "воркеров" через QueueSubscribe — так
// несколько экземпляров сервиса делят входящие запросы) и клиент-requester
// с таймаутом через context.
package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/nats-io/nats.go"

	"nats-cookbook-go/internal/natsconn"
)

const subject = "service.time"

func main() {
	nc, err := natsconn.Connect("request-reply")
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		natsconn.Shutdown(ctx, nc)
	}()

	startResponder(nc)

	// Небольшая пауза, чтобы подписка responder'а гарантированно долетела
	// до сервера до первого запроса — в бою запросы просто ретраятся,
	// здесь это для стабильности однократного демо-прогона.
	time.Sleep(100 * time.Millisecond)

	if err := requestOnce(nc); err != nil {
		log.Fatalf("request: %v", err)
	}
}

// startResponder поднимает "сервис", отвечающий на service.time.
// QueueSubscribe с общим именем группы — тот же приём, что и в 00-core/
// nats reply: несколько экземпляров сервиса можно запустить параллельно,
// и NATS сам распределит запросы между ними round-robin-подобно.
func startResponder(nc *nats.Conn) {
	sub, err := nc.QueueSubscribe(subject, "time-service", func(msg *nats.Msg) {
		now := time.Now().Format(time.RFC3339)
		if err := msg.Respond([]byte(now)); err != nil {
			log.Printf("responder: ответ не отправлен: %v", err)
		}
	})
	if err != nil {
		log.Fatalf("responder subscribe: %v", err)
	}
	log.Printf("responder: слушаю %q (queue group time-service)", subject)
	_ = sub
}

// requestOnce делает один запрос с таймаутом через context.
// RequestWithContext — предпочтительный способ задать таймаут в коде,
// который уже пронизан context (HTTP-хендлеры, gRPC-методы и т.п.);
// nc.Request(subj, data, timeout) с time.Duration равнозначен, но не
// прокидывает отмену вызывающего контекста.
func requestOnce(nc *nats.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msg, err := nc.RequestWithContext(ctx, subject, nil)
	switch {
	case errors.Is(err, nats.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return errors.New("нет ответа в течение таймаута — responder недоступен или перегружен")
	case err != nil:
		return err
	}

	log.Printf("requester: получен ответ: %s", string(msg.Data))
	return nil
}
