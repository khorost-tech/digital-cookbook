package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"khorost.tech/cookbook/auth/internal/session"
)

// accessCookieName — единственная cookie, которую ставит login-methods.
// Refresh-cookie здесь намеренно НЕ ставится (FIX M-4): в этом сервисе нет
// эндпоинта /auth/refresh (это отдельный demo — см. multi-service/authservice
// и client-refresh), поэтому app_rt была бы мёртвым весом, отправляемым на
// каждый запрос без единого потребителя. CreateTokenPair всё ещё выпускает
// refresh-токен (session.CreateTokenPair всегда возвращает пару) — здесь он
// просто не публикуется наружу и живёт только как запись rs:{refresh} в Redis
// (самоистечёт по RefreshTTL).
const accessCookieName = "app_at"

// newRouter собирает chi-роутер login-methods: OTP (/login/otp/*), OAuth
// поверх mock-OIDC (/oauth/{provider}/*), Telegram — canonical widget-HMAC
// (/login/telegram/widget) и контрастный OAuth-вариант (/login/telegram/oauth).
func newRouter(rdb *redis.Client, secret []byte, botToken string, oauth *oauthHandler, secureCookies bool) http.Handler {
	h := &loginHandler{rdb: rdb, secret: secret, botToken: botToken, secureCookies: secureCookies}

	r := chi.NewRouter()

	r.Post("/login/otp/request", h.otpRequest)
	r.Post("/login/otp/verify", h.otpVerify)

	r.Get("/oauth/{provider}/start", oauth.startOAuth)
	r.Get("/oauth/{provider}/callback", oauth.callbackOAuth)

	r.Post("/login/telegram/widget", h.telegramWidget)
	r.Post("/login/telegram/oauth", h.telegramOAuth)

	return r
}

type loginHandler struct {
	rdb           *redis.Client
	secret        []byte
	botToken      string
	secureCookies bool
}

type otpRequestBody struct {
	Email string `json:"email"`
}

// otpRequest — POST /login/otp/request {"email"}. Генерирует и "отправляет"
// (в демо — только сохраняет в Redis) одноразовый код.
func (h *loginHandler) otpRequest(w http.ResponseWriter, r *http.Request) {
	var req otpRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	if err := RequestOTP(r.Context(), h.rdb, req.Email); err != nil {
		if errors.Is(err, ErrOTPRateLimited) {
			writeError(w, http.StatusTooManyRequests, "rate_limited")
			return
		}
		slog.Error("otp request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type otpVerifyBody struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

type loginResponse struct {
	AccessToken string `json:"access_token"`
}

// otpVerify — POST /login/otp/verify {"email","code"}. При успехе выпускает
// пару access/refresh (loginMethod="otp") и ставит cookie app_at (refresh —
// см. disclaimer у accessCookieName).
func (h *loginHandler) otpVerify(w http.ResponseWriter, r *http.Request) {
	var req otpVerifyBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	aid, err := VerifyOTP(r.Context(), h.rdb, req.Email, req.Code)
	if err != nil {
		switch {
		case errors.Is(err, ErrTooManyAttempts):
			writeError(w, http.StatusTooManyRequests, "too_many_attempts")
		case errors.Is(err, ErrInvalidCode):
			writeError(w, http.StatusUnauthorized, "invalid_code")
		default:
			slog.Error("otp verify failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal_error")
		}
		return
	}

	acc := session.Account{ID: aid, Nick: req.Email, Roles: []string{"user"}}
	access, _, err := session.CreateTokenPair(r.Context(), h.rdb, h.secret, acc, "otp")
	if err != nil {
		slog.Error("otp verify: create token pair failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	setAccessCookie(w, r, access, h.secureCookies)
	writeJSON(w, http.StatusOK, loginResponse{AccessToken: access})
}

// telegramWidget — POST /login/telegram/widget, тело — JSON-объект с полями
// виджета Telegram Login (id, first_name, ..., auth_date, hash). Проверяет
// canonical widget-HMAC (VerifyTelegramWidget); при успехе выпускает пару
// access/refresh (loginMethod="telegram").
func (h *loginHandler) telegramWidget(w http.ResponseWriter, r *http.Request) {
	var data map[string]string
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil || len(data) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	ok, err := VerifyTelegramWidget(h.botToken, data, DefaultTelegramAuthMaxAge)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_signature")
		return
	}

	h.issueForTelegram(w, r, data["id"], data["username"], data["first_name"])
}

type telegramOAuthBody struct {
	IDToken string `json:"id_token"`
}

// telegramOAuth — POST /login/telegram/oauth {"id_token"} — КОНТРАСТНЫЙ
// вариант: доверяет содержимому id_token без проверки подписи
// (parseTelegramOAuthProfile). Существует в этом демо-стенде только для
// сравнения с canonical widget-HMAC — см. disclaimer в telegram.go.
func (h *loginHandler) telegramOAuth(w http.ResponseWriter, r *http.Request) {
	var req telegramOAuthBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IDToken == "" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	profile, err := parseTelegramOAuthProfile(req.IDToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_id_token")
		return
	}

	h.issueForTelegram(w, r, profile.Sub, profile.Username, profile.Name)
}

func (h *loginHandler) issueForTelegram(w http.ResponseWriter, r *http.Request, tgID, username, firstName string) {
	nick := username
	if nick == "" {
		nick = firstName
	}
	if nick == "" {
		nick = "tg:" + tgID
	}

	acc := session.Account{ID: accountIDFromTelegramID(tgID), Nick: nick, Roles: []string{"user"}}
	access, _, err := session.CreateTokenPair(r.Context(), h.rdb, h.secret, acc, "telegram")
	if err != nil {
		slog.Error("telegram login: create token pair failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	setAccessCookie(w, r, access, h.secureCookies)
	writeJSON(w, http.StatusOK, loginResponse{AccessToken: access})
}

func setAccessCookie(w http.ResponseWriter, r *http.Request, access string, secureCookies bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     accessCookieName,
		Value:    access,
		Path:     "/",
		HttpOnly: true,
		Secure:   secureCookies || r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(session.AccessTTL.Seconds()),
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
