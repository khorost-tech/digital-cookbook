// Package natsconn — общий хелпер подключения для всех примеров clients/go.
//
// Инкапсулирует то, что в продакшн-коде иначе пришлось бы копировать в каждый
// cmd/: опции reconnect, коллбэки Disconnected/Reconnected/Closed и
// graceful shutdown через Drain() вместо Close().
package natsconn

import (
	"context"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

// DefaultURL — адрес ноды n1 стенда nats/02-cluster (см. digital-cookbook).
// Для nats/01-jetstream (один узел) адрес совпадает.
const DefaultURL = nats.DefaultURL // "nats://127.0.0.1:4222"

// Connect открывает соединение с сервером (или списком серверов кластера)
// с включённым автопереподключением и логированием переходов состояния.
//
// name — видимое имя клиента (появляется в мониторинге сервера, "connz"),
// удобно для отладки, когда одновременно подключено несколько демо-процессов.
func Connect(name string, urls ...string) (*nats.Conn, error) {
	if len(urls) == 0 {
		urls = []string{DefaultURL}
	}

	opts := []nats.Option{
		nats.Name(name),

		// RetryOnFailedConnect — не завершать nats.Connect ошибкой, если сервер
		// временно недоступен в момент старта: клиент уйдёт в фоновый reconnect-
		// цикл и подключится, как только сервер станет доступен.
		nats.RetryOnFailedConnect(true),

		// MaxReconnects(-1) — не ограничивать число попыток переподключения.
		// Опция без ограничения существует специально: по умолчанию клиент
		// после исчерпания лимита попыток считает соединение окончательно
		// потерянным и вызывает ClosedHandler — для долгоживущего сервиса
		// это обычно не то поведение, которое нужно.
		nats.MaxReconnects(-1),
		nats.ReconnectWait(1 * time.Second),

		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Printf("[%s] disconnected: %v", name, err)
			} else {
				log.Printf("[%s] disconnected", name)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Printf("[%s] reconnected to %s", name, nc.ConnectedUrl())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			log.Printf("[%s] connection closed", name)
		}),
	}

	nc, err := nats.Connect(joinURLs(urls), opts...)
	if err != nil {
		return nil, err
	}
	return nc, nil
}

// Shutdown делает graceful shutdown: Drain() вместо Close().
//
// Разница принципиальна. Close() рвёт соединение немедленно — недошедшие
// исходящие сообщения и незавершённые обработчики подписок теряются.
// Drain() переводит соединение в режим "дожить": перестаёт принимать новые
// сообщения на подписки, ждёт завершения уже запущенных обработчиков,
// досылает буферизованные исходящие публикации и только после этого
// закрывает соединение сам. Это тот же принцип, что graceful shutdown
// HTTP-сервера (дождаться in-flight запросов), перенесённый на pub/sub.
//
// ctx используется только как дедлайн ожидания — сам Drain блокирует
// вызывающего до завершения или до nats.DrainTimeout (по умолчанию 30s,
// настраивается через nats.DrainTimeout()).
func Shutdown(ctx context.Context, nc *nats.Conn) {
	done := make(chan struct{})
	go func() {
		if err := nc.Drain(); err != nil {
			log.Printf("drain: %v", err)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		log.Printf("drain: не завершился до дедлайна, закрываем соединение принудительно")
		nc.Close()
	}
}

func joinURLs(urls []string) string {
	out := urls[0]
	for _, u := range urls[1:] {
		out += "," + u
	}
	return out
}
