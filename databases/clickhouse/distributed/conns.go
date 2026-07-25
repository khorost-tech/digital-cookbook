package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// nodeAddrs — 4 CH-ноды кластера, порядок соответствует remote_servers
// (../config/remote-servers.xml): шард 1 (r1, r2), шард 2 (r1, r2).
// Адреса — internal-хостнеймы compose/cluster.yml (сеть
// clickhouse-cluster-net), НЕ host-порты — этот Go-стенд сам запускается как
// контейнер на той же сети (см. ../ops/distributed-demo.sh).
var nodeAddrs = map[string]string{
	"s1-r1": "ch-s1-r1:9000",
	"s1-r2": "ch-s1-r2:9000",
	"s2-r1": "ch-s2-r1:9000",
	"s2-r2": "ch-s2-r2:9000",
}

// nodeOrder — стабильный порядок обхода нод для отчётов.
var nodeOrder = []string{"s1-r1", "s1-r2", "s2-r1", "s2-r2"}

func openConn(addr string) (clickhouse.Conn, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{addr},
		Auth:        clickhouse.Auth{Database: "demo", Username: "default"},
		DialTimeout: 10 * time.Second,
		Settings:    clickhouse.Settings{"max_execution_time": 60},
	})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", addr, err)
	}
	return conn, nil
}

// openAllNodes открывает по одному соединению на каждую из 4 нод — нужно
// узлам, которые в разных фазах читают/пишут СВОЮ локальную (не Distributed)
// таблицу напрямую (Step 3/Step 4 брифа: вставка в конкретную реплику,
// проверка конкретной ноды после docker stop/start).
func openAllNodes() (map[string]clickhouse.Conn, error) {
	conns := make(map[string]clickhouse.Conn, len(nodeAddrs))
	for _, name := range nodeOrder {
		c, err := openConn(nodeAddrs[name])
		if err != nil {
			return nil, err
		}
		if err := c.Ping(context.Background()); err != nil {
			return nil, fmt.Errorf("ping %s (%s): %w", name, nodeAddrs[name], err)
		}
		conns[name] = c
	}
	return conns, nil
}

func closeAll(conns map[string]clickhouse.Conn) {
	for _, c := range conns {
		c.Close()
	}
}
