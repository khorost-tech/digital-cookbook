package main

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRDB(t *testing.T) *redis.Client {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func TestOTPHappy(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRDB(t)
	email := "a@b.c"

	if err := RequestOTP(ctx, rdb, email); err != nil {
		t.Fatalf("RequestOTP failed: %v", err)
	}

	code, err := rdb.HGet(ctx, "auth:"+email, "code").Result()
	if err != nil {
		t.Fatalf("HGet code failed: %v", err)
	}
	if code == "" {
		t.Fatal("empty code")
	}

	aid, err := VerifyOTP(ctx, rdb, email, code)
	if err != nil {
		t.Fatalf("VerifyOTP failed: %v", err)
	}
	if aid == 0 {
		t.Fatal("accountID must not be zero")
	}
}

func TestOTPMaxAttempts(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRDB(t)
	email := "attempts@example.com"

	if err := RequestOTP(ctx, rdb, email); err != nil {
		t.Fatalf("RequestOTP failed: %v", err)
	}

	for i := 0; i < 5; i++ {
		_, err := VerifyOTP(ctx, rdb, email, "WRONG-CODE")
		if !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("attempt %d: want ErrInvalidCode, got %v", i, err)
		}
	}

	_, err := VerifyOTP(ctx, rdb, email, "WRONG-CODE")
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("6th attempt: want ErrTooManyAttempts, got %v", err)
	}
}

func TestOTPRateLimit(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRDB(t)
	email := "rl@example.com"

	for i := 0; i < 3; i++ {
		if err := RequestOTP(ctx, rdb, email); err != nil {
			t.Fatalf("request %d: unexpected error: %v", i, err)
		}
	}

	err := RequestOTP(ctx, rdb, email)
	if !errors.Is(err, ErrOTPRateLimited) {
		t.Fatalf("4th request: want ErrOTPRateLimited, got %v", err)
	}
}
