package main

import (
	"context"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
)

// peekCode вытаскивает код из HASH auth:pending:{email} для теста — эмулирует
// то, что в реальности пользователь получает по email.
func peekCode(t *testing.T, ctx context.Context, rdb *redis.Client, email string) string {
	t.Helper()
	code, err := rdb.HGet(ctx, "auth:pending:"+email, "code").Result()
	if err != nil {
		t.Fatalf("no pending code found for %s: %v", email, err)
	}
	return code
}

func TestSendVerifyCode(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	if err := SendCode(ctx, rdb, "a@b.c"); err != nil {
		t.Fatal(err)
	}
	code := peekCode(t, ctx, rdb, "a@b.c")
	uid, err := VerifyCode(ctx, rdb, "a@b.c", code)
	if err != nil || uid == "" {
		t.Fatalf("verify failed: %v", err)
	}
}

func TestRateLimitPerMinute(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	for i := 0; i < 5; i++ {
		if err := SendCode(ctx, rdb, "x@y.z"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if err := SendCode(ctx, rdb, "x@y.z"); err == nil {
		t.Fatal("6th call must be rate-limited")
	}
}

func TestMaxAttempts(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	if err := SendCode(ctx, rdb, "a@b.c"); err != nil {
		t.Fatal(err)
	}
	// 5 неверных попыток инкрементируют счётчик
	for i := 0; i < 5; i++ {
		if _, err := VerifyCode(ctx, rdb, "a@b.c", "WRONG1"); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("attempt %d: want ErrInvalidCode, got %v", i, err)
		}
	}
	// 6-я — блокировка
	if _, err := VerifyCode(ctx, rdb, "a@b.c", "WRONG1"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("want ErrTooManyAttempts, got %v", err)
	}
}
