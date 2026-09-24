// Command consumer — сервис-потребитель, демонстрирующий локальную валидацию
// access-токена: он проверяет JWT общим middleware (multi-service/authmw) по
// общему JWT_SECRET и НЕ ходит в auth-сервис на каждый запрос. authCalls
// доказывает это: он инкрементируется только тогда, когда consumer сам решает
// сходить в auth (здесь — никогда, локальная валидация самодостаточна).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"khorost.tech/cookbook/auth/multi-service/authmw"
)

// authCalls — счётчик обращений consumer-а к auth-сервису за подтверждением
// токена. При ЛОКАЛЬНОЙ валидации (jwtclaims.Parse внутри authmw.Middleware)
// он НЕ инкрементируется — существует ровно чтобы это доказать в тестах и в
// бенче: consumer никуда не ходит, кроме собственной памяти.
var authCalls int64

func newRouter(secret []byte) http.Handler {
	r := chi.NewRouter()

	r.Group(func(r chi.Router) {
		r.Use(authmw.Middleware(secret, authmw.Opts{}))
		r.Get("/me", meHandler)
	})

	r.Get("/debug/auth-calls", authCallsHandler)

	return r
}

type meResponse struct {
	AccountID int64    `json:"aid"`
	Nick      string   `json:"nick"`
	Roles     []string `json:"roles"`
}

func meHandler(w http.ResponseWriter, r *http.Request) {
	claims, ok := authmw.GetClaims(r.Context())
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(meResponse{
		AccountID: claims.AccountID,
		Nick:      claims.Nick,
		Roles:     claims.Roles,
	})
}

func authCallsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"auth_calls": atomic.LoadInt64(&authCalls)})
}

func main() {
	secret := []byte(os.Getenv("JWT_SECRET"))
	if len(secret) == 0 {
		slog.Error("JWT_SECRET is required")
		os.Exit(1)
	}

	handler := newRouter(secret)

	srv := &http.Server{
		Addr:    ":8083",
		Handler: handler,
	}

	go func() {
		slog.Info("consumer listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	slog.Info("shutting down")
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	}
}
