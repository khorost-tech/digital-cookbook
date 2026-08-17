package cancel

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestManualCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WaitDone(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ждали context.Canceled, получили %v", err)
	}
}

func TestTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := WaitDone(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ждали context.DeadlineExceeded, получили %v", err)
	}
}

func TestChildCancelledWithParent(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	child, cancelChild := ChildInheritsCancel(parent)
	defer cancelChild()

	cancelParent() // отменяем родителя — дочерний обязан отмениться следом
	if err := WaitDone(child); !errors.Is(err, context.Canceled) {
		t.Fatalf("дочерний контекст не отменился с родителем: %v", err)
	}
}

func TestChildCancelDoesNotAffectParent(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	_, cancelChild := ChildInheritsCancel(parent)

	cancelChild() // отмена ребёнка не должна отменять родителя
	select {
	case <-parent.Done():
		t.Fatal("родитель отменился из-за отмены ребёнка — отмена не распространяется вверх")
	default:
	}
}

func TestEarliestDeadlineWins(t *testing.T) {
	// у родителя дедлайн 20 мс, дочерний просит 1 час — действует родительский
	parent, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	dl, ok := FirstDeadline(parent, time.Hour)
	if !ok {
		t.Fatal("у дочернего контекста нет дедлайна")
	}
	if until := time.Until(dl); until > time.Minute {
		t.Fatalf("действует дочерний дедлайн (%v), а должен родительский ~20мс", until)
	}
}

func TestCancelWithCause(t *testing.T) {
	err, cause := CancelWithCause(context.Background(), ErrShutdown)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx.Err() должен быть context.Canceled, получили %v", err)
	}
	if !errors.Is(cause, ErrShutdown) {
		t.Fatalf("context.Cause должен вернуть ErrShutdown, получили %v", cause)
	}
}
