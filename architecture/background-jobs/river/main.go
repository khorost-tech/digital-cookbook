// River — готовая очередь задач на PostgreSQL. Здесь она нужна как КОНТРАСТ к
// своему воркеру: тот же принцип (SKIP LOCKED + аренда), но всё готовое —
// миграции, воркер-пул, повторы. Показываем ровно это, без пересказа её внутренностей.
//
// Версия проверена живьём на момент задачи (Task 9 серии «Фоновые задачи и
// планировщики»): github.com/riverqueue/river v0.40.0 — последняя стабильная
// в списке `go list -m -versions` (без -rc-тегов). rivermigrate — подпакет
// основного модуля river, отдельного go-модуля для него нет.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

type EmailArgs struct {
	To string `json:"to"`
}

func (EmailArgs) Kind() string { return "email" }

type EmailWorker struct {
	river.WorkerDefaults[EmailArgs]
}

func (w *EmailWorker) Work(ctx context.Context, job *river.Job[EmailArgs]) error {
	fmt.Printf("[river] джоба id=%d попытка=%d to=%s\n", job.ID, job.Attempt, job.Args.To)
	return nil
}

func main() { os.Exit(run()) }

func run() int {
	ctx := context.Background()
	dsn := "postgres://jobs:jobs@localhost:5456/jobs"

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Println("pgxpool:", err)
		return 1
	}
	defer pool.Close()

	// Миграции River создают свои таблицы — рядом с нашей jobs, не мешая ей.
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		fmt.Println("migrator:", err)
		return 1
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		fmt.Println("migrate:", err)
		return 1
	}

	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &EmailWorker{}); err != nil {
		fmt.Println("add worker:", err)
		return 1
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 2}},
		Workers: workers,
	})
	if err != nil {
		fmt.Println("client:", err)
		return 1
	}
	if err := client.Start(ctx); err != nil {
		fmt.Println("start:", err)
		return 1
	}
	for i := 1; i <= 5; i++ {
		if _, err := client.Insert(ctx, EmailArgs{To: fmt.Sprintf("user%d@example.com", i)}, nil); err != nil {
			fmt.Println("insert:", err)
			return 1
		}
	}
	time.Sleep(3 * time.Second)
	if err := client.Stop(ctx); err != nil {
		fmt.Println("stop:", err)
		return 1
	}
	fmt.Println("[river] готово")
	return 0
}
