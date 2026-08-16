// Command loader inserts n rows with a fixed payload into one of three
// tables (k_bigint, k_uuid4, k_uuid7) that differ only in the primary key
// type. The three branches differ ONLY in how the key value is produced:
// bigint lets Postgres assign the identity, uuid4 uses uuid.New(), uuid7
// uses uuid.NewV7(). Payload is identical across branches.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const batchSize = 1000

func main() {
	os.Exit(run())
}

func run() int {
	key := flag.String("key", "", "bigint|uuid4|uuid7")
	n := flag.Int("n", 100000, "rows")
	dsn := flag.String("dsn", "postgres://ids:ids@localhost:15436/ids", "pg dsn")
	flag.Parse()

	payload := strings.Repeat("x", 64) // фиксированная нагрузка во всех ветках

	table, sql, genUUID, err := branch(*key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "unknown -key:", *key)
		return 1
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, *dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		return 1
	}
	defer func() { _ = conn.Close(ctx) }()

	start := time.Now()
	if err := loadRows(ctx, conn, *key, sql, payload, *n, genUUID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	elapsed := time.Since(start)
	fmt.Fprintf(os.Stderr, "table=%s rows=%d elapsed=%s rate=%.0f rows/s\n",
		table, *n, elapsed, float64(*n)/elapsed.Seconds())
	return 0
}

// branch returns the target table, insert SQL, and key generator for the
// given -key value. genUUID is nil for the bigint branch, where the key is
// assigned by the database identity column instead.
func branch(key string) (table, sql string, genUUID func() (uuid.UUID, error), err error) {
	switch key {
	case "bigint":
		return "k_bigint", "INSERT INTO k_bigint(payload) VALUES($1)", nil, nil
	case "uuid4":
		return "k_uuid4", "INSERT INTO k_uuid4(id,payload) VALUES($1,$2)", func() (uuid.UUID, error) {
			return uuid.New(), nil
		}, nil
	case "uuid7":
		return "k_uuid7", "INSERT INTO k_uuid7(id,payload) VALUES($1,$2)", uuid.NewV7, nil
	default:
		return "", "", nil, fmt.Errorf("unknown key type: %s", key)
	}
}

func loadRows(ctx context.Context, conn *pgx.Conn, key, sql, payload string, n int, genUUID func() (uuid.UUID, error)) error {
	batch := &pgx.Batch{}
	for i := 0; i < n; i++ {
		if key == "bigint" {
			batch.Queue(sql, payload)
		} else {
			id, err := genUUID()
			if err != nil {
				return fmt.Errorf("gen: %w", err)
			}
			batch.Queue(sql, id, payload)
		}
		if batch.Len() >= batchSize {
			if err := sendBatch(ctx, conn, batch); err != nil {
				return err
			}
			batch = &pgx.Batch{}
		}
	}
	if batch.Len() > 0 {
		if err := sendBatch(ctx, conn, batch); err != nil {
			return err
		}
	}
	return nil
}

func sendBatch(ctx context.Context, conn *pgx.Conn, batch *pgx.Batch) error {
	if err := conn.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("batch: %w", err)
	}
	return nil
}
