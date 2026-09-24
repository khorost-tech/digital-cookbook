package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"khorost.tech/cookbook/auth/internal/jwtclaims"
)

// TestMeLocalValidationNoAuthCalls доказывает, что consumer валидирует
// access-токен ЛОКАЛЬНО: N=100 запросов к /me с валидным Bearer-токеном все
// отвечают 200, и глобальный счётчик authCalls (обращений к auth-сервису)
// остаётся 0.
func TestMeLocalValidationNoAuthCalls(t *testing.T) {
	atomic.StoreInt64(&authCalls, 0)

	sec := []byte("s")
	raw, err := jwtclaims.Issue(sec, jwtclaims.Claims{AccountID: 11, Nick: "eve", Roles: []string{"user"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	h := newRouter(sec)

	const n = 100
	for i := 0; i < n; i++ {
		req := httptest.NewRequest("GET", "/me", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: want 200 got %d", i, rec.Code)
		}
	}

	if got := atomic.LoadInt64(&authCalls); got != 0 {
		t.Fatalf("authCalls = %d, want 0 (consumer must validate locally)", got)
	}
}

func TestMeMissingToken(t *testing.T) {
	h := newRouter([]byte("s"))
	req := httptest.NewRequest("GET", "/me", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", rec.Code)
	}
}

func TestDebugAuthCallsEndpoint(t *testing.T) {
	atomic.StoreInt64(&authCalls, 0)
	h := newRouter([]byte("s"))
	req := httptest.NewRequest("GET", "/debug/auth-calls", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", rec.Code)
	}
	if body := rec.Body.String(); body != `{"auth_calls":0}`+"\n" {
		t.Fatalf("unexpected body: %q", body)
	}
}
