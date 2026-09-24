package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"khorost.tech/cookbook/auth/internal/rstore"
)

func main() {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	rpID := os.Getenv("WEBAUTHN_RP_ID")
	if rpID == "" {
		rpID = "localhost"
	}

	rpName := os.Getenv("WEBAUTHN_RP_NAME")
	if rpName == "" {
		rpName = "Khorost WebAuthn Demo"
	}

	rpOrigins := splitOrigins(os.Getenv("WEBAUTHN_RP_ORIGINS"))
	if len(rpOrigins) == 0 {
		rpOrigins = []string{"http://localhost:8087"}
	}

	// STATIC_DIR — каталог с frontend'ом (Task 6: web/index.html+app.js).
	// В Docker-образе (см. Dockerfile) файлы лежат в /web; при пустом
	// значении статика не раздаётся (только JSON-API), как было до Task 6.
	staticDir := os.Getenv("STATIC_DIR")
	if staticDir == "" {
		staticDir = "/web"
	}

	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: rpName,
		RPOrigins:     rpOrigins,
	})
	if err != nil {
		slog.Error("webauthn config invalid", "err", err)
		os.Exit(1)
	}

	rdb := rstore.New(addr)
	defer func() { _ = rdb.Close() }()

	handler := newRouter(rdb, wa, staticDir)

	srv := &http.Server{
		Addr:              ":8087",
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("webauthn listening", "addr", srv.Addr, "redis", addr, "rp_id", rpID, "rp_origins", rpOrigins)
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

// splitOrigins разбирает WEBAUTHN_RP_ORIGINS (список через запятую) в срез
// разрешённых origin'ов для webauthn.Config.RPOrigins.
func splitOrigins(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
