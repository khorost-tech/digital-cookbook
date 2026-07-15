// Примеры к статье #4 «Брокеры и стриминг: транзакции в очереди и логе — RabbitMQ и Kafka».
// https://khorost.tech/databases/transactions-brokers-rabbitmq-kafka/
//
//	go run . rabbit   # publisher confirms (нужен RabbitMQ)
//	go run . kafka    # транзакционный producer + read_committed (нужен Kafka)
//	go run . outbox   # outbox pattern: PG-транзакция + релей в Kafka (нужны PostgreSQL и Kafka)
package main

import (
	"fmt"
	"os"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: go run . <rabbit|kafka|outbox>")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "rabbit":
		runRabbit()
	case "kafka":
		runKafka()
	case "outbox":
		runOutbox()
	default:
		fmt.Println("неизвестная команда:", os.Args[1])
		os.Exit(1)
	}
}
