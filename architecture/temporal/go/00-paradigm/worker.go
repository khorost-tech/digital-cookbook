// worker.go — воркер: процесс, который опрашивает очередь задач и исполняет
// код воркфлоу и activity. Именно ЭТОТ процесс мы будем убивать и
// перезапускать в демонстрации durability.
//
// Важно: воркер НЕ хранит состояние воркфлоу у себя. Состояние (история
// событий, таймеры, ожидание сигналов) живёт на сервере Temporal. Поэтому
// убийство воркера не теряет прогресс — новый воркер подхватит воркфлоу с
// той же точки, «переиграв» историю и продолжив с места паузы/ожидания.
package main

import (
	"log"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// runWorker поднимает клиент к серверу Temporal и запускает воркер на очереди
// TaskQueue, регистрируя воркфлоу и все activity. Блокируется до сигнала
// прерывания (Ctrl+C) или до kill процесса.
func runWorker(hostPort string) error {
	c, err := client.Dial(client.Options{HostPort: hostPort})
	if err != nil {
		return err
	}
	defer c.Close()

	w := worker.New(c, TaskQueue, worker.Options{})

	// Регистрация воркфлоу и activity. Имя типа воркфлоу фиксируем явно, чтобы
	// стартер и Web UI видели стабильное "ProvisioningWorkflow".
	w.RegisterWorkflowWithOptions(ProvisioningWorkflow, workflow.RegisterOptions{Name: WorkflowType})
	w.RegisterActivity(CheckAvailability)
	w.RegisterActivity(Reserve)
	w.RegisterActivity(Allocate)
	w.RegisterActivity(CancelReservation)

	log.Printf("[worker] запущен, очередь=%q, сервер=%s — убейте меня (Ctrl+C) в середине воркфлоу и запустите снова", TaskQueue, hostPort)
	log.Printf("[worker] activity выполняются ТОЛЬКО в этом процессе; после перезапуска уже сделанных activity в логе быть не должно")

	// worker.Run блокируется до InterruptCh; но для демонстрации нам важнее
	// именно жёсткое убийство (kill/Ctrl+C) — оба варианта корректны.
	return w.Run(worker.InterruptCh())
}
