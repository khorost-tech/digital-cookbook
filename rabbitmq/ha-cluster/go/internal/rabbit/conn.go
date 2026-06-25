package rabbit

import (
	"context"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Dial открывает соединение и канал.
func Dial(url string) (*amqp.Connection, *amqp.Channel, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, ch, nil
}

// Connect поддерживает живое соединение, перебирая все узлы кластера по кругу.
// Ключевой момент клиентского HA: клиент должен знать НЕСКОЛЬКО узлов
// (или ходить через балансировщик). Подключение к одному узлу делает его SPOF —
// при падении этого узла переподключаться будет некуда.
func Connect(ctx context.Context, urls []string) <-chan *amqp.Connection {
	if len(urls) == 0 {
		panic("rabbit.Connect: список URL не должен быть пустым")
	}
	out := make(chan *amqp.Connection)
	go func() {
		defer close(out)
		attempt := 0
		for {
			url := urls[attempt%len(urls)]
			attempt++
			conn, err := amqp.Dial(url)
			if err != nil {
				log.Printf("reconnect: узел %s недоступен (%v); пробуем следующий через 1s", url, err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(1 * time.Second):
					continue
				}
			}
			log.Printf("reconnect: подключились к %s", url)
			select {
			case out <- conn:
			case <-ctx.Done():
				conn.Close()
				return
			}
			closed := make(chan *amqp.Error)
			conn.NotifyClose(closed)
			select {
			case <-ctx.Done():
				conn.Close()
				return
			case err := <-closed:
				log.Printf("reconnect: соединение закрыто (%v) — переключаемся на другой узел", err)
			}
		}
	}()
	return out
}
