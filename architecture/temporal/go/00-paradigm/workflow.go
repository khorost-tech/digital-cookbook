// workflow.go — воркфлоу «провизионинг ресурса» и его activity.
//
// Здесь живёт вся суть durable execution:
//   - КОД воркфлоу (ProvisioningWorkflow) детерминирован: никакого прямого
//     I/O, никаких time.Now()/rand/сети — только через workflow.* API.
//     Именно поэтому Temporal может «переиграть» (replay) историю событий
//     после падения воркера и получить ровно то же состояние.
//   - activity (CheckAvailability/Reserve/Allocate/CancelReservation) — это,
//     наоборот, единственное место, где разрешён побочный эффект (реальное
//     обращение к внешнему миру). Их результат Temporal записывает в историю,
//     и при повторном проигрывании воркфлоу activity НЕ вызываются заново —
//     результат берётся из истории. Это ключ к тому, что перезапуск воркера
//     не выполняет уже сделанную работу дважды.
package main

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	// TaskQueue — общая очередь задач для воркера и стартера этого суб-стенда.
	TaskQueue = "provisioning-tq"
	// SignalConfirmation — имя сигнала «человек в цикле» (подтверждение).
	SignalConfirmation = "confirmation"
	// WorkflowType — фиксированное имя типа воркфлоу (для send-signal/UI).
	WorkflowType = "ProvisioningWorkflow"
)

// ConfirmationSignal — полезная нагрузка сигнала подтверждения.
type ConfirmationSignal struct {
	Approved bool   `json:"approved"` // true — подтверждено, false — явный отказ
	By       string `json:"by"`       // кто подтвердил (для наглядности лога)
}

// activityRuns — СЧЁТЧИК фактических выполнений activity В ПРЕДЕЛАХ ОДНОГО
// ПРОЦЕССА воркера. При перезапуске воркера процесс новый → счётчик снова с
// нуля. Именно этим счётчиком (и логом ниже) доказывается, что после
// перезапуска уже выполненные activity НЕ выполняются повторно: их имён
// просто не будет в логе нового процесса, а результат воркфлоу возьмёт из
// истории Temporal.
var activityRuns atomic.Int64

// ---------------------------------------------------------------------------
// Activities — здесь и только здесь разрешён побочный эффект (I/O).
// ---------------------------------------------------------------------------

// CheckAvailability — «проверить доступность ресурса» (шаг 1).
func CheckAvailability(ctx context.Context, resource string) (bool, error) {
	n := activityRuns.Add(1)
	log.Printf(">>> ACTIVITY CheckAvailability(%q) — РЕАЛЬНОЕ выполнение №%d в этом процессе воркера", resource, n)
	time.Sleep(300 * time.Millisecond) // имитация обращения к внешней системе
	return true, nil
}

// Reserve — «зарезервировать ресурс» (шаг 2). Возвращает id брони.
func Reserve(ctx context.Context, resource string) (string, error) {
	n := activityRuns.Add(1)
	// Детерминизм тут не требуется: это activity, а не код воркфлоу. Но
	// id брони делаем стабильным от имени ресурса, чтобы лог был читаемым.
	reservationID := fmt.Sprintf("res-%s-001", resource)
	log.Printf(">>> ACTIVITY Reserve(%q) — РЕАЛЬНОЕ выполнение №%d → %s", resource, n, reservationID)
	time.Sleep(300 * time.Millisecond)
	return reservationID, nil
}

// Allocate — «выделить (закрепить) ресурс по брони» (шаг 5, успешный путь).
func Allocate(ctx context.Context, reservationID string) (string, error) {
	n := activityRuns.Add(1)
	log.Printf(">>> ACTIVITY Allocate(%q) — РЕАЛЬНОЕ выполнение №%d", reservationID, n)
	time.Sleep(300 * time.Millisecond)
	return fmt.Sprintf("ресурс выделен по брони %s", reservationID), nil
}

// CancelReservation — компенсация: снять бронь, если подтверждения не было
// (таймаут или явный отказ) — шаг 6, ветка отката.
func CancelReservation(ctx context.Context, reservationID string) (string, error) {
	n := activityRuns.Add(1)
	log.Printf(">>> ACTIVITY CancelReservation(%q) — РЕАЛЬНОЕ выполнение №%d (компенсация)", reservationID, n)
	time.Sleep(300 * time.Millisecond)
	return fmt.Sprintf("бронь %s снята", reservationID), nil
}

