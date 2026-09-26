// replica-health — мониторинг здоровья HA-кластера из кода: лаг реплик со стороны
// лидера (pg_stat_replication) плюс топология из Patroni REST API (/cluster).
// Так строят health-check и алерты: «реплика отстала на N байт» / «лидер сменился».
//
//	go run . [leader_dsn] [patroni_url]
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

const replQuery = `
SELECT application_name,
       state,
       sync_state,
       pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn) AS replay_lag_bytes
FROM pg_stat_replication
ORDER BY application_name`

type member struct {
	Name  string `json:"name"`
	Role  string `json:"role"`
	State string `json:"state"`
	Lag   any    `json:"lag"`
}
type cluster struct {
	Members []member `json:"members"`
}

func replicationStatus(dsn string) error {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx, replQuery)
	if err != nil {
		return err
	}
	defer rows.Close()

	fmt.Printf("%-12s %-10s %-10s %s\n", "replica", "state", "sync", "replay_lag_bytes")
	for rows.Next() {
		var app, state, sync string
		var lag int64
		if err := rows.Scan(&app, &state, &sync, &lag); err != nil {
			return err
		}
		fmt.Printf("%-12s %-10s %-10s %d\n", app, state, sync, lag)
	}
	return rows.Err()
}

func patroniTopology(url string) error {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var c cluster
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return err
	}
	fmt.Printf("%-12s %-10s %s\n", "member", "role", "state")
	for _, m := range c.Members {
		fmt.Printf("%-12s %-10s %s\n", m.Name, m.Role, m.State)
	}
	return nil
}

func main() {
	dsn := "postgres://postgres:hapass@localhost:5000/postgres"
	patroni := "http://localhost:8008/cluster"
	if len(os.Args) > 1 {
		dsn = os.Args[1]
	}
	if len(os.Args) > 2 {
		patroni = os.Args[2]
	}

	fmt.Println("== реплики со стороны лидера (pg_stat_replication) ==")
	if err := replicationStatus(dsn); err != nil {
		fmt.Fprintln(os.Stderr, "репликация:", err)
	}
	fmt.Println("\n== топология кластера (Patroni REST /cluster) ==")
	if err := patroniTopology(patroni); err != nil {
		fmt.Fprintln(os.Stderr, "patroni:", err)
	}
}
