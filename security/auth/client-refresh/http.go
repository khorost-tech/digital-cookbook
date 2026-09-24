package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"khorost.tech/cookbook/auth/internal/session"
	"khorost.tech/cookbook/auth/multi-service/authmw"
)

const (
	accessCookieName  = "app_at"
	refreshCookieName = "app_rt"
)

// newRouter собирает chi-роутер demo-сервиса client-refresh: POST /auth/login
// (выпуск начальной пары access/refresh), POST /auth/refresh (ротация через
// Rotate — grace + lock + replay), GET /api/ping (demo protected-эндпоинт под
// access-токеном) и раздачу статики web/ (frontend Task 6 — naive/coordinated
// клиенты) на "/". Маршруты /auth/* и /api/* регистрируются явно и имеют
// приоритет над catch-all "/*" со статикой.
func newRouter(rdb *redis.Client, secret []byte, secureCookies bool, staticDir string) http.Handler {
	h := &refreshHandler{rdb: rdb, secret: secret, secureCookies: secureCookies}

	r := chi.NewRouter()
	r.Post("/auth/login", h.login)
	r.Post("/auth/refresh", h.refresh)

	r.With(authmw.Middleware(secret, authmw.Opts{})).Get("/api/ping", pingHandler)

	if staticDir != "" {
		r.Handle("/*", http.FileServer(http.Dir(staticDir)))
	}

	return r
}

// pingHandler — demo protected-эндпоинт: доступен только с валидным
// access-токеном (Bearer или cookie app_at, см. authmw.Middleware). Не
// участвует в замере координации вкладок — вспомогательная демонстрация
// "защищённый ресурс отвечает, пока access жив".
func pingHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type refreshHandler struct {
	rdb           *redis.Client
	secret        []byte
	secureCookies bool
}

type loginRequest struct {
	Email string `json:"email"`
}

type loginResponse struct {
	AccessToken string `json:"access_token"`
}

// login — demo-эндпоинт: детерминированно отображает email в Account (в
// реальной системе тут был бы lookup/insert в таблице пользователей),
// выпускает начальную пару access/refresh и ставит cookie app_at/app_rt.
func (h *refreshHandler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	acc := accountFromEmail(req.Email)

	access, refresh, err := session.CreateTokenPair(r.Context(), h.rdb, h.secret, acc, "password")
	if err != nil {
		slog.Error("login: create token pair failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	h.setAccessCookie(w, r, access)
	h.setRefreshCookie(w, r, refresh)

	writeJSON(w, http.StatusOK, loginResponse{AccessToken: access})
}

type refreshResponse struct {
	AccessToken string `json:"access_token"`
}

// refresh — ротация refresh-токена: cookie app_rt проверяется и ротируется
// через Rotate (grace-period + распределённый lock + replay-successor), так
// что гонка нескольких вкладок/устройств не приводит к ложному разлогину.
//
//   - ErrRefreshInProgress (проигранный single-flight lock) → 429 +
//     Retry-After: 1 — клиент должен повторить запрос чуть позже.
//   - ErrTokenInvalid (сессии нет и grace-мост истёк/отсутствует) → 401 —
//     настоящий разлогин.
func (h *refreshHandler) refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(refreshCookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "missing_refresh")
		return
	}

	access, newRefresh, err := Rotate(r.Context(), h.rdb, h.secret, cookie.Value)
	if err != nil {
		switch {
		case errors.Is(err, ErrRefreshInProgress):
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "refresh_in_progress")
		case errors.Is(err, ErrTokenInvalid):
			writeError(w, http.StatusUnauthorized, "invalid_refresh")
		default:
			slog.Error("refresh: rotate failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal_error")
		}
		return
	}

	h.setAccessCookie(w, r, access)
	h.setRefreshCookie(w, r, newRefresh)

	writeJSON(w, http.StatusOK, refreshResponse{AccessToken: access})
}

func (h *refreshHandler) setAccessCookie(w http.ResponseWriter, r *http.Request, access string) {
	http.SetCookie(w, &http.Cookie{
		Name:     accessCookieName,
		Value:    access,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookies || r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(session.AccessTTL.Seconds()),
	})
}

func (h *refreshHandler) setRefreshCookie(w http.ResponseWriter, r *http.Request, refresh string) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    refresh,
		Path:     "/auth",
		HttpOnly: true,
		Secure:   h.secureCookies || r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(session.RefreshTTL.Seconds()),
	})
}

// accountFromEmail детерминированно производит Account из email для demo-целей:
// AccountID = первые 8 байт sha256(email) как положительное int64, Nick —
// local-part адреса (до @).
func accountFromEmail(email string) session.Account {
	sum := sha256.Sum256([]byte(email))
	id := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)

	nick := email
	if i := strings.IndexByte(email, '@'); i >= 0 {
		nick = email[:i]
	}

	return session.Account{ID: id, Nick: nick, Roles: []string{"user"}}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
