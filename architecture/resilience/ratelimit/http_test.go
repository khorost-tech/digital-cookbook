package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestMiddleware429 — отклонённый запрос получает 429 и Retry-After, а не тихую
// потерю. Лимитер с одним токеном: первый запрос проходит, второй — отбит.
func TestMiddleware429(t *testing.T) {
	ok := http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})
	// FixedWindow(1, 1s) с фиксированными часами (base): оба запроса в одном окне,
	// поэтому исход детерминирован — первый проходит, второй отбит.
	clock := func() time.Time { return base }
	h := middlewareWithClock(ok, NewFixedWindow(1, time.Second), clock)

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec1.Code != http.StatusOK {
		t.Fatalf("первый запрос: код %d, ждали 200", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("второй запрос: код %d, ждали 429", rec2.Code)
	}
	if ra := rec2.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("Retry-After отсутствует или ноль: %q", ra)
	}
}
