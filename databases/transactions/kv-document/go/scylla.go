package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gocql/gocql"
)

// runScylla: LWT-резервация. BATCH в Scylla/Cassandra — это НЕ транзакция: logged BATCH даёт
// не откат, а eventual completion через batchlog (координатор доигрывает недоприменённые
// мутации до конца), и не даёт изоляции — в том числе между партициями. Настоящая
// линеаризуемость — только через LWT (условие IF [NOT EXISTS]) на Paxos, и только в пределах
// одного раздела, ценой раундов консенсуса.
// Здесь: 16 воркеров одновременно бронируют одно место.
//   - LWT (INSERT ... IF NOT EXISTS): применяется РОВНО ОДИН — остальные получают applied=false.
//   - plain INSERT: «успешны» все, взаимного исключения нет (last-write-wins).
func runScylla() {
	host := envOr("SCYLLA_HOST", "localhost:9052")
	cluster := gocql.NewCluster(host)
	cluster.Consistency = gocql.Quorum
	cluster.Timeout = 15 * time.Second
	cluster.ConnectTimeout = 15 * time.Second
	root, err := cluster.CreateSession()
	if err != nil {
		fmt.Println("scylla connect:", err)
		return
	}
	if err := root.Query(`CREATE KEYSPACE IF NOT EXISTS demo
		WITH replication = {'class':'SimpleStrategy','replication_factor':1}`).Exec(); err != nil {
		fmt.Println("keyspace:", err)
		root.Close()
		return
	}
	root.Close()

	cluster.Keyspace = "demo"
	sess, err := cluster.CreateSession()
	if err != nil {
		fmt.Println("scylla session:", err)
		return
	}
	defer sess.Close()
	_ = sess.Query(`CREATE TABLE IF NOT EXISTS seats (seat_id text PRIMARY KEY, booked_by text)`).Exec()

	const workers = 16

	// 1) LWT: INSERT ... IF NOT EXISTS — линеаризуемо, победитель ровно один
	_ = sess.Query(`DELETE FROM seats WHERE seat_id = ?`, "A1").Exec()
	var lwtWon int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			var exSeat, exBy string
			applied, err := sess.Query(
				`INSERT INTO seats (seat_id, booked_by) VALUES (?, ?) IF NOT EXISTS`,
				"A1", fmt.Sprintf("worker-%d", id)).ScanCAS(&exSeat, &exBy)
			if err == nil && applied {
				atomic.AddInt64(&lwtWon, 1)
			}
		}(w)
	}
	wg.Wait()

	// 2) plain INSERT (без IF) — взаимного исключения нет, «успешны» все
	_ = sess.Query(`DELETE FROM seats WHERE seat_id = ?`, "B1").Exec()
	var plainOK int64
	var wg2 sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg2.Add(1)
		go func(id int) {
			defer wg2.Done()
			if err := sess.Query(`INSERT INTO seats (seat_id, booked_by) VALUES (?, ?)`,
				"B1", fmt.Sprintf("worker-%d", id)).Exec(); err == nil {
				atomic.AddInt64(&plainOK, 1)
			}
		}(w)
	}
	wg2.Wait()

	fmt.Printf("scylla: %d воркеров бронируют одно место.\n", workers)
	fmt.Printf("  LWT (IF NOT EXISTS): применилось %d из %d — эксклюзивно ✓\n", atomic.LoadInt64(&lwtWon), workers)
	fmt.Printf("  plain INSERT: «успешно» %d из %d — взаимного исключения нет (last-write-wins)\n", atomic.LoadInt64(&plainOK), workers)
}
