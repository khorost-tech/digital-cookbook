// Command authservice — выделенный auth-сервис: выпускает JWT access
// (HS256, короткоживущий) и opaque refresh (в Redis), обслуживает базовый
// обмен refresh→access. Сервисы-потребители (multi-service/consumer) в него
// НЕ ходят на каждый запрос — они валидируют access локально общим
// middleware (multi-service/authmw) по общему JWT_SECRET.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"khorost.tech/cookbook/auth/internal/rstore"
)

func main() {
	secret := []byte(os.Getenv("JWT_SECRET"))
	if len(secret) == 0 {
		slog.Error("JWT_SECRET is required")
		os.Exit(1)
	}

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	rdb := rstore.New(redisAddr)
	defer func() { _ = rdb.Close() }()

	handler := newRouter(rdb, secret, false)

	srv := &http.Server{
		Addr:    ":8082",
		Handler: handler,
	}

	go func() {
		slog.Info("authservice listening", "addr", srv.Addr, "redis", redisAddr)
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
