// leak_fixed_test.go: SafeWorker завершается по отмене контекста и не оставляет
// висящих горутин. Проверяем это напрямую через goleak.VerifyNone(t): в конце
// теста goleak сверяет набор горутин и падает, если появилась лишняя.
//
// Прогон: go test ./debugging/...
package debugging

import (
	"context"
	"testing"

	"go.uber.org/goleak"
)

// TestSafeWorkerExitsByCancel: воркер, которому не дали задачу, завершается по
// отмене контекста, а goleak подтверждает отсутствие утечки.
func TestSafeWorkerExitsByCancel(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan struct{}) // отправителя специально нет — как в утечке

	done := SafeWorker(ctx, in)

	// Не шлём задачу, а отменяем контекст — воркер обязан выйти сам.
	cancel()

	// Дожидаемся фактического завершения горутины, иначе goleak может
	// поймать её ещё живой.
	<-done
}

// TestSafeWorkerExitsByTask: тот же воркер завершается и по полученной задаче.
func TestSafeWorkerExitsByTask(t *testing.T) {
	defer goleak.VerifyNone(t)

	in := make(chan struct{}, 1)
	done := SafeWorker(context.Background(), in)

	in <- struct{}{} // отдаём задачу — воркер обрабатывает и выходит
	<-done
}
