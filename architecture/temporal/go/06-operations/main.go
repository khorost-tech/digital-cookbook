// Профиль 06-operations к статье «Temporal в эксплуатации».
//
// Сюжет: недонастроенный воркер при живом и незагруженном сервере даёт
// растущий schedule-to-start. Варьируем ёмкость воркера и число pollers
// при ФИКСИРОВАННОЙ нагрузке — и смотрим, что меняется.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"tech.khorost/temporal-cookbook/internal/obs"
)

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		log.Fatal("подкоманда: worker | load")
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	address := fs.String("address", "temporal-frontend:7233", "адрес frontend")
	queue := fs.String("queue", "ops-tq", "task queue")
	concurrency := fs.Int("concurrency", 2, "MaxConcurrentActivityExecutionSize")
	pollers := fs.Int("pollers", 2, "MaxConcurrentActivityTaskPollers")
	work := fs.Duration("work", 300*time.Millisecond, "длительность одной activity")
	steps := fs.Int("steps", 60, "activity в одном воркфлоу")
	count := fs.Int("count", 4, "сколько воркфлоу запустить")
	label := fs.String("label", "", "метка замера")
	_ = fs.Parse(args)

	ctx := context.Background()

	switch cmd {
	case "worker":
		// Метрики SDK на 8077 — их скрейпит Prometheus стенда по имени
		// контейнера ops-worker (см. compose/prometheus.yml).
		mh, err := obs.NewPrometheusHandler(":8077")
		if err != nil {
			log.Fatalf("метрики: %v", err)
		}
		c, err := client.Dial(client.Options{HostPort: *address, MetricsHandler: mh})
		if err != nil {
			log.Fatalf("подключение: %v", err)
		}
		defer c.Close()

		w := worker.New(c, *queue, worker.Options{
			MaxConcurrentActivityExecutionSize: *concurrency,
			MaxConcurrentActivityTaskPollers:   *pollers,
		})
		w.RegisterWorkflow(LoadWorkflow)
		w.RegisterWorkflow(DayLongWorkflow)
		w.RegisterActivity(&SlowActs{Work: *work})
		log.Printf("[worker] очередь=%s concurrency=%d pollers=%d work=%s",
			*queue, *concurrency, *pollers, *work)
		if err := w.Run(worker.InterruptCh()); err != nil {
			log.Fatalf("воркер: %v", err)
		}

	case "load":
		c, err := client.Dial(client.Options{HostPort: *address})
		if err != nil {
			log.Fatalf("подключение: %v", err)
		}
		defer c.Close()

		start := time.Now()
		runs := make([]client.WorkflowRun, 0, *count)
		for i := 0; i < *count; i++ {
			run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
				ID:        fmt.Sprintf("ops-%s-%d", *label, i),
				TaskQueue: *queue,
			}, LoadWorkflow, *steps)
			if err != nil {
				log.Fatalf("старт: %v", err)
			}
			runs = append(runs, run)
		}
		for _, r := range runs {
			if err := r.Get(ctx, nil); err != nil {
				log.Fatalf("результат: %v", err)
			}
		}
		elapsed := time.Since(start)
		fmt.Printf("ЗАМЕР ops label=%s steps=%d count=%d activities=%d elapsed_s=%.2f\n",
			*label, *steps, *count, (*steps)*(*count), elapsed.Seconds())

	default:
		log.Fatalf("неизвестная подкоманда %q", cmd)
	}
}
