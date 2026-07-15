package main

import (
	"context"
	"time"

	"github.com/gocql/gocql"
)

// ScyllaDB: LWT нельзя применить к выражению `balance = balance - 1` (это не counter-колонка),
// поэтому — компаре-энд-сет: читаем текущее, UPDATE ... SET balance = new WHERE ... IF balance = old.
// Если между чтением и записью значение изменилось — CAS не применился, повторяем (Paxos + retry).
// Это дороже одиночного атомарного апдейта: каждый успех — раунды консенсуса, под конкуренцией — retry.
type scyllaStore struct{ sess *gocql.Session }

func newScylla() (Store, error) {
	host := envOr("SCYLLA_HOST", "localhost:9053")
	c := gocql.NewCluster(host)
	c.Consistency = gocql.Quorum
	c.Timeout = 15 * time.Second
	c.ConnectTimeout = 15 * time.Second
	root, err := c.CreateSession()
	if err != nil {
		return nil, err
	}
	if err := root.Query(`CREATE KEYSPACE IF NOT EXISTS demo
		WITH replication = {'class':'SimpleStrategy','replication_factor':1}`).Exec(); err != nil {
		root.Close()
		return nil, err
	}
	root.Close()
	c.Keyspace = "demo"
	sess, err := c.CreateSession()
	if err != nil {
		return nil, err
	}
	_ = sess.Query(`CREATE TABLE IF NOT EXISTS accounts (id text PRIMARY KEY, balance bigint)`).Exec()
	return &scyllaStore{sess}, nil
}

func (s *scyllaStore) Name() string { return "scylla" }

func (s *scyllaStore) Reset(ctx context.Context, budget int64) error {
	return s.sess.Query(`INSERT INTO accounts (id, balance) VALUES ('acct', ?)`, budget).Exec()
}

func (s *scyllaStore) Decrement(ctx context.Context) (bool, int64) {
	var cur int64
	if err := s.sess.Query(`SELECT balance FROM accounts WHERE id = 'acct'`).Scan(&cur); err != nil {
		return false, 0
	}
	var conflicts int64
	for {
		if cur <= 0 {
			return false, conflicts
		}
		// ScanCAS: при неудаче cur получает актуальное значение колонки balance
		applied, err := s.sess.Query(
			`UPDATE accounts SET balance = ? WHERE id = 'acct' IF balance = ?`, cur-1, cur).ScanCAS(&cur)
		if err != nil {
			return false, conflicts
		}
		if applied {
			return true, conflicts
		}
		conflicts++ // кто-то изменил balance между чтением и CAS — повторяем с новым cur
	}
}

func (s *scyllaStore) Final(ctx context.Context) int64 {
	var b int64
	_ = s.sess.Query(`SELECT balance FROM accounts WHERE id = 'acct'`).Scan(&b)
	return b
}

func (s *scyllaStore) Close() { s.sess.Close() }
