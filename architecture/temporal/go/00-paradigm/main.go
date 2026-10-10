// Профиль 00-paradigm к статье «Durable execution: почему код должен
// переживать падения».
//
// Один и тот же процесс тремя способами:
//
//	naive        — состояние в памяти процесса
//	statemachine — состояние в таблице Postgres, механика руками
//	temporal     — durable execution
//
// Все три ломаются одинаково: убийством процесса в середине. Разница —
// в том, что происходит дальше.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"
)

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		log.Fatal("подкоманда: naive | statemachine | temporal-worker | temporal-start | temporal-signal")
	}
	ctx := context.Background()
	cmd, args := os.Args[1], os.Args[2:]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	address := fs.String("address", "temporal-frontend:7233", "адрес frontend Temporal")
	dsn := fs.String("dsn", "postgres://temporal:temporal@postgres:5432/temporal?sslmode=disable", "DSN Postgres")
	resource := fs.String("resource", "gpu-node-7", "имя ресурса")
	workflowID := fs.String("workflow", "provisioning-demo", "WorkflowID / идентификатор процесса")
	pause := fs.Duration("pause", 15*time.Second, "длительность паузы в середине процесса")
	approve := fs.Bool("approve", false, "подтвердить (для temporal-signal)")
	_ = fs.Parse(args)

	var err error
	switch cmd {
	case "naive":
		err = runNaive(ctx, *resource, *pause)
	case "statemachine":
		err = runStateMachine(ctx, *dsn, *workflowID, *resource, *pause)
	case "temporal-worker":
		err = runTemporalWorker(*address)
	case "temporal-start":
		err = startTemporalWorkflow(ctx, *address, *workflowID, *resource)
	case "temporal-signal":
		err = signalTemporal(ctx, *address, *workflowID, *approve)
	default:
		log.Fatalf("неизвестная подкоманда %q", cmd)
	}
	if err != nil {
		log.Fatalf("[%s] ОШИБКА: %v", cmd, err)
	}
}
