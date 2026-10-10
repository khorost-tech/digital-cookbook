// Профиль 04-messaging к статье «Signals, Queries, Updates и child workflows».
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/proto"
)

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		log.Fatal("подкоманда: worker | cart | signal | query | update | parent | history")
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	address := fs.String("address", "temporal-frontend:7233", "адрес frontend")
	queue := fs.String("queue", "messaging-tq", "task queue")
	workflowID := fs.String("workflow", "cart-demo", "WorkflowID")
	withCAN := fs.Bool("can", false, "использовать Continue-As-New")
	maxCycles := fs.Int("max-cycles", 20, "итераций до завершения или Continue-As-New")
	capacity := fs.Int("cap", 1000, "начальный cap")
	amount := fs.Int("amount", 10, "величина для signal")
	newCap := fs.Int("new-cap", 2000, "новое значение cap для update")
	children := fs.Int("children", 5, "число дочерних воркфлоу")
	_ = fs.Parse(args)

	ctx := context.Background()
	c, err := client.Dial(client.Options{HostPort: *address})
	if err != nil {
		log.Fatalf("подключение: %v", err)
	}
	defer c.Close()

	switch cmd {
	case "worker":
		w := worker.New(c, *queue, worker.Options{})
		w.RegisterWorkflow(CartWorkflow)
		w.RegisterWorkflow(ParentWorkflow)
		w.RegisterWorkflow(ChildWorkflow)
		log.Printf("[worker] очередь=%s", *queue)
		if err := w.Run(worker.InterruptCh()); err != nil {
			log.Fatalf("воркер: %v", err)
		}

	case "cart":
		_, err := c.ExecuteWorkflow(ctx,
			client.StartWorkflowOptions{ID: *workflowID, TaskQueue: *queue},
			CartWorkflow, CartState{
				Cap: *capacity, MaxCycles: *maxCycles, WithCAN: *withCAN,
			})
		if err != nil {
			log.Fatalf("старт: %v", err)
		}
		log.Printf("[cart] запущен %s can=%v max-cycles=%d", *workflowID, *withCAN, *maxCycles)

	case "signal":
		if err := c.SignalWorkflow(ctx, *workflowID, "", SignalAdd, *amount); err != nil {
			log.Fatalf("signal: %v", err)
		}

	case "query":
		// Query не пишет в историю: его можно звать сколько угодно,
		// объём истории от этого не растёт.
		val, err := c.QueryWorkflow(ctx, *workflowID, "", QueryTotal)
		if err != nil {
			log.Fatalf("query: %v", err)
		}
		var total int
		if err := val.Get(&total); err != nil {
			log.Fatalf("разбор query: %v", err)
		}
		fmt.Printf("QUERY workflow=%s total=%d\n", *workflowID, total)

	case "update":
		h, err := c.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
			WorkflowID:   *workflowID,
			UpdateName:   UpdateSetCap,
			Args:         []any{*newCap},
			WaitForStage: client.WorkflowUpdateStageCompleted,
		})
		if err != nil {
			// Отклонение валидатором приходит СЮДА, синхронно —
			// в отличие от Signal, который просто уходит в пустоту.
			fmt.Printf("UPDATE workflow=%s new_cap=%d ОТКЛОНЁН: %v\n", *workflowID, *newCap, err)
			return
		}
		var old int
		// Отдельная переменная: писать сюда внешний err значило бы
		// печатать nil вместо настоящей причины отказа.
		if getErr := h.Get(ctx, &old); getErr != nil {
			fmt.Printf("UPDATE workflow=%s new_cap=%d ОТКЛОНЁН: %v\n", *workflowID, *newCap, getErr)
			return
		}
		fmt.Printf("UPDATE workflow=%s new_cap=%d ПРИНЯТ, прежнее значение=%d\n", *workflowID, *newCap, old)

	case "parent":
		run, err := c.ExecuteWorkflow(ctx,
			client.StartWorkflowOptions{ID: *workflowID, TaskQueue: *queue},
			ParentWorkflow, *children)
		if err != nil {
			log.Fatalf("старт: %v", err)
		}
		var res []string
		if err := run.Get(ctx, &res); err != nil {
			log.Fatalf("результат: %v", err)
		}
		fmt.Printf("PARENT workflow=%s children=%d results=%d\n", *workflowID, *children, len(res))

	case "history":
		// Размер истории ТЕКУЩЕГО запуска. При Continue-As-New каждый
		// новый запуск начинает с пустой истории — в этом и смысл.
		iter := c.GetWorkflowHistory(ctx, *workflowID, "", false,
			enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		// Размер считаем через proto.Size — это фактический размер
		// сериализованного protobuf. Прежняя версия брала len(ev.String()),
		// то есть длину ТЕКСТОВОГО представления: величина другого порядка
		// и другой природы, называть её размером истории нельзя.
		events, protoBytes := 0, 0
		for iter.HasNext() {
			ev, err := iter.Next()
			if err != nil {
				log.Fatalf("история: %v", err)
			}
			events++
			protoBytes += proto.Size(ev)
		}
		desc, err := c.DescribeWorkflowExecution(ctx, *workflowID, "")
		if err != nil {
			log.Fatalf("describe: %v", err)
		}
		info := desc.GetWorkflowExecutionInfo()
		mode := "without"
		if *withCAN {
			mode = "with"
		}
		fmt.Printf("ЗАМЕР can mode=%s events=%d proto_bytes=%d status=%s run=%s\n",
			mode, events, protoBytes, info.GetStatus().String(), info.GetExecution().GetRunId())

	default:
		log.Fatalf("неизвестная подкоманда %q", cmd)
	}
}
