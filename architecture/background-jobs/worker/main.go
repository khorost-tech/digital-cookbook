// Воркер очереди: claim -> (heartbeat в фоне) -> работа -> complete.
//
//	go run . -id w1 -work 2s -lease 5s
//	go run . -id w1 -heartbeat=false   # без продления аренды (артефакт 2)
//	go run . -reclaim                  # только возврат истёкших аренд
//	go run . -id w1 -mode listen       # пробуждение по NOTIFY, а не по таймеру (артефакт 8)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() { os.Exit(run()) }

func run() int {
	dsn := flag.String("dsn", "postgres://jobs:jobs@localhost:5456/jobs", "PostgreSQL DSN")
	id := flag.String("id", "w1", "идентификатор воркера (leased_by)")
	lease := flag.Duration("lease", 5*time.Second, "срок аренды джобы")
	work := flag.Duration("work", 1*time.Second, "сколько «выполняется» джоба")
	hb := flag.Bool("heartbeat", true, "продлевать аренду во время работы")
	dur := flag.Duration("for", 15*time.Second, "сколько работать")
	reclaimOnly := flag.Bool("reclaim", false, "только вернуть истёкшие аренды и выйти")
	mode := flag.String("mode", "poll", "poll|listen — как воркер узнаёт о новых джобах")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	db, err := Connect(ctx, *dsn)
	if err != nil {
		fmt.Println("postgres:", err)
		return 1
	}
	defer db.Close()

	if *reclaimOnly {
		ids, err := ReclaimExpired(ctx, db)
		if err != nil {
			fmt.Println("reclaim:", err)
			return 1
		}
		fmt.Printf("reclaim: возвращено в очередь %d джоб: %v\n", len(ids), ids)
		return 0
	}

	deadline, cancel2 := context.WithTimeout(ctx, *dur)
	defer cancel2()

	done, failed := 0, 0
	for {
		if deadline.Err() != nil {
			break
		}
		job, err := Claim(deadline, db, *id, *lease)
		if err != nil {
			if deadline.Err() != nil {
				break
			}
			fmt.Println("claim:", err)
			return 1
		}
		if job == nil {
			if *mode == "listen" {
				// LISTEN вместо сна: воркер блокируется на своём соединении, пока
				// триггер (см. sql/01-schema.sql) не пришлёт NOTIFY в jobs_new —
				// на вставке новой джобы или на возврате в очередь после reclaim/fail.
				// Таймаут 2с — подстраховка (WaitForNotification не должен спать
				// дольше общего дедлайна воркера), а не интервал опроса: в отличие
				// от poll-ветки здесь нет закладываемой нижней границы задержки.
				if _, err := WaitForJob(deadline, *dsn, 2*time.Second); err != nil && deadline.Err() == nil {
					fmt.Println("listen:", err)
					return 1
				}
				continue
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		fmt.Printf("[%s] взял джобу id=%d kind=%s попытка=%d\n", *id, job.ID, job.Kind, job.Attempt)

		// Работа выполняется под ctx (не deadline): при SIGTERM хотим дать джобе
		// доработать, а не бросить её на середине — см. артефакт 3.
		if !doWork(ctx, db, *id, job, *work, *lease, *hb) {
			failed++
			continue
		}
		ok, err := Complete(context.Background(), db, job.ID, *id)
		if err != nil {
			fmt.Println("complete:", err)
			return 1
		}
		if !ok {
			// Аренда потеряна, пока мы работали: джобу уже забрал другой воркер.
			// Это и есть at-least-once ИСПОЛНЕНИЯ — работа сделана дважды.
			fmt.Printf("[%s] джоба id=%d завершена, но аренда была потеряна — результат чужой\n", *id, job.ID)
			failed++
			continue
		}
		done++
		fmt.Printf("[%s] завершил id=%d\n", *id, job.ID)
	}

	fmt.Printf("воркер %s: выполнено=%d потеряно_аренд=%d\n", *id, done, failed)
	return 0
}

// doWork имитирует полезную работу, продлевая аренду каждые lease/3.
// Возвращает false, если аренда потеряна во время работы.
//
// SIGTERM НЕ прерывает начатую джобу: воркер дорабатывает её и лишь потом
// выходит из внешнего цикла, не забирая новых (артефакт 3). Ключевая деталь —
// во время такого дренажа heartbeat обязан продолжаться. Наивная реализация
// «получили сигнал -> досыпаем остаток» перестаёт продлевать аренду, и если
// работа длиннее lease, аренда истекает ПРЯМО В ХОДЕ штатного завершения:
// ReclaimExpired возвращает джобу в очередь, её берёт другой воркер, и она
// выполняется дважды — ровно то, что graceful shutdown должен предотвращать.
// Поэтому ctx.Done() только снимает канал с select (обнулением переменной),
// а тикер и дедлайн работы продолжают жить.
func doWork(ctx context.Context, db *pgxpool.Pool, workerID string, job *Job,
	work, lease time.Duration, heartbeat bool) bool {
	tick := time.NewTicker(lease / 3)
	defer tick.Stop()
	deadline := time.After(work)
	stopping := ctx.Done()
	for {
		select {
		case <-deadline:
			return true
		case <-stopping:
			// Сигнал получен и учтён. nil-канал в select блокируется навсегда,
			// поэтому ветка больше не сработает, а heartbeat ниже продолжит
			// продлевать аренду до конца работы.
			stopping = nil
			fmt.Printf("[%s] сигнал завершения: дорабатываю id=%d, новых джоб не беру\n", workerID, job.ID)
		case <-tick.C:
			if !heartbeat {
				continue
			}
			ok, err := Heartbeat(context.Background(), db, job.ID, workerID, lease)
			if err != nil {
				fmt.Println("heartbeat:", err)
				return false
			}
			if !ok {
				fmt.Printf("[%s] аренда джобы id=%d потеряна — прекращаю работу\n", workerID, job.ID)
				return false
			}
		}
	}
}
