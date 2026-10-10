// Профиль 07-languages, часть Go. Тот же сценарий, что в четырёх других
// SDK: activity → таймер → ожидание сигнала с таймаутом → компенсация.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"tech.khorost/temporal-cookbook/internal/provisioning"
)

const signalName = "confirm"

// CrossLangWorkflow — эталон сценария. Ровно этот порядок шагов
// воспроизводится на Java, TypeScript, Python и .NET.
//
// Детерминизм в Go-SDK достигается собственной корутинной моделью:
// workflow.Sleep, workflow.NewTimer и workflow.Selector — не обёртки над
// time и select, а конструкции, которые рантайм проигрывает по истории.
func CrossLangWorkflow(ctx workflow.Context, resource string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, provisioning.DefaultActivityOptions())
	var acts *provisioning.Activities

	var rid string
	if err := workflow.ExecuteActivity(ctx, acts.Reserve, resource).Get(ctx, &rid); err != nil {
		return "", err
	}
	if err := workflow.Sleep(ctx, 2*time.Second); err != nil {
		return "", err
	}

	var approved bool
	timerCtx, cancel := workflow.WithCancel(ctx)
	sel := workflow.NewSelector(ctx)
	sel.AddReceive(workflow.GetSignalChannel(ctx, signalName),
		func(c workflow.ReceiveChannel, _ bool) { c.Receive(ctx, &approved) })
	sel.AddFuture(workflow.NewTimer(timerCtx, 60*time.Second), func(workflow.Future) {})
	sel.Select(ctx)
	cancel()

	if approved {
		if err := workflow.ExecuteActivity(ctx, acts.Allocate, rid).Get(ctx, nil); err != nil {
			return "", err
		}
		return "allocated", nil
	}
	if err := workflow.ExecuteActivity(ctx, acts.CancelReservation, rid).Get(ctx, nil); err != nil {
		return "", err
	}
	return "cancelled", nil
}

// sdkVersion — версия SDK из сведений о сборке, а не из go.mod: в лог
// должно попасть то, что реально слинковано.
func sdkVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "неизвестна"
	}
	for _, d := range info.Deps {
		if d.Path == "go.temporal.io/sdk" {
			return d.Version
		}
	}
	return "неизвестна"
}

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		log.Fatal("подкоманда: worker | run | signal")
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	address := fs.String("address", "temporal-frontend:7233", "адрес frontend")
	queue := fs.String("queue", "lang-go-tq", "task queue")
	workflowID := fs.String("workflow", "lang-go", "WorkflowID")
	approve := fs.Bool("approve", false, "подтвердить")
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
		w.RegisterWorkflow(CrossLangWorkflow)
		w.RegisterActivity(&provisioning.Activities{})
		log.Printf("[worker] lang=go sdk=%s queue=%s", sdkVersion(), *queue)
		if err := w.Run(worker.InterruptCh()); err != nil {
			log.Fatalf("воркер: %v", err)
		}

	case "run":
		run, err := c.ExecuteWorkflow(ctx,
			client.StartWorkflowOptions{ID: *workflowID, TaskQueue: *queue},
			CrossLangWorkflow, "gpu-node-7")
		if err != nil {
			log.Fatalf("старт: %v", err)
		}
		time.Sleep(4 * time.Second)
		if err := c.SignalWorkflow(ctx, *workflowID, "", signalName, *approve); err != nil {
			log.Fatalf("signal: %v", err)
		}
		var res string
		if err := run.Get(ctx, &res); err != nil {
			log.Fatalf("результат: %v", err)
		}
		fmt.Printf("ЯЗЫК lang=go sdk=%s queue=%s outcome=%s\n", sdkVersion(), *queue, res)

	default:
		log.Fatalf("неизвестная подкоманда %q", cmd)
	}
}
