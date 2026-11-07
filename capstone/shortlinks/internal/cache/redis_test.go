package cache

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"shortlinks/internal/links"
)

// slowStore — хранилище, чтение из которого ждёт release или срок контекста.
type slowStore struct {
	release chan struct{}
	reads   atomic.Int64
}

func (s *slowStore) Get(ctx context.Context, code string) (links.Link, error) {
	s.reads.Add(1)
	select {
	case <-s.release:
		return links.Link{Code: code, URL: "https://example.com/" + code}, nil
	case <-ctx.Done():
		return links.Link{}, ctx.Err()
	}
}
func (s *slowStore) Create(context.Context, links.Link) error      { return nil }
func (s *slowStore) Clicks(context.Context, string) (int64, error) { return 0, nil }

// Клиент со своим сроком перестаёт ждать общую загрузку, остальные получают результат одной загрузки.
func TestWaiterLeavesSharedLoadContinues(t *testing.T) {
	st := &slowStore{release: make(chan struct{})}
	r := &Resolver{Store: st, Singleflight: true, LoadTimeout: time.Second}

	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	other := make(chan string)
	go func() { v, _ := r.Resolve(context.Background(), "abc"); other <- v }()
	time.Sleep(5 * time.Millisecond)
	if _, err := r.Resolve(short, "abc"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("клиент со своим сроком ждал дольше него: err=%v", err)
	}
	close(st.release)
	if v := <-other; v != "https://example.com/abc" {
		t.Fatalf("второй клиент не получил общий результат: %q", v)
	}
	if n := st.reads.Load(); n != 1 {
		t.Fatalf("чтений %d, ожидалось одно на всех", n)
	}
}

// У общей загрузки свой срок: зависшее хранилище не держит ожидающих бесконечно.
func TestSharedLoadHasOwnDeadline(t *testing.T) {
	st := &slowStore{release: make(chan struct{})} // никогда не отпускается
	r := &Resolver{Store: st, Singleflight: true, LoadTimeout: 30 * time.Millisecond}
	start := time.Now()
	_, err := r.Resolve(context.Background(), "abc") // у клиента срока нет вовсе
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("загрузка без срока клиента не ограничена своим: err=%v за %s", err, time.Since(start))
	}
}
