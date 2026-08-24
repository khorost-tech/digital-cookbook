// middleware_test.go — тесты через httptest доказывают: Recover превращает
// панику обработчика в 500 (процесс не падает), RequestID доносит id через
// context, а Chain применяет слои в объявленном порядке (счётчик Logger растёт).
package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestRecoverTurnsPanicInto500: паника в обработчике перехвачена, ответ — 500,
// а сам тест (процесс) продолжает работать.
func TestRecoverTurnsPanicInto500(t *testing.T) {
	panicky := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("что-то пошло не так")
	})
	h := Recover(panicky)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("ожидали 500, получили %d", rec.Code)
	}
}

// TestRequestIDReachesHandler: id, положенный middleware, доходит до обработчика
// через контекст запроса.
func TestRequestIDReachesHandler(t *testing.T) {
	var got string
	var ok bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = RequestIDFromContext(r.Context())
	})
	h := RequestID("req-123")(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !ok {
		t.Fatal("request-id не найден в контексте")
	}
	if got != "req-123" {
		t.Errorf("ожидали req-123, получили %q", got)
	}
}

// TestChainAppliesAllLayers: Chain склеивает Logger + RequestID + Recover;
// счётчик логгера увеличивается ровно на один запрос, а id всё так же доходит.
func TestChainAppliesAllLayers(t *testing.T) {
	var counter atomic.Int64
	var got string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	h := Chain(inner, Logger(&counter), RequestID("chain-1"), Recover)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if counter.Load() != 1 {
		t.Errorf("ожидали счётчик 1, получили %d", counter.Load())
	}
	if got != "chain-1" {
		t.Errorf("ожидали id chain-1, получили %q", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("ожидали 200, получили %d", rec.Code)
	}
}

// TestMaxBytesRejectsOversizedBody: тело больше лимита → чтение возвращает
// "http: request body too large", обработчик отвечает 413.
func TestMaxBytesRejectsOversizedBody(t *testing.T) {
	const limit = 8
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			if !strings.Contains(err.Error(), "request body too large") {
				t.Errorf("ожидали ошибку про большой размер тела, получили %v", err)
			}
			http.Error(w, "тело слишком большое", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}), MaxBytes(limit))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("это заметно больше восьми байт"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("ожидали 413, получили %d", rec.Code)
	}
}

// TestMaxBytesAllowsSmallBody: тело в пределах лимита проходит, обработчик даёт 200.
func TestMaxBytesAllowsSmallBody(t *testing.T) {
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Fatalf("не ожидали ошибку на малом теле: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}), MaxBytes(1024))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("маленькое тело"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ожидали 200, получили %d", rec.Code)
	}
}
