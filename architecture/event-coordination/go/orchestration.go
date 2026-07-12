package main

// orchestration.go — ОРКЕСТРАЦИЯ через Temporal.
//
// Весь сценарий заказа собран в ОДНОЙ функции OrderWorkflow: шаги идут по
// порядку, ошибка каждого обрабатывается явно (return err — Temporal при
// падении activity сам ретраит по политике, а неустранимую ошибку вернёт в
// workflow). Чтобы понять «что происходит с заказом», достаточно прочитать эту
// функцию сверху вниз — весь поток здесь, в одном месте.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// OrderWorkflow — явный сценарий оформления заказа. ВЕСЬ поток читается здесь:
// оплата → резерв → доставка → уведомление, строго по порядку, с явной
// обработкой ошибки на каждом шаге.
func OrderWorkflow(ctx workflow.Context, order Order) (OrderResult, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3, // при сбое шага Temporal сам ретраит — и это видно в UI
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	res := OrderResult{OrderID: order.ID}

	// Шаг 1 — оплата.
	if err := workflow.ExecuteActivity(ctx, TakePayment, order).Get(ctx, &res.PaymentID); err != nil {
		return res, fmt.Errorf("оплата не прошла: %w", err)
	}
	// Шаг 2 — резерв товара.
	if err := workflow.ExecuteActivity(ctx, ReserveStock, order).Get(ctx, &res.ReservationID); err != nil {
		return res, fmt.Errorf("резерв не удался: %w", err)
	}
	// Шаг 3 — создание доставки.
	if err := workflow.ExecuteActivity(ctx, CreateShipment, order).Get(ctx, &res.ShipmentID); err != nil {
		return res, fmt.Errorf("доставка не создана: %w", err)
	}
	// Шаг 4 — уведомление клиента.
	if err := workflow.ExecuteActivity(ctx, NotifyCustomer, order).Get(ctx, &res.Notification); err != nil {
		return res, fmt.Errorf("уведомление не отправлено: %w", err)
	}

	return res, nil
}

// --- Activities: по одной на каждый шаг. Оборачивают общую бизнес-логику do*. --
//
// Каждая печатает свой шаг — так в консоли виден порядок исполнения. Порядок
// задаётся ИСКЛЮЧИТЕЛЬНО кодом workflow выше (а не тем, кто на что подписан, как
// в хореографии).

func TakePayment(ctx context.Context, o Order) (string, error) {
	id := doTakePayment(o)
	fmt.Printf("  [шаг 1/4] оплата           -> %s\n", id)
	return id, nil
}

func ReserveStock(ctx context.Context, o Order) (string, error) {
	id := doReserveStock(o)
	fmt.Printf("  [шаг 2/4] резерв товара    -> %s\n", id)
	return id, nil
}

func CreateShipment(ctx context.Context, o Order) (string, error) {
	id := doCreateShipment(o)
	fmt.Printf("  [шаг 3/4] создание доставки-> %s\n", id)
	return id, nil
}

func NotifyCustomer(ctx context.Context, o Order) (string, error) {
	msg := doNotifyCustomer(o)
	fmt.Printf("  [шаг 4/4] уведомление      -> %s\n", msg)
	return msg, nil
}

// runOrchestration поднимает клиент+воркер, запускает OrderWorkflow и дожидается
// результата. Возвращает итог заказа для сравнения с хореографией.
func runOrchestration(hostPort string, order Order) (OrderResult, error) {
	line := strings.Repeat("=", 78)
	fmt.Println(line)
	fmt.Println("ОРКЕСТРАЦИЯ (Temporal): весь сценарий — в одной функции OrderWorkflow")
	fmt.Println(line)

	c, err := dialTemporal(hostPort)
	if err != nil {
		return OrderResult{}, fmt.Errorf("подключение к Temporal (%s): %w", hostPort, err)
	}
	defer c.Close()

	w, err := startWorker(c)
	if err != nil {
		return OrderResult{}, fmt.Errorf("запуск воркера: %w", err)
	}
	defer w.Stop()

	workflowID := "order-" + order.ID
	fmt.Printf("запускаю workflow id=%s на очереди %q\n", workflowID, taskQueue)
	fmt.Println("(статус и история шагов — в Temporal UI: http://localhost:8233)")

	we, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: taskQueue,
	}, OrderWorkflow, order)
	if err != nil {
		return OrderResult{}, fmt.Errorf("ExecuteWorkflow: %w", err)
	}

	var res OrderResult
	if err := we.Get(context.Background(), &res); err != nil {
		return OrderResult{}, fmt.Errorf("workflow завершился ошибкой: %w", err)
	}

	fmt.Printf("workflow завершён (RunID=%s)\n", we.GetRunID())
	fmt.Println("итог:", res)
	fmt.Println()
	return res, nil
}
