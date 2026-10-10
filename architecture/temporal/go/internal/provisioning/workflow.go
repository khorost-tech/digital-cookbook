package provisioning

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// DefaultActivityOptions — единые опции activity для всех профилей, кроме
// профиля 03, который варьирует их намеренно.
//
// StartToCloseTimeout обязателен: без него зависшая activity не будет
// обнаружена никогда, а ретраи по умолчанию бесконечны.
func DefaultActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    3,
		},
	}
}

// ProvisioningWorkflow — эталонный процесс серии:
//
//	CheckAvailability → Reserve → durable-пауза
//	  → ожидание Signal с таймаутом
//	    → Allocate           (подтверждено)
//	    → CancelReservation  (таймаут или отказ)
//
// Пауза и ожидание сигнала — «окно», в котором профили убивают воркер или
// сервер. Таймер и ожидание живут на СЕРВЕРЕ, а не в памяти воркера,
// поэтому переживают его падение.
//
// Код здесь строго детерминирован: ни time.Now(), ни rand, ни прямого I/O.
// Всё внешнее — через activity, результат которой попадает в историю.
func ProvisioningWorkflow(ctx workflow.Context, req Request) (Result, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("workflow старт", "resource", req.Resource)

	var res Result

	// Статус, читаемый снаружи через Query. Query-обработчик ничего не
	// меняет и не пишет в историю — он только отдаёт текущее значение.
	status := "started"
	if err := workflow.SetQueryHandler(ctx, QueryStatus, func() (string, error) {
		return status, nil
	}); err != nil {
		return res, fmt.Errorf("регистрация query-обработчика: %w", err)
	}

	ctx = workflow.WithActivityOptions(ctx, DefaultActivityOptions())
	var acts *Activities

	var available bool
	if err := workflow.ExecuteActivity(ctx, acts.CheckAvailability, req.Resource).Get(ctx, &available); err != nil {
		return res, fmt.Errorf("CheckAvailability: %w", err)
	}
	if !available {
		return res, fmt.Errorf("ресурс %q недоступен", req.Resource)
	}

	status = "checked"

	if err := workflow.ExecuteActivity(ctx, acts.Reserve, req.Resource).Get(ctx, &res.ReservationID); err != nil {
		return res, fmt.Errorf("Reserve: %w", err)
	}
	logger.Info("ресурс зарезервирован", "reservationID", res.ReservationID)
	status = "reserved"

	// Durable-пауза: окно для убийства воркера или сервера.
	logger.Info("durable-пауза перед ожиданием подтверждения", "sleep", "15s")
	if err := workflow.Sleep(ctx, 15*time.Second); err != nil {
		return res, err
	}

	status = "awaiting_confirmation"
	logger.Info("ждём сигнал подтверждения",
		"signal", SignalConfirmation, "timeout", req.ConfirmTimeout.String())

	var sig ConfirmationSignal
	got, timedOut := false, false

	timerCtx, cancelTimer := workflow.WithCancel(ctx)
	sel := workflow.NewSelector(ctx)
	sel.AddReceive(workflow.GetSignalChannel(ctx, SignalConfirmation),
		func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, &sig)
			got = true
			logger.Info("получен сигнал подтверждения", "approved", sig.Approved, "by", sig.By)
		})
	sel.AddFuture(workflow.NewTimer(timerCtx, req.ConfirmTimeout), func(workflow.Future) {
		timedOut = true
		logger.Info("таймаут ожидания подтверждения")
	})
	sel.Select(ctx)
	// Гасим таймер, если сигнал пришёл раньше: иначе он останется висеть
	// в истории до самого срабатывания.
	cancelTimer()

	if got && sig.Approved {
		var msg string
		if err := workflow.ExecuteActivity(ctx, acts.Allocate, res.ReservationID).Get(ctx, &msg); err != nil {
			return res, fmt.Errorf("Allocate: %w", err)
		}
		res.Outcome = "allocated"
		status = "allocated"
		logger.Info("workflow завершён успешно", "result", msg)
		return res, nil
	}

	var msg string
	if err := workflow.ExecuteActivity(ctx, acts.CancelReservation, res.ReservationID).Get(ctx, &msg); err != nil {
		return res, fmt.Errorf("CancelReservation: %w", err)
	}
	if timedOut {
		res.Outcome = "cancelled_timeout"
	} else {
		res.Outcome = "cancelled_rejected"
	}
	status = res.Outcome
	logger.Info("workflow завершён компенсацией", "outcome", res.Outcome, "result", msg)
	return res, nil
}
