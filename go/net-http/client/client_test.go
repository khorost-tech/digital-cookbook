// client_test.go — тесты против httptest.Server (реальный сервер на локальном
// адресе, но управляемый тестом). Доказывают: обычный GET возвращает тело;
// отмена context прерывает запрос к медленному серверу с ошибкой контекста.
package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestGetReturnsBody: запрос к httptest-серверу возвращает его ответ.
func TestGetReturnsBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	defer ts.Close()

	c := New(2 * time.Second)
	body, err := Get(context.Background(), c, ts.URL)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if string(body) != "hello" {
		t.Errorf("ожидали hello, получили %q", body)
	}
}

// TestGetCancelledByContext: сервер отвечает с задержкой, но контекст с коротким
// дедлайном отменяет запрос раньше — Get возвращает ошибку контекста.
func TestGetCancelledByContext(t *testing.T) {
	// Сервер держит ответ дольше, чем дедлайн контекста; блокировка снимается
	// по r.Context().Done(), чтобы не задерживать закрытие сервера.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			_, _ = io.WriteString(w, "too late")
		case <-r.Context().Done():
		}
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	c := New(5 * time.Second) // таймаут клиента заведомо больше дедлайна ctx
	_, err := Get(ctx, c, ts.URL)
	if err == nil {
		t.Fatal("ожидали ошибку отмены, получили nil")
	}
	// Ошибка транспорта оборачивает context.DeadlineExceeded — проверяем сквозь
	// цепочку через errors.Is.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ожидали DeadlineExceeded, получили %v", err)
	}
}
