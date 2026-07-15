// Примеры к статье #3 «KV и документные: транзакций почти нет — Redis, MongoDB, ScyllaDB».
// https://khorost.tech/databases/transactions-kv-document-redis-mongo-scylla/
//
// По одному выразительному примеру на систему: Redis (WATCH-CAS против naive GET/SET),
// MongoDB (multi-document transaction), ScyllaDB (LWT-резервация на Paxos).
//
//	go run . redis    # нужен docker compose up -d redis
//	go run . mongo    # нужен mongo (replica set инициализирован, см. README)
//	go run . scylla   # нужен scylla (может стартовать ~минуту)
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
		fmt.Println("usage: go run . <redis|mongo|scylla>")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "redis":
		runRedis()
	case "mongo":
		runMongo()
	case "scylla":
		runScylla()
	default:
		fmt.Println("неизвестная команда:", os.Args[1])
		os.Exit(1)
	}
}
