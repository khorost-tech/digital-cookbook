package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

const sessionCookieName = "session_id"

// newRouter собирает chi-роутер demo-сервиса TOTP: enrollment/verify/recovery
// и step-up-защищённый /sensitive.
func newRouter(rdb *redis.Client) http.Handler {
	h := &totpHandler{rdb: rdb}

	r := chi.NewRouter()
	r.Get("/health", h.health)
	r.Post("/totp/enroll", h.enroll)
	r.Post("/totp/verify", h.verify)
	r.Post("/totp/recovery", h.recovery)
	r.Post("/sensitive", h.sensitive)

	return r
}

type totpHandler struct {
	rdb *redis.Client
}

// health — GET /health, используется docker-compose healthcheck: не
// зависит от Redis, только подтверждает, что процесс поднялся и слушает.
func (h *totpHandler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type enrollRequest struct {
	UserID string `json:"user_id"`
}

type enrollResponse struct {
	Secret     string   `json:"secret"`
	OtpauthURI string   `json:"otpauth_uri"`
	Recovery   []string `json:"recovery_codes"`
}

type codeRequest struct {
	UserID string `json:"user_id"`
	Code   string `json:"code"`
}

func (h *totpHandler) enroll(w http.ResponseWriter, r *http.Request) {
	var req enrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	secret, uri, recovery, err := Enroll(r.Context(), h.rdb, req.UserID)
	if err != nil {
		slog.Error("enroll failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, enrollResponse{Secret: secret, OtpauthURI: uri, Recovery: recovery})
}

func (h *totpHandler) verify(w http.ResponseWriter, r *http.Request) {
	var req codeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	ok, err := Verify(r.Context(), h.rdb, req.UserID, req.Code)
	if err != nil {
		if errors.Is(err, ErrTOTPRateLimited) {
			writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
			return
		}
		slog.Error("verify failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid code")
		return
	}

	sid := sessionID(r)
	if sid != "" {
		if err := MarkStepUp(r.Context(), h.rdb, sid); err != nil {
			slog.Error("mark step-up failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *totpHandler) recovery(w http.ResponseWriter, r *http.Request) {
	var req codeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	ok, err := UseRecovery(r.Context(), h.rdb, req.UserID, req.Code)
	if err != nil {
		slog.Error("recovery failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid or used recovery code")
		return
	}

	sid := sessionID(r)
	if sid != "" {
		if err := MarkStepUp(r.Context(), h.rdb, sid); err != nil {
			slog.Error("mark step-up failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// sensitive — пример защищённого действия: требует свежего MFA-подтверждения
// (RequireStepUp) для сессии из cookie/заголовка session_id. Демонстрирует
// step-up authentication: обычной (первичной) сессии недостаточно.
func (h *totpHandler) sensitive(w http.ResponseWriter, r *http.Request) {
	sid := sessionID(r)
	if sid == "" {
		writeError(w, http.StatusUnauthorized, "no session")
		return
	}

	ok, err := RequireStepUp(r.Context(), h.rdb, sid)
	if err != nil {
		slog.Error("require step-up failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		writeError(w, http.StatusForbidden, "step_up_required")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// sessionID берёт идентификатор сессии для demo из cookie session_id,
// а если её нет — из заголовка X-Session-Id (упрощение для curl/demo).
func sessionID(r *http.Request) string {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	return r.Header.Get("X-Session-Id")
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