// ---------------------------------------------------------------------------
// Workflow — ДЕТЕРМИНИРОВАННЫЙ код. Никакого прямого I/O здесь быть не должно.
// ---------------------------------------------------------------------------

// ProvisioningWorkflow оркестрирует провизионинг ресурса:
//
//	CheckAvailability → Reserve → Sleep(имитация ожидания)
//	  → ждём Signal "confirmation" с таймаутом
//	    → Allocate          (если подтверждено)
//	    → CancelReservation  (если таймаут/отказ — компенсация)
//
// Пауза (Sleep) и ожидание сигнала — то самое «окно», в котором мы убиваем
// воркер в демонстрации durability. Таймеры и ожидание сигнала живут на
// стороне СЕРВЕРА Temporal, а не в памяти воркера, поэтому переживают его
// падение.
func ProvisioningWorkflow(ctx workflow.Context, resource string) (string, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("workflow старт", "resource", resource)

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    3,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	// Шаг 1 — доступность.
	var available bool
	if err := workflow.ExecuteActivity(ctx, CheckAvailability, resource).Get(ctx, &available); err != nil {
		return "", fmt.Errorf("CheckAvailability: %w", err)
	}
	if !available {
		return "", fmt.Errorf("ресурс %q недоступен", resource)
	}

	// Шаг 2 — резервирование.
	var reservationID string
	if err := workflow.ExecuteActivity(ctx, Reserve, resource).Get(ctx, &reservationID); err != nil {
		return "", fmt.Errorf("Reserve: %w", err)
	}
	logger.Info("ресурс зарезервирован", "reservationID", reservationID)

	// Шаг 3 — durable-пауза. Даёт «окно», чтобы убить воркер. Таймер живёт на
	// сервере: даже если воркер мёртв дольше, чем длится Sleep, после
	// перезапуска воркфлоу продолжится, а не начнётся заново.
	logger.Info("durable-пауза перед ожиданием подтверждения", "sleep", "15s")
	if err := workflow.Sleep(ctx, 15*time.Second); err != nil {
		return "", err
	}

	// Шаг 4 — ждём сигнал подтверждения ИЛИ таймаут. Ожидание сигнала —
	// тоже состояние на сервере: убить воркер здесь так же безопасно.
	logger.Info("ждём сигнал подтверждения", "signal", SignalConfirmation, "timeout", "5m")
	confirmCh := workflow.GetSignalChannel(ctx, SignalConfirmation)

	var sig ConfirmationSignal
	confirmed := false
	timedOut := false

	timerCtx, cancelTimer := workflow.WithCancel(ctx)
	selector := workflow.NewSelector(ctx)
	selector.AddReceive(confirmCh, func(c workflow.ReceiveChannel, _ bool) {
		c.Receive(ctx, &sig)
		confirmed = sig.Approved
		logger.Info("получен сигнал подтверждения", "approved", sig.Approved, "by", sig.By)
	})
	selector.AddFuture(workflow.NewTimer(timerCtx, 5*time.Minute), func(_ workflow.Future) {
		timedOut = true
		logger.Info("таймаут ожидания подтверждения")
	})
	selector.Select(ctx)
	cancelTimer() // если пришёл сигнал раньше таймера — гасим таймер, чтобы не «висел»

	// Шаг 5/6 — решение.
	if confirmed {
		var result string
		if err := workflow.ExecuteActivity(ctx, Allocate, reservationID).Get(ctx, &result); err != nil {
			return "", fmt.Errorf("Allocate: %w", err)
		}
		logger.Info("workflow завершён успешно", "result", result)
		return result, nil
	}

	// Ветка компенсации: таймаут или явный отказ.
	reason := "явный отказ"
	if timedOut {
		reason = "таймаут ожидания подтверждения"
	}
	var cancelMsg string
	if err := workflow.ExecuteActivity(ctx, CancelReservation, reservationID).Get(ctx, &cancelMsg); err != nil {
		return "", fmt.Errorf("CancelReservation: %w", err)
	}
	return "", fmt.Errorf("провизионинг отменён (%s): %s", reason, cancelMsg)
}
