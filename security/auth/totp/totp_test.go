package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
)

func newRDB(t *testing.T) *redis.Client {
	rdb, _ := newRDBWithMiniredis(t)
	return rdb
}

// newRDBWithMiniredis отдаёт ещё и сам *miniredis.Miniredis — нужен тестам,
// которым надо промотать время (FastForward) и проверить истечение TTL.
func newRDBWithMiniredis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()}), mr
}

func TestEnrollAndVerify(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	secret, uri, recovery, err := Enroll(ctx, rdb, "u1")
	if err != nil || secret == "" || !strings.HasPrefix(uri, "otpauth://totp/") || len(recovery) == 0 {
		t.Fatalf("enroll: secret=%q uri=%q rec=%d err=%v", secret, uri, len(recovery), err)
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	ok, err := Verify(ctx, rdb, "u1", code)
	if err != nil || !ok {
		t.Fatalf("verify current code: ok=%v err=%v", ok, err)
	}
}

func TestVerifySkewWindow(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	secret, _, _, _ := Enroll(ctx, rdb, "u1")
	near, _ := totp.GenerateCode(secret, time.Now().Add(-30*time.Second))
	far, _ := totp.GenerateCode(secret, time.Now().Add(-120*time.Second))
	if ok, _ := Verify(ctx, rdb, "u1", near); !ok {
		t.Fatal("adjacent-window code must pass")
	}
	if ok, _ := Verify(ctx, rdb, "u1", far); ok {
		t.Fatal("far code must fail")
	}
}

func TestVerifyRejectsReplay(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	secret, _, _, _ := Enroll(ctx, rdb, "u1")
	code, _ := totp.GenerateCode(secret, time.Now())

	ok, err := Verify(ctx, rdb, "u1", code)
	if err != nil || !ok {
		t.Fatalf("first verify must succeed: ok=%v err=%v", ok, err)
	}

	ok, err = Verify(ctx, rdb, "u1", code)
	if err != nil {
		t.Fatalf("replay verify errored: %v", err)
	}
	if ok {
		t.Fatal("replayed code must be rejected")
	}
}

func TestRecoverySingleUse(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	_, _, recovery, _ := Enroll(ctx, rdb, "u1")
	code := recovery[0]
	if ok, _ := UseRecovery(ctx, rdb, "u1", code); !ok {
		t.Fatal("first use must succeed")
	}
	if ok, _ := UseRecovery(ctx, rdb, "u1", code); ok {
		t.Fatal("second use must fail")
	}
}

func TestStepUp(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	if ok, _ := RequireStepUp(ctx, rdb, "sess1"); ok {
		t.Fatal("no step-up yet")
	}
	if err := MarkStepUp(ctx, rdb, "sess1"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := RequireStepUp(ctx, rdb, "sess1"); !ok {
		t.Fatal("step-up must be active after mark")
	}
}

func TestStepUpExpires(t *testing.T) {
	ctx := context.Background()
	rdb, mr := newRDBWithMiniredis(t)

	if err := MarkStepUp(ctx, rdb, "sess1"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := RequireStepUp(ctx, rdb, "sess1"); !ok {
		t.Fatal("step-up must be active right after mark")
	}

	mr.FastForward(6 * time.Minute)

	if ok, _ := RequireStepUp(ctx, rdb, "sess1"); ok {
		t.Fatal("step-up must expire after stepupTTL")
	}
}

// TestVerifyRateLimited проверяет анти-брутфорс /totp/verify (FIX N-1):
// после totpRLLimit неудачных попыток окно totp:rl:{userID} блокирует ЛЮБОЙ
// код — в т.ч. корректный — до истечения окна (документированное поведение:
// см. комментарий к ErrTOTPRateLimited).
func TestVerifyRateLimited(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	secret, _, _, _ := Enroll(ctx, rdb, "u1")

	valid, _ := totp.GenerateCode(secret, time.Now())
	wrong := flipLastDigit(valid)

	for i := 0; i < totpRLLimit; i++ {
		ok, err := Verify(ctx, rdb, "u1", wrong)
		if err != nil {
			t.Fatalf("attempt %d: unexpected error: %v", i, err)
		}
		if ok {
			t.Fatalf("attempt %d: wrong code must not verify", i)
		}
	}

	// Лимит исчерпан: даже верный код теперь отклоняется — ErrTOTPRateLimited,
	// а не обычное "invalid code" (ok=false, err=nil).
	ok, err := Verify(ctx, rdb, "u1", valid)
	if !errors.Is(err, ErrTOTPRateLimited) {
		t.Fatalf("expected ErrTOTPRateLimited, got ok=%v err=%v", ok, err)
	}
	if ok {
		t.Fatal("rate-limited verify must not succeed even with the correct code")
	}
}

// TestVerifyRateLimitResetsOnSuccess проверяет, что успешный verify
// сбрасывает счётчик неудачных попыток — легитимный пользователь, однажды
// ошибившись, не остаётся заблокированным до конца окна после того, как
// ввёл верный код.
func TestVerifyRateLimitResetsOnSuccess(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	secret, _, _, _ := Enroll(ctx, rdb, "u1")

	valid, _ := totp.GenerateCode(secret, time.Now())
	wrong := flipLastDigit(valid)

	if ok, _ := Verify(ctx, rdb, "u1", wrong); ok {
		t.Fatal("wrong code must not verify")
	}
	if ok, err := Verify(ctx, rdb, "u1", valid); err != nil || !ok {
		t.Fatalf("valid code after a single failed attempt must succeed: ok=%v err=%v", ok, err)
	}

	n, err := rdb.Get(ctx, totpRLKey("u1")).Result()
	if !errors.Is(err, redis.Nil) {
		t.Fatalf("rate limit counter must be reset after success: n=%q err=%v", n, err)
	}
}

func flipLastDigit(code string) string {
	b := []byte(code)
	last := len(b) - 1
	if b[last] == '0' {
		b[last] = '1'
	} else {
		b[last] = '0'
	}
	return string(b)
}

func TestReEnrollInvalidatesOldRecovery(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)

	_, _, recovery1, err := Enroll(ctx, rdb, "u1")
	if err != nil {
		t.Fatal(err)
	}
	old := recovery1[0]

	if _, _, _, err := Enroll(ctx, rdb, "u1"); err != nil {
		t.Fatalf("re-enroll: %v", err)
	}

	if ok, _ := UseRecovery(ctx, rdb, "u1", old); ok {
		t.Fatal("recovery code from previous enrollment must be invalid after re-enroll")
	}
}
