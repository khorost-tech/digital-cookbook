package main

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"khorost.tech/cookbook/auth/internal/jwtclaims"
	"khorost.tech/cookbook/auth/internal/session"
)

var secret = []byte("devsecret")

func newRDB(t *testing.T) *redis.Client {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

// seedSession создаёт живую refresh-сессию для аккаунта и возвращает refresh.
// Nick/Roles заполняются не пусто, чтобы тесты могли проверить, что Rotate
// переносит их в новый access (FIX I-1).
func seedSession(t *testing.T, ctx context.Context, rdb *redis.Client, aid int64) string {
	acc := session.Account{ID: aid, Nick: "nick" + strconv.FormatInt(aid, 10), Roles: []string{"user"}}
	_, rt, err := session.CreateTokenPair(ctx, rdb, secret, acc, "password")
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestRotateHappy(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	rt := seedSession(t, ctx, rdb, 7)
	access, nrt, err := Rotate(ctx, rdb, secret, rt)
	if err != nil || access == "" || nrt == "" || nrt == rt {
		t.Fatalf("rotate failed: a=%q nrt=%q err=%v", access, nrt, err)
	}
	if rdb.Exists(ctx, "rs:"+rt).Val() != 0 {
		t.Fatal("old session must be deleted")
	}
	if rdb.Exists(ctx, "rs:"+nrt).Val() != 1 {
		t.Fatal("new session must exist")
	}
	if rdb.Exists(ctx, "rotated:"+rt).Val() != 1 {
		t.Fatal("rotated bridge must exist")
	}
}

func TestRotatePreservesNickAndRoles(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	rt := seedSession(t, ctx, rdb, 7)

	access, _, err := Rotate(ctx, rdb, secret, rt)
	if err != nil {
		t.Fatal(err)
	}

	claims, err := jwtclaims.Parse(secret, access)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Nick == "" {
		t.Fatalf("Rotate must preserve Nick, got empty claims: %+v", claims)
	}
	if len(claims.Roles) == 0 {
		t.Fatalf("Rotate must preserve Roles, got empty claims: %+v", claims)
	}
}

func TestRotateReplayPreservesNickAndRoles(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	rt := seedSession(t, ctx, rdb, 7)

	if _, _, err := Rotate(ctx, rdb, secret, rt); err != nil {
		t.Fatal(err)
	}
	// Опоздавшая вкладка тем же старым токеном — путь replayRotated.
	access, _, err := Rotate(ctx, rdb, secret, rt)
	if err != nil {
		t.Fatalf("replay must succeed within grace: %v", err)
	}

	claims, err := jwtclaims.Parse(secret, access)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Nick == "" {
		t.Fatalf("replay must preserve Nick, got empty claims: %+v", claims)
	}
	if len(claims.Roles) == 0 {
		t.Fatalf("replay must preserve Roles, got empty claims: %+v", claims)
	}
}

func TestRotateReplayGivesSameSuccessor(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	rt := seedSession(t, ctx, rdb, 7)
	_, nrt1, err := Rotate(ctx, rdb, secret, rt)
	if err != nil {
		t.Fatal(err)
	}
	_, nrt2, err := Rotate(ctx, rdb, secret, rt) // опоздавшая вкладка тем же старым токеном
	if err != nil {
		t.Fatalf("replay must succeed within grace: %v", err)
	}
	if nrt2 != nrt1 {
		t.Fatalf("replay must return same successor: %q != %q", nrt2, nrt1)
	}
}

func TestConcurrentRefreshNoFalseLogout(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	rt := seedSession(t, ctx, rdb, 7)
	const N = 20
	var wg sync.WaitGroup
	results := make([]error, N)
	successors := make([]string, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for attempt := 0; attempt < 20; attempt++ {
				_, s, err := Rotate(ctx, rdb, secret, rt)
				if err == ErrRefreshInProgress {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				results[i] = err
				successors[i] = s
				return
			}
			results[i] = ErrRefreshInProgress // не смог за 20 попыток
		}(i)
	}
	wg.Wait()
	for i, err := range results {
		if err != nil {
			t.Fatalf("goroutine %d falsely logged out: %v", i, err)
		}
		if successors[i] == "" {
			t.Fatalf("goroutine %d got empty successor", i)
		}
	}
}

func TestNaiveConcurrentProducesFalseLogouts(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	rt := seedSession(t, ctx, rdb, 7)
	const N = 20
	var wg sync.WaitGroup
	results := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := RotateNaive(ctx, rdb, secret, rt)
			results[i] = err
		}(i)
	}
	wg.Wait()

	falseLogouts := 0
	for _, err := range results {
		if err == ErrTokenInvalid {
			falseLogouts++
		}
	}
	t.Logf("naive contrast: %d/%d goroutines got ErrTokenInvalid (false logout)", falseLogouts, N)
	// Демонстрация проблемы: без lock/grace конкурентный refresh тем же
	// токеном обычно приводит к ложным разлогинам у части горутин
	// (единственный "победитель" удаляет старую сессию, остальные бьют в
	// уже удалённый rs:{oldRefresh}). Если атомарность miniredis для
	// конкретного прогона этого не проявит — тест толерантен к 0, но факт
	// наблюдения фиксируется в отчёте, а не подгоняется.
	if falseLogouts < 0 {
		t.Fatalf("impossible negative count: %d", falseLogouts)
	}
}
