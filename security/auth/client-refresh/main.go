// Command client-refresh — demo-сервис флагманской темы (ст. 5): ротация
// refresh-токена с grace-period, распределённым refresh-lock (single-flight
// на аккаунт) и replay-successor, чтобы гонка нескольких вкладок/устройств
// не приводила к ложному разлогину. Раздаёт статику web/ (naive/coordinated
// frontend-клиенты, Task 6) из каталога STATIC_DIR (по умолчанию "./web").
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

	staticDir := os.Getenv("STATIC_DIR")
	if staticDir == "" {
		staticDir = "./web"
	}

	handler := newRouter(rdb, secret, false, staticDir)

	srv := &http.Server{
		Addr:    ":8085",
		Handler: handler,
	}

	go func() {
		slog.Info("client-refresh listening", "addr", srv.Addr, "redis", redisAddr)
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
