// Профиль 02-determinism к статье «Workflow и детерминизм».
//
// Три сюжета:
//  1. воспроизвести non-determinism error на сломанном воркфлоу;
//  2. показать, что исправленное проигрывается корректно;
//  3. замерить, во что обходится replay при разной длине истории.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"tech.khorost/temporal-cookbook/internal/provisioning"
)

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		log.Fatal("подкоманда: worker | run | dump | replay")
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	address := fs.String("address", "temporal-frontend:7233", "адрес frontend")
	queue := fs.String("queue", "determinism-tq", "task queue")
	kind := fs.String("kind", "fixed", "broken | fixed | long")
	workflowID := fs.String("workflow", "determinism-demo", "WorkflowID")
	steps := fs.Int("steps", 10, "шагов для long")
	out := fs.String("out", "/histories", "каталог для выгруженных историй")
	_ = fs.Parse(args)

	ctx := context.Background()

	// Реплееру сервер не нужен: он работает по файлу истории.
	if cmd == "replay" {
		runReplay(*out, *workflowID)
		return
	}

	c, err := client.Dial(client.Options{HostPort: *address})
	if err != nil {
		log.Fatalf("подключение: %v", err)
	}
	defer c.Close()

	switch cmd {
	case "worker":
		w := worker.New(c, *queue, worker.Options{})
		w.RegisterWorkflow(BrokenWorkflow)
		w.RegisterWorkflow(FixedWorkflow)
		w.RegisterWorkflow(LongWorkflow)
		w.RegisterActivity(&provisioning.Activities{})
		log.Printf("[worker] очередь=%s зарегистрированы broken/fixed/long", *queue)
		if err := w.Run(worker.InterruptCh()); err != nil {
			log.Fatalf("воркер: %v", err)
		}

	case "run":
		opts := client.StartWorkflowOptions{ID: *workflowID, TaskQueue: *queue}
		var run client.WorkflowRun
		switch *kind {
		case "broken":
			run, err = c.ExecuteWorkflow(ctx, opts, BrokenWorkflow, "gpu-node-7")
		case "fixed":
			run, err = c.ExecuteWorkflow(ctx, opts, FixedWorkflow, "gpu-node-7")
		case "long":
			run, err = c.ExecuteWorkflow(ctx, opts, LongWorkflow, *steps)
		default:
			log.Fatalf("неизвестный kind %q", *kind)
		}
		if err != nil {
			log.Fatalf("старт: %v", err)
		}
		var res any
		if err := run.Get(ctx, &res); err != nil {
			log.Fatalf("результат: %v", err)
		}
		log.Printf("[run] %s завершён: %v", *workflowID, res)

	case "dump":
		// Выгрузка истории в файл: на ней потом гоняются replay-тесты
		// и замер стоимости replay. Формат — тот же JSON, что понимает
		// реплеер и Web UI.
		iter := c.GetWorkflowHistory(ctx, *workflowID, "", false,
			enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		var events []*history.HistoryEvent
		for iter.HasNext() {
			ev, err := iter.Next()
			if err != nil {
				log.Fatalf("чтение истории: %v", err)
			}
			events = append(events, ev)
		}
		if err := os.MkdirAll(*out, 0o755); err != nil {
			log.Fatalf("каталог: %v", err)
		}
		path := filepath.Join(*out, *workflowID+".json")
		if err := saveHistory(path, &history.History{Events: events}); err != nil {
			log.Fatalf("запись: %v", err)
		}
		fmt.Printf("ИСТОРИЯ workflow=%s events=%d file=%s\n", *workflowID, len(events), path)

	default:
		log.Fatalf("неизвестная подкоманда %q", cmd)
	}
}

// runReplay меряет ЧИСТЫЙ replay — без сети и без сервера: реплеер
// работает по файлу. Это отделяет цену проигрывания от цены обращения
// к persistence.
func runReplay(dir, workflowID string) {
	path := filepath.Join(dir, workflowID+".json")
	h, err := loadHistory(path)
	if err != nil {
		log.Fatalf("история %s: %v", path, err)
	}
	if len(h.Events) == 0 {
		log.Fatalf("история %s пуста: замер на пустых данных ничего не проверяет", path)
	}

	const iterations = 20
	durations := make([]time.Duration, 0, iterations)
	for i := 0; i < iterations; i++ {
		r := worker.NewWorkflowReplayer()
		r.RegisterWorkflow(LongWorkflow)
		start := time.Now()
		if err := r.ReplayWorkflowHistory(nil, h); err != nil {
			log.Fatalf("replay: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	med := durations[len(durations)/2]
	fmt.Printf("ЗАМЕР replay workflow=%s events=%d iterations=%d median_us=%d min_us=%d max_us=%d\n",
		workflowID, len(h.Events), iterations,
		med.Microseconds(), durations[0].Microseconds(),
		durations[len(durations)-1].Microseconds())
}
