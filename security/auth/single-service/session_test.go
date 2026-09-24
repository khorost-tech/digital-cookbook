package main

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newRDB(t *testing.T) *redis.Client {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	sid, err := CreateSession(ctx, rdb, "u1")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := ValidateAndTouch(ctx, rdb, sid)
	if err != nil || uid != "u1" {
		t.Fatalf("uid=%q err=%v", uid, err)
	}
	if err := DeleteSession(ctx, rdb, sid, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateAndTouch(ctx, rdb, sid); err == nil {
		t.Fatal("session must be gone after delete")
	}
}

func TestListSelfCleansStale(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	sid, _ := CreateSession(ctx, rdb, "u1")
	rdb.Del(ctx, "s:"+sid) // истёк HASH, ссылка в su: осталась
	views, err := ListSessions(ctx, rdb, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 0 {
		t.Fatalf("stale not cleaned: %d", len(views))
	}
	if n, _ := rdb.SCard(ctx, "su:u1").Result(); n != 0 {
		t.Fatalf("su: still has %d members", n)
	}
}

func TestDeleteForeignSessionRejected(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	sid, err := CreateSession(ctx, rdb, "u1")
	if err != nil {
		t.Fatal(err)
	}

	if err := DeleteSession(ctx, rdb, sid, "u2"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("want ErrNoSession, got %v", err)
	}

	uid, err := ValidateAndTouch(ctx, rdb, sid)
	if err != nil || uid != "u1" {
		t.Fatalf("session u1 must survive foreign delete attempt: uid=%q err=%v", uid, err)
	}
}
