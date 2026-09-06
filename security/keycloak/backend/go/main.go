// Command backend — демонстрационный OIDC resource server для стенда Keycloak.
// Валидирует access-токены Keycloak (JWKS локально или через introspection) и
// разграничивает доступ по realm-ролям. Конфигурируется только через env-vars.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"khorost.tech/keycloak-demo/backend/internal/auth"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	issuer := getenv("KC_ISSUER", "http://keycloak:8080/realms/demo")
	audience := getenv("KC_AUDIENCE", "backend")
	mode := auth.Mode(getenv("AUTH_MODE", string(auth.ModeJWKS)))
	addr := getenv("LISTEN_ADDR", ":8081")

	// discovery (JWKS-режим) требует готового Keycloak: даём ему время на старт.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	verifier, err := newVerifierWithRetry(ctx, logger, auth.Config{
		Mode:         mode,
		Issuer:       issuer,
		Audience:     audience,
		ClientID:     getenv("KC_BACKEND_CLIENT_ID", "backend"),
		ClientSecret: os.Getenv("KC_BACKEND_SECRET"),
		Logger:       logger,
	})
	if err != nil {
		return err
	}
	defer verifier.Close() // останавливает фоновое обновление JWKS при завершении

	mux := http.NewServeMux()
	mux.HandleFunc("/public", handlePublic)
	mux.Handle("/me", verifier.Middleware(http.HandlerFunc(handleMe), ""))
	mux.Handle("/admin", verifier.Middleware(http.HandlerFunc(handleAdmin), "admin"))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr, "mode", mode, "issuer", issuer, "audience", audience)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case <-stop:
		logger.Info("shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// newVerifierWithRetry повторяет создание Verifier, пока Keycloak поднимается
// (discovery-эндпоинт может быть недоступен первые секунды после старта стека).
func newVerifierWithRetry(ctx context.Context, logger *slog.Logger, cfg auth.Config) (*auth.Verifier, error) {
	var lastErr error
	for attempt := 1; ; attempt++ {
		v, err := auth.New(ctx, cfg)
		if err == nil {
			return v, nil
		}
		lastErr = err
		logger.Warn("verifier init failed, retrying", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return nil, errors.Join(lastErr, ctx.Err())
		case <-time.After(3 * time.Second):
		}
	}
}

func handlePublic(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"message": "public endpoint, no token required"})
}

func handleMe(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no principal"})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func handleAdmin(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "admin endpoint",
		"sub":     p.Subject,
		"roles":   p.Roles,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "err", err)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
