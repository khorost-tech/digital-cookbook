// server_test.go — тест поднимает сервер на :0 (свободный порт от ОС), делает
// один реальный запрос и сразу инициирует graceful-shutdown. Доказывает, что
// сконструированный Server обслуживает ответ и что Shutdown завершается штатно.
package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestServerServesAndShutsDown: сервер отдаёт 200, затем корректно гасится.
func TestServerServesAndShutsDown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "pong")
	})

	// Слушатель на :0 — ОС выдаёт свободный порт, тест не конфликтует с чужими.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("не удалось открыть слушатель: %v", err)
	}

	srv := New(ln.Addr().String(), mux)

	// Serve блокирует, поэтому уводим в горутину; ошибку заберём после Shutdown.
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	// Один настоящий HTTP-запрос к живому серверу.
	resp, err := http.Get("http://" + ln.Addr().String() + "/ping")
	if err != nil {
		t.Fatalf("запрос не прошёл: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ожидали 200, получили %d", resp.StatusCode)
	}
	if string(body) != "pong" {
		t.Errorf("ожидали pong, получили %q", body)
	}

	// Немедленный graceful-shutdown с ограниченным сроком.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Shutdown(ctx, srv); err != nil {
		t.Fatalf("shutdown завершился ошибкой: %v", err)
	}

	// После Shutdown Serve возвращает http.ErrServerClosed — это штатный исход.
	if err := <-errCh; err != nil && err != http.ErrServerClosed {
		t.Errorf("ожидали ErrServerClosed, получили %v", err)
	}
}
