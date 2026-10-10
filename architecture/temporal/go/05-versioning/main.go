// Профиль 05-versioning к статье «Версионирование workflow».
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"tech.khorost/temporal-cookbook/internal/provisioning"
)

const workflowName = "VersionedWorkflow"

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		log.Fatal("подкоманда: worker | start | status")
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	address := fs.String("address", "temporal-frontend:7233", "адрес frontend")
	queue := fs.String("queue", "versioning-tq", "task queue")
	version := fs.String("version", "v1", "v1 | v2 | v3 | v4")
	workflowID := fs.String("workflow", "versioning-demo", "WorkflowID")
	_ = fs.Parse(args)

	ctx := context.Background()
	c, err := client.Dial(client.Options{HostPort: *address})
	if err != nil {
		log.Fatalf("подключение: %v", err)
	}
	defer c.Close()

	switch cmd {
	case "worker":
		var fn any
		switch *version {
		case "v1":
			fn = WorkflowV1
		case "v2":
			fn = WorkflowV2Unpatched
		case "v3":
			fn = WorkflowV3Patched
		case "v4":
			fn = WorkflowV4PatchRemoved
		default:
			log.Fatalf("неизвестная версия %q", *version)
		}
		w := worker.New(c, *queue, worker.Options{})
		w.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{Name: workflowName})
		w.RegisterActivity(&provisioning.Activities{})
		log.Printf("[worker] версия=%s очередь=%s", *version, *queue)
		if err := w.Run(worker.InterruptCh()); err != nil {
			log.Fatalf("воркер: %v", err)
		}

	case "start":
		run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
			ID: *workflowID, TaskQueue: *queue,
		}, workflowName, "gpu-node-7")
		if err != nil {
			log.Fatalf("старт: %v", err)
		}
		log.Printf("[start] %s RunID=%s", run.GetID(), run.GetRunID())

	case "status":
		// Статус и попытка текущей workflow task. Non-determinism error
		// НЕ завершает воркфлоу: он остаётся Running, а workflow task
		// падает по кругу с растущим номером попытки. Это важное отличие
		// от «упало и умерло»: процесс жив и ждёт исправления кода.
		desc, err := c.DescribeWorkflowExecution(ctx, *workflowID, "")
		if err != nil {
			log.Fatalf("describe: %v", err)
		}
		info := desc.GetWorkflowExecutionInfo()
		attempt := int32(0)
		if p := desc.GetPendingWorkflowTask(); p != nil {
			attempt = p.GetAttempt()
		}
		fmt.Printf("СТАТУС workflow=%s status=%s pending_task_attempt=%d\n",
			*workflowID, info.GetStatus().String(), attempt)

	default:
		log.Fatalf("неизвестная подкоманда %q", cmd)
	}
}
