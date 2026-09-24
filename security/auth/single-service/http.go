package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

const sessionCookieName = "session_id"

type ctxKey int

const userIDCtxKey ctxKey = iota

// newRouter собирает chi-роутер: публичные /auth/send-code и /auth/verify-code,
// защищённые (за RequireAuth) — /auth/sessions (GET/DELETE) и /auth/logout.
func newRouter(rdb *redis.Client, secureCookies bool) http.Handler {
	h := &authHandler{rdb: rdb, secureCookies: secureCookies}

	r := chi.NewRouter()
	r.Post("/auth/send-code", h.sendCode)
	r.Post("/auth/verify-code", h.verifyCode)

	r.Group(func(r chi.Router) {
		r.Use(h.RequireAuth)
		r.Get("/auth/sessions", h.listSessions)
		r.Post("/auth/logout", h.logout)
		r.Delete("/auth/sessions/{sid}", h.deleteSession)
	})

	return r
}

type authHandler struct {
	rdb           *redis.Client
	secureCookies bool
}

type sendCodeRequest struct {
	Email string `json:"email"`
}

type verifyCodeRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

func (h *authHandler) sendCode(w http.ResponseWriter, r *http.Request) {
	var req sendCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	if err := SendCode(r.Context(), h.rdb, req.Email); err != nil {
		if errors.Is(err, ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		slog.Error("send-code failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *authHandler) verifyCode(w http.ResponseWriter, r *http.Request) {
	var req verifyCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	uid, err := VerifyCode(r.Context(), h.rdb, req.Email, req.Code)
	if err != nil {
		switch {
		case errors.Is(err, ErrTooManyAttempts):
			writeError(w, http.StatusTooManyRequests, "too many attempts")
		case errors.Is(err, ErrInvalidCode):
			writeError(w, http.StatusUnauthorized, "invalid code")
		default:
			slog.Error("verify-code failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	sid, err := CreateSession(r.Context(), h.rdb, uid)
	if err != nil {
		slog.Error("create session failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.setSessionCookie(w, r, sid)
	w.WriteHeader(http.StatusNoContent)
}

func (h *authHandler) listSessions(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(userIDCtxKey).(string)

	views, err := ListSessions(r.Context(), h.rdb, uid)
	if err != nil {
		slog.Error("list sessions failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, views)
}

func (h *authHandler) logout(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(userIDCtxKey).(string)

	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		if err := DeleteSession(r.Context(), h.rdb, cookie.Value, uid); err != nil {
			slog.Error("logout: delete session failed", "err", err)
		}
	}

	h.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (h *authHandler) deleteSession(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(userIDCtxKey).(string)
	sid := chi.URLParam(r, "sid")

	if err := DeleteSession(r.Context(), h.rdb, sid, uid); err != nil {
		slog.Error("delete session failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RequireAuth — middleware: читает cookie session_id, валидирует через ValidateAndTouch,
// кладёт userID в контекст запроса. Отвечает 401, если сессии нет.
func (h *authHandler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "no session")
			return
		}

		uid, err := ValidateAndTouch(r.Context(), h.rdb, cookie.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid session")
			return
		}

		ctx := context.WithValue(r.Context(), userIDCtxKey, uid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *authHandler) setSessionCookie(w http.ResponseWriter, r *http.Request, sid string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookies || r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (h *authHandler) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookies || r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
