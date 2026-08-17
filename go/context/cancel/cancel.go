// Package cancel показывает три источника отмены context: ручной cancel,
// таймаут и дедлайн, а также распространение отмены по дереву контекстов
// и передачу причины отмены (WithCancelCause / context.Cause).
package cancel

import (
	"context"
	"errors"
	"time"
)

// WaitDone блокируется, пока контекст не будет отменён, и возвращает
// ctx.Err(): context.Canceled при ручной отмене, context.DeadlineExceeded
// при истечении дедлайна/таймаута.
func WaitDone(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// ChildInheritsCancel строит дочерний контекст от родителя и возвращает
// оба Done-канала. Отмена родителя закрывает и дочерний Done — отмена
// распространяется вниз по дереву, но не вверх.
func ChildInheritsCancel(parent context.Context) (child context.Context, cancelChild context.CancelFunc) {
	return context.WithCancel(parent)
}

// FirstDeadline демонстрирует, что при вложенных дедлайнах срабатывает
// самый ранний: дочерний контекст не может «продлить» родительский.
// Возвращает дедлайн, который реально действует на child.
func FirstDeadline(parent context.Context, childTimeout time.Duration) (time.Time, bool) {
	child, cancel := context.WithTimeout(parent, childTimeout)
	defer cancel()
	return child.Deadline()
}

// ErrShutdown — причина отмены, которую передаём через WithCancelCause.
var ErrShutdown = errors.New("плановая остановка")

// CancelWithCause отменяет контекст с явной причиной. ctx.Err() при этом
// остаётся context.Canceled, а context.Cause(ctx) вернёт переданную причину —
// удобно отличать «отменили из-за X» от «отменили из-за Y».
func CancelWithCause(parent context.Context, cause error) (err, causeErr error) {
	ctx, cancel := context.WithCancelCause(parent)
	cancel(cause)
	<-ctx.Done()
	return ctx.Err(), context.Cause(ctx)
}
