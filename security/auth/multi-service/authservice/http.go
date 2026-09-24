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

	"khorost.tech/cookbook/auth/internal/jwtclaims"
	"khorost.tech/cookbook/auth/internal/session"
	"khorost.tech/cookbook/auth/multi-service/authmw"
)

const (
	accessCookieName  = "app_at"
	refreshCookieName = "app_rt"
)

// newRouter собирает chi-роутер auth-сервиса: публичные /auth/login и
// /auth/refresh, защищённый (за authmw) /auth/sessions.
func newRouter(rdb *redis.Client, secret []byte, secureCookies bool) http.Handler {
	h := &authHandler{rdb: rdb, secret: secret, secureCookies: secureCookies}

	r := chi.NewRouter()
	r.Post("/auth/login", h.login)
	r.Post("/auth/refresh", h.refresh)

	r.Group(func(r chi.Router) {
		r.Use(authmw.Middleware(secret, authmw.Opts{}))
		r.Get("/auth/sessions", h.sessions)
	})

	return r
}

type authHandler struct {
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
// реальной системе тут был бы lookup/insert в таблице пользователей), выпускает
// пару access/refresh и ставит cookie app_at/app_rt. Тело ответа — access-токен
// (для клиентов, которые сами кладут его в Authorization: Bearer, а не полагаются
// на cookie).
func (h *authHandler) login(w http.ResponseWriter, r *http.Request) {
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

// refresh — базовый обмен refresh→access: cookie app_rt проверяется через
// session.ValidateAndTouch, при успехе выпускается свежий access-токен.
// Полная ротация refresh-токена с grace-периодом и блокировкой повторного
// использования — отдельный сервис (см. Task 5), здесь намеренно не
// реализована.
func (h *authHandler) refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(refreshCookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "missing_refresh")
		return
	}

	if _, err := session.ValidateAndTouch(r.Context(), h.rdb, cookie.Value); err != nil {
		if errors.Is(err, session.ErrNoSession) {
			writeError(w, http.StatusUnauthorized, "invalid_refresh")
			return
		}
		slog.Error("refresh: validate failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	// Полный Account (nick/roles), а не только aid, — иначе новый access
	// после refresh терял бы nick/roles и RequireRole молча отклонял бы
	// запросы, подписанные этим токеном (см. session.AccountFromSession).
	acc, err := session.AccountFromSession(r.Context(), h.rdb, cookie.Value)
	if err != nil {
		if errors.Is(err, session.ErrNoSession) {
			writeError(w, http.StatusUnauthorized, "invalid_refresh")
			return
		}
		slog.Error("refresh: load account failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	access, err := jwtclaims.Issue(h.secret, jwtclaims.Claims{
		AccountID: acc.ID,
		Nick:      acc.Nick,
		Roles:     acc.Roles,
	}, session.AccessTTL)
	if err != nil {
		slog.Error("refresh: issue access failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	h.setAccessCookie(w, r, access)
	writeJSON(w, http.StatusOK, refreshResponse{AccessToken: access})
}

// sessions — список живых refresh-сессий текущего аккаунта (из rsu:{aid}).
func (h *authHandler) sessions(w http.ResponseWriter, r *http.Request) {
	claims, ok := authmw.GetClaims(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing_token")
		return
	}

	views, err := session.ListSessions(r.Context(), h.rdb, claims.AccountID)
	if err != nil {
		slog.Error("sessions: list failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	writeJSON(w, http.StatusOK, views)
}

func (h *authHandler) setAccessCookie(w http.ResponseWriter, r *http.Request, access string) {
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

func (h *authHandler) setRefreshCookie(w http.ResponseWriter, r *http.Request, refresh string) {
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
// local-part адреса (до @). В реальной системе это был бы lookup/insert в
// таблице пользователей; здесь — чтобы не тащить БД в demo-стенд.
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
