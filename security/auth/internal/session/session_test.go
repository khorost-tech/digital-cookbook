package session

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"khorost.tech/cookbook/auth/internal/jwtclaims"
)

func newRDB(t *testing.T) *redis.Client {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func TestCreateTokenPair(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	sec := []byte("secret")

	acc := Account{ID: 42, Nick: "bob", Roles: []string{"user"}}
	access, refresh, err := CreateTokenPair(ctx, rdb, sec, acc, "password")
	if err != nil {
		t.Fatal(err)
	}
	if access == "" || refresh == "" {
		t.Fatalf("empty tokens: access=%q refresh=%q", access, refresh)
	}

	// access parses and carries the right claims
	claims, err := jwtclaims.Parse(sec, access)
	if err != nil {
		t.Fatal(err)
	}
	if claims.AccountID != 42 || claims.Nick != "bob" {
		t.Fatalf("claims=%+v", claims)
	}

	// rs:{refresh} exists with matching aid
	data, err := rdb.HGetAll(ctx, "rs:"+refresh).Result()
	if err != nil {
		t.Fatal(err)
	}
	if data["aid"] != "42" {
		t.Fatalf("rs: aid=%q want 42, data=%v", data["aid"], data)
	}
	if data["lm"] != "password" {
		t.Fatalf("rs: lm=%q want password", data["lm"])
	}
	if data["nick"] != "bob" {
		t.Fatalf("rs: nick=%q want bob", data["nick"])
	}
	if data["roles"] != `["user"]` {
		t.Fatalf("rs: roles=%q want [\"user\"]", data["roles"])
	}

	// index rsu:{aid} has the refresh as a field
	exists, err := rdb.HExists(ctx, "rsu:42", refresh).Result()
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("rsu:42 must contain refresh field")
	}

	// AccountFromSession restores the full Account, incl. nick/roles
	acc2, err := AccountFromSession(ctx, rdb, refresh)
	if err != nil {
		t.Fatal(err)
	}
	if acc2.ID != 42 || acc2.Nick != "bob" || len(acc2.Roles) != 1 || acc2.Roles[0] != "user" {
		t.Fatalf("AccountFromSession=%+v want {42 bob [user]}", acc2)
	}
}

func TestAccountFromSessionMissing(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)

	if _, err := AccountFromSession(ctx, rdb, "does-not-exist"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("want ErrNoSession, got %v", err)
	}
}

func TestValidateAndTouch(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	sec := []byte("secret")

	acc := Account{ID: 7, Nick: "alice"}
	_, refresh, err := CreateTokenPair(ctx, rdb, sec, acc, "password")
	if err != nil {
		t.Fatal(err)
	}

	aid, err := ValidateAndTouch(ctx, rdb, refresh)
	if err != nil || aid != 7 {
		t.Fatalf("aid=%d err=%v", aid, err)
	}
}

func TestValidateAndTouchMissing(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)

	if _, err := ValidateAndTouch(ctx, rdb, "does-not-exist"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("want ErrNoSession, got %v", err)
	}
}

func TestListSessions(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	sec := []byte("secret")

	acc := Account{ID: 99, Nick: "carol"}
	_, r1, err := CreateTokenPair(ctx, rdb, sec, acc, "password")
	if err != nil {
		t.Fatal(err)
	}
	_, r2, err := CreateTokenPair(ctx, rdb, sec, acc, "password")
	if err != nil {
		t.Fatal(err)
	}

	views, err := ListSessions(ctx, rdb, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("want 2 sessions, got %d: %+v", len(views), views)
	}
	seen := map[string]bool{}
	for _, v := range views {
		seen[v.Refresh] = true
	}
	if !seen[r1] || !seen[r2] {
		t.Fatalf("missing refresh in views: %+v", views)
	}
}

func TestListSessionsSelfCleansStale(t *testing.T) {
	ctx := context.Background()
	rdb := newRDB(t)
	sec := []byte("secret")

	acc := Account{ID: 5, Nick: "dave"}
	_, refresh, err := CreateTokenPair(ctx, rdb, sec, acc, "password")
	if err != nil {
		t.Fatal(err)
	}
	rdb.Del(ctx, "rs:"+refresh) // истёк HASH, ссылка в rsu: осталась

	views, err := ListSessions(ctx, rdb, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 0 {
		t.Fatalf("stale not cleaned: %+v", views)
	}
	if n, _ := rdb.HLen(ctx, "rsu:5").Result(); n != 0 {
		t.Fatalf("rsu: still has %d fields", n)
	}
}
