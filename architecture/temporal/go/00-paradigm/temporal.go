package main

// Та же задача на Temporal. Разница с двумя предыдущими файлами — в том,
// чего здесь НЕТ: ни таблицы состояний, ни цикла опроса, ни ручного
// таймера, ни подбора незавершённых процессов при старте.

import (
	"context"
	"log"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"tech.khorost/temporal-cookbook/internal/provisioning"
)

func runTemporalWorker(hostPort string) error {
	c, err := client.Dial(client.Options{HostPort: hostPort})
	if err != nil {
		return err
	}
	defer c.Close()

	w := worker.New(c, provisioning.TaskQueue, worker.Options{})
	w.RegisterWorkflow(provisioning.ProvisioningWorkflow)
	w.RegisterActivity(&provisioning.Activities{Latency: 300 * time.Millisecond})

	log.Printf("[worker] запущен, очередь=%q, сервер=%s", provisioning.TaskQueue, hostPort)
	return w.Run(worker.InterruptCh())
}

func startTemporalWorkflow(ctx context.Context, hostPort, workflowID, resource string) error {
	c, err := client.Dial(client.Options{HostPort: hostPort})
	if err != nil {
		return err
	}
	defer c.Close()

	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: provisioning.TaskQueue,
	}, provisioning.ProvisioningWorkflow, provisioning.Request{
		Resource:       resource,
		ConfirmTimeout: 5 * time.Minute,
	})
	if err != nil {
		return err
	}
	log.Printf("[starter] воркфлоу запущен: WorkflowID=%s RunID=%s", run.GetID(), run.GetRunID())
	log.Printf("[starter] жду результат (это НЕ мешает убивать воркер и сервер)...")

	var res provisioning.Result
	if err := run.Get(ctx, &res); err != nil {
		return err
	}
	log.Printf("[starter] РЕЗУЛЬТАТ: outcome=%s reservation=%s", res.Outcome, res.ReservationID)
	return nil
}

func signalTemporal(ctx context.Context, hostPort, workflowID string, approved bool) error {
	c, err := client.Dial(client.Options{HostPort: hostPort})
	if err != nil {
		return err
	}
	defer c.Close()

	return c.SignalWorkflow(ctx, workflowID, "", provisioning.SignalConfirmation,
		provisioning.ConfirmationSignal{Approved: approved, By: "operator"})
}
