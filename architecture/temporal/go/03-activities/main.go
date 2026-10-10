// Профиль 03-activities к статье «Activities вглубь».
//
// Сюжеты: ретрай неидемпотентной activity задваивает эффект; ключ
// идемпотентности это чинит; heartbeat возобновляет долгую задачу с
// последней точки; local activity против обычной — замер.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		log.Fatal("подкоманда: worker | charge | import | ping")
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	address := fs.String("address", "temporal-frontend:7233", "адрес frontend")
	dsn := fs.String("dsn", "postgres://temporal:temporal@postgres:5432/temporal?sslmode=disable", "DSN Postgres")
	queue := fs.String("queue", "activities-tq", "task queue")
	failTimes := fs.Int("fail-times", 2, "сколько первых попыток падают")
	safe := fs.Bool("safe", false, "использовать идемпотентную activity")
	local := fs.Bool("local", false, "использовать local activity")
	orderID := fs.String("order", "order-1", "идентификатор заказа")
	jobID := fs.String("job", "import-1", "идентификатор импорта")
	total := fs.Int("total", 60, "шагов импорта")
	calls := fs.Int("calls", 200, "вызовов activity для замера")
	workflowID := fs.String("workflow", "activities-demo", "WorkflowID")
	_ = fs.Parse(args)

	ctx := context.Background()
	c, err := client.Dial(client.Options{HostPort: *address})
	if err != nil {
		log.Fatalf("подключение: %v", err)
	}
	defer c.Close()

	switch cmd {
	case "worker":
		acts, err := NewActs(*dsn, *failTimes)
		if err != nil {
			log.Fatalf("activity: %v", err)
		}
		w := worker.New(c, *queue, worker.Options{})
		w.RegisterWorkflow(ChargeWorkflow)
		w.RegisterWorkflow(ImportWorkflow)
		w.RegisterWorkflow(PingWorkflow)
		w.RegisterActivity(acts)
		log.Printf("[worker] очередь=%s fail-times=%d", *queue, *failTimes)
		if err := w.Run(worker.InterruptCh()); err != nil {
			log.Fatalf("воркер: %v", err)
		}

	case "charge":
		run, err := c.ExecuteWorkflow(ctx,
			client.StartWorkflowOptions{ID: *workflowID, TaskQueue: *queue},
			ChargeWorkflow, *safe, *orderID, int64(100))
		if err != nil {
			log.Fatalf("старт: %v", err)
		}
		if err := run.Get(ctx, nil); err != nil {
			log.Fatalf("результат: %v", err)
		}
		log.Printf("[charge] завершено safe=%v order=%s", *safe, *orderID)

	case "import":
		run, err := c.ExecuteWorkflow(ctx,
			client.StartWorkflowOptions{ID: *workflowID, TaskQueue: *queue},
			ImportWorkflow, *jobID, *total)
		if err != nil {
			log.Fatalf("старт: %v", err)
		}
		var done int
		if err := run.Get(ctx, &done); err != nil {
			log.Fatalf("результат: %v", err)
		}
		fmt.Printf("ИМПОРТ job=%s done=%d total=%d\n", *jobID, done, *total)

	case "ping":
		start := time.Now()
		run, err := c.ExecuteWorkflow(ctx,
			client.StartWorkflowOptions{ID: *workflowID, TaskQueue: *queue},
			PingWorkflow, *calls, *local)
		if err != nil {
			log.Fatalf("старт: %v", err)
		}
		if err := run.Get(ctx, nil); err != nil {
			log.Fatalf("результат: %v", err)
		}
		elapsed := time.Since(start)

		// Считаем события в истории: цена вызова видна не только во
		// времени, но и в объёме записанного. Local activity пишет в
		// историю принципиально меньше — ради этого она и существует.
		iter := c.GetWorkflowHistory(ctx, *workflowID, "", false,
			enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		events := 0
		for iter.HasNext() {
			if _, err := iter.Next(); err != nil {
				log.Fatalf("история: %v", err)
			}
			events++
		}
		kind := "regular"
		if *local {
			kind = "local"
		}
		fmt.Printf("ЗАМЕР activity kind=%s calls=%d total_ms=%d per_call_us=%d events=%d events_per_call=%.2f\n",
			kind, *calls, elapsed.Milliseconds(),
			elapsed.Microseconds()/int64(*calls), events,
			float64(events)/float64(*calls))

	default:
		log.Fatalf("неизвестная подкоманда %q", cmd)
	}
}
