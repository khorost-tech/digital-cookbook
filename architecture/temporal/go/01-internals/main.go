// Профиль 01-internals к статье «Архитектура Temporal вглубь».
//
// Два сюжета:
//  1. sticky-кэш: воркер с прогретым кэшем против воркера с
//     MaxCachedWorkflows=0, который реплеит историю на КАЖДОМ workflow task;
//  2. роль history убита — что видит воркер и что происходит с исполнением.
//
// Замер намеренно устроен так, чтобы различалась РОВНО одна настройка.
// Домен, нагрузка, число воркеров и очередь — одни и те же.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"tech.khorost/temporal-cookbook/internal/obs"
	"tech.khorost/temporal-cookbook/internal/provisioning"
)

// StepWorkflow — воркфлоу с управляемым числом шагов. Чем больше шагов,
// тем длиннее история и тем дороже полный replay. Именно на этом
// различаются прогретый и холодный воркер.
func StepWorkflow(ctx workflow.Context, steps int) (int, error) {
	ctx = workflow.WithActivityOptions(ctx, provisioning.DefaultActivityOptions())
	var acts *provisioning.Activities
	done := 0
	for i := 0; i < steps; i++ {
		var ok bool
		if err := workflow.ExecuteActivity(ctx, acts.CheckAvailability,
			fmt.Sprintf("шаг-%d", i)).Get(ctx, &ok); err != nil {
			return done, err
		}
		done++
		// Таймер между шагами разрывает workflow task: воркер отдаёт
		// управление серверу и получает следующий task заново — момент,
		// в который sticky-кэш либо помогает, либо его нет.
		if err := workflow.Sleep(ctx, time.Millisecond); err != nil {
			return done, err
		}
	}
	return done, nil
}

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		log.Fatal("подкоманда: worker | load")
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	address := fs.String("address", "temporal-frontend:7233", "адрес frontend")
	queue := fs.String("queue", "internals-tq", "task queue")
	cache := fs.Int("cache", 1000, "MaxCachedWorkflows; 0 отключает sticky-кэш")
	steps := fs.Int("steps", 40, "шагов в воркфлоу")
	count := fs.Int("count", 20, "сколько воркфлоу запустить")
	label := fs.String("label", "", "метка замера для вывода")
	_ = fs.Parse(args)

	ctx := context.Background()

	opts := client.Options{HostPort: *address}
	if cmd == "worker" {
		// Метрики SDK — единственный инструмент, которым видно работу
		// sticky-кэша: сервер про кэш воркера не знает. End-to-end
		// latency здесь не годится — цена replay измеряется
		// миллисекундами и тонет в сетевых раундах.
		mh, err := obs.NewPrometheusHandler(":8077")
		if err != nil {
			log.Fatalf("метрики: %v", err)
		}
		opts.MetricsHandler = mh
	}

	c, err := client.Dial(opts)
	if err != nil {
		log.Fatalf("подключение: %v", err)
	}
	defer c.Close()

	switch cmd {
	case "worker":
		// Размер sticky-кэша в Go SDK — настройка ПРОЦЕССА, а не воркера:
		// в worker.Options такого поля нет. Для стенда это кстати —
		// каждый воркер живёт в отдельном контейнере, и настройка
		// действует ровно на него. Ноль отключает кэш: каждый workflow
		// task начинается с полного replay истории.
		worker.SetStickyWorkflowCacheSize(*cache)

		w := worker.New(c, *queue, worker.Options{})
		w.RegisterWorkflow(StepWorkflow)
		w.RegisterActivity(&provisioning.Activities{})
		log.Printf("[worker] очередь=%s MaxCachedWorkflows=%d", *queue, *cache)
		if err := w.Run(worker.InterruptCh()); err != nil {
			log.Fatalf("воркер: %v", err)
		}

	case "load":
		// Воркфлоу гоняются с ОГРАНИЧЕННЫМ параллелизмом, а не по одному.
		// Причина прозаическая: на Windows-хосте каждый gRPC-раунд стоит
		// сотни миллисекунд, и последовательный прогон растягивает замер
		// на часы. Мерится по-прежнему длительность КАЖДОГО воркфлоу
		// целиком, а параллелизм одинаков во всех конфигурациях —
		// сравнение между ними от этого не страдает.
		const parallel = 5

		durations := make([]time.Duration, *count)
		sem := make(chan struct{}, parallel)
		var wg sync.WaitGroup
		var failed atomic.Value

		for i := 0; i < *count; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				id := fmt.Sprintf("internals-%s-%d-%d", *label, *steps, i)
				start := time.Now()
				run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
					ID: id, TaskQueue: *queue,
				}, StepWorkflow, *steps)
				if err != nil {
					failed.Store(fmt.Errorf("старт %s: %w", id, err))
					return
				}
				var got int
				if err := run.Get(ctx, &got); err != nil {
					failed.Store(fmt.Errorf("результат %s: %w", id, err))
					return
				}
				if got != *steps {
					failed.Store(fmt.Errorf("%s: выполнено %d шагов из %d", id, got, *steps))
					return
				}
				durations[i] = time.Since(start)
			}(i)
		}
		wg.Wait()
		if err, ok := failed.Load().(error); ok && err != nil {
			log.Fatalf("нагрузка: %v", err)
		}

		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		med := durations[len(durations)/2]
		p95 := durations[(len(durations)*95)/100]
		// Формат строки фиксирован: его разбирает скрипт профиля.
		fmt.Printf("ЗАМЕР sticky label=%s cache=%d steps=%d count=%d median_ms=%d p95_ms=%d\n",
			*label, *cache, *steps, *count, med.Milliseconds(), p95.Milliseconds())

	default:
		log.Fatalf("неизвестная подкоманда %q", cmd)
	}
}
