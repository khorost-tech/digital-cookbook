// Суб-стенд 00-paradigm серии «Temporal: durable execution вглубь».
//
// Демонстрирует главное свойство Temporal — durable execution: выполнение
// воркфлоу ПЕРЕЖИВАЕТ падение воркера. Воркфлоу «провизионинг ресурса»
// (CheckAvailability → Reserve → durable-пауза → ожидание Signal-подтверждения
// с таймаутом → Allocate, либо компенсация CancelReservation) можно прервать,
// убив процесс воркера в середине, и продолжить с той же точки, перезапустив
// воркер: уже выполненные activity НЕ выполняются заново (их результат берётся
// из истории на сервере Temporal).
//
// Три подкоманды — по одному процессу на каждую, чтобы демонстрацию можно
// было проделать руками из разных терминалов:
//
//	go run . start-worker                          # воркер (его убиваем/перезапускаем)
//	go run . start-workflow [-resource=gpu-node-7] # запуск воркфлоу (блокируется до итога)
//	go run . send-signal   [-approve]              # подтверждение (человек в цикле)
//
// Все процессы по умолчанию ходят на localhost:7253 (см. temporal/compose).
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

const defaultHostPort = "localhost:7253"

func main() {
	log.SetFlags(log.Ltime)

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "start-worker":
		fs := flag.NewFlagSet("start-worker", flag.ExitOnError)
		hostPort := fs.String("address", defaultHostPort, "адрес frontend Temporal (gRPC)")
		_ = fs.Parse(args)
		if err := runWorker(*hostPort); err != nil {
			log.Fatalf("[worker] ошибка: %v", err)
		}

	case "start-workflow":
		fs := flag.NewFlagSet("start-workflow", flag.ExitOnError)
		hostPort := fs.String("address", defaultHostPort, "адрес frontend Temporal (gRPC)")
		workflowID := fs.String("workflow", "provisioning-demo", "WorkflowID")
		resource := fs.String("resource", "gpu-node-7", "имя провизионируемого ресурса")
		_ = fs.Parse(args)
		if err := startWorkflow(*hostPort, *workflowID, *resource); err != nil {
			log.Fatalf("[starter] %v", err)
		}

	case "send-signal":
		fs := flag.NewFlagSet("send-signal", flag.ExitOnError)
		hostPort := fs.String("address", defaultHostPort, "адрес frontend Temporal (gRPC)")
		workflowID := fs.String("workflow", "provisioning-demo", "WorkflowID")
		approve := fs.Bool("approve", false, "true — подтвердить (Allocate), false — отказ (компенсация)")
		_ = fs.Parse(args)
		if err := sendSignal(*hostPort, *workflowID, *approve); err != nil {
			log.Fatalf("[signal] %v", err)
		}

	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `00-paradigm — durable execution в Temporal.

Использование:
  go run . start-worker    [-address=host:port]
  go run . start-workflow  [-address=host:port] [-workflow=ID] [-resource=name]
  go run . send-signal     [-address=host:port] [-workflow=ID] [-approve]

Сценарий демонстрации durability — см. temporal/README.md.`)
}
