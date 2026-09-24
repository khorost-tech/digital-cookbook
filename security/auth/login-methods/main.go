// Command login-methods — демо-стенд методов входа поверх auth-сервиса
// (internal/session): беспарольный email-код (OTP), OAuth через встроенный
// mock-OIDC-провайдер (internal/login-methods/mockidp, PKCE S256) и Telegram
// (canonical Login Widget с HMAC-проверкой + контрастный небезопасный
// OAuth-вариант). mockidp встроен в этот же процесс на под-пути /mockidp —
// отдельный сетевой сервис для demo-масштаба избыточен, а встраивание
// позволяет OAuth start/callback ходить на mockidp через тот же localhost
// без лишнего docker-compose сервиса. См. также docker-compose.yml.
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

	"github.com/go-chi/chi/v5"

	"khorost.tech/cookbook/auth/internal/rstore"
	"khorost.tech/cookbook/auth/login-methods/mockidp"
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

	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		slog.Warn("TELEGRAM_BOT_TOKEN is not set — /login/telegram/widget will reject all requests")
	}

	publicURL := os.Getenv("PUBLIC_URL")
	if publicURL == "" {
		publicURL = "http://localhost:8084"
	}

	clientID := os.Getenv("OIDC_CLIENT_ID")
	if clientID == "" {
		clientID = "login-methods-demo"
	}

	// OIDC_ISSUER позволяет вынести mockidp во внешний сервис (см. docstring
	// пакета выше); по умолчанию используется встроенный /mockidp этого же
	// процесса.
	issuer := os.Getenv("OIDC_ISSUER")
	if issuer == "" {
		issuer = publicURL + "/mockidp"
	}

	rdb := rstore.New(redisAddr)
	defer func() { _ = rdb.Close() }()

	oauth := newOAuthHandler(rdb, secret, issuer, clientID, publicURL, false)

	router := chi.NewRouter()
	router.Mount("/mockidp", http.StripPrefix("/mockidp", mockidp.New()))
	router.Mount("/", newRouter(rdb, secret, botToken, oauth, false))

	srv := &http.Server{
		Addr:    ":8084",
		Handler: router,
	}

	go func() {
		slog.Info("login-methods listening", "addr", srv.Addr, "redis", redisAddr, "issuer", issuer)
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
