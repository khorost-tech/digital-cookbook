// Command smoke — минимальная живая проверка коннекта к ClickHouse из Go
// через clickhouse-go v2 (нативный протокол). Task 1 каркаса серии
// "ClickHouse: глубокое погружение": подтверждает, что compose/compose.yml
// поднимает рабочий сервер и что go.mod резолвится в контейнере golang:1.25.
// Следующие стенды серии (#2 MergeTree, #3 бенчмарк драйверов) заменят это
// на содержательные сценарии.
//
// Запуск (из контейнера на сети clickhouse-cookbook-net, см. ../../README.md):
//
//	docker run --rm --network clickhouse-cookbook-net -v "$(pwd)/go:/app" -w /app golang:1.25 \
//	  go run ./smoke -addr=clickhouse:9000
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func main() {
	addr := flag.String("addr", "clickhouse:9000", "адрес ClickHouse (нативный протокол, host:port)")
	flag.Parse()

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{*addr},
		Auth: clickhouse.Auth{
			Database: "demo",
			Username: "default",
		},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("clickhouse.Open: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := conn.Ping(ctx); err != nil {
		log.Fatalf("Ping: %v", err)
	}

	row := conn.QueryRow(ctx, "SELECT 1")
	var one uint8
	if err := row.Scan(&one); err != nil {
		log.Fatalf("SELECT 1 scan: %v", err)
	}
	if one != 1 {
		log.Fatalf("SELECT 1 returned unexpected value: %d", one)
	}

	var version string
	if err := conn.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		log.Fatalf("SELECT version() scan: %v", err)
	}

	fmt.Printf("smoke OK: connected to %s, SELECT 1 = %d, server version = %s\n", *addr, one, version)
}
