// starter.go — стартер и отправитель сигнала. Это ОТДЕЛЬНЫЕ от воркера
// процессы (клиент Temporal), чтобы демонстрацию можно было проделать руками
// из разных терминалов: запустить воркфлоу, убить/перезапустить воркер,
// затем послать сигнал подтверждения.
package main

import (
	"context"
	"fmt"
	"log"

	"go.temporal.io/sdk/client"
)

// startWorkflow запускает воркфлоу с фиксированным WorkflowID (чтобы send-signal
// мог его адресовать) и БЛОКИРУЕТСЯ, ожидая финальный результат. Пока стартер
// ждёт, вы в другом терминале убиваете и перезапускаете воркер — на итог это
// не влияет: результат придёт, как только воркфлоу дойдёт до конца.
func startWorkflow(hostPort, workflowID, resource string) error {
	c, err := client.Dial(client.Options{HostPort: hostPort})
	if err != nil {
		return err
	}
	defer c.Close()

	run, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: TaskQueue,
	}, WorkflowType, resource)
	if err != nil {
		return fmt.Errorf("не удалось запустить воркфлоу: %w", err)
	}

	log.Printf("[starter] воркфлоу запущен: WorkflowID=%s RunID=%s", run.GetID(), run.GetRunID())
	log.Printf("[starter] сейчас: (1) в терминале воркера убейте его во время паузы/ожидания, (2) запустите воркер снова,")
	log.Printf("[starter]        (3) отправьте подтверждение: go run . send-signal -workflow=%s -approve", workflowID)
	log.Printf("[starter] жду результат воркфлоу (это НЕ мешает убивать/перезапускать воркер)...")

	var result string
	if err := run.Get(context.Background(), &result); err != nil {
		return fmt.Errorf("воркфлоу завершился ошибкой (это ожидаемо для ветки компенсации): %w", err)
	}
	log.Printf("[starter] РЕЗУЛЬТАТ: %s", result)
	return nil
}

// sendSignal шлёт сигнал подтверждения работающему воркфлоу. approve=false
// демонстрирует ветку компенсации (CancelReservation).
func sendSignal(hostPort, workflowID string, approve bool) error {
	c, err := client.Dial(client.Options{HostPort: hostPort})
	if err != nil {
		return err
	}
	defer c.Close()

	sig := ConfirmationSignal{Approved: approve, By: "operator"}
	// Пустой runID → сигнал уходит текущему запуску этого WorkflowID.
	if err := c.SignalWorkflow(context.Background(), workflowID, "", SignalConfirmation, sig); err != nil {
		return fmt.Errorf("не удалось отправить сигнал: %w", err)
	}
	log.Printf("[signal] отправлено %q approved=%v воркфлоу WorkflowID=%s", SignalConfirmation, approve, workflowID)
	return nil
}
