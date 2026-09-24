package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/redis/go-redis/v9"
)

// sessionTTL — время жизни challenge между /begin и /finish. Пять минут с
// запасом покрывает время, которое пользователь тратит на подтверждение
// у аутентификатора (Touch ID, ключ безопасности и т.п.).
const sessionTTL = 5 * time.Minute

// webauthnUser реализует webauthn.User поверх записи, хранимой в Redis под
// ключом webauthn:user:{username}. WebAuthnID — случайные 32 байта (user
// handle), не привязанные к username: так требует спецификация (§5.4.3).
type webauthnUser struct {
	ID          []byte                `json:"id"`
	Username    string                `json:"username"`
	Credentials []webauthn.Credential `json:"credentials"`
}

func (u *webauthnUser) WebAuthnID() []byte                         { return u.ID }
func (u *webauthnUser) WebAuthnName() string                       { return u.Username }
func (u *webauthnUser) WebAuthnDisplayName() string                { return u.Username }
func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

func newUser(username string) (*webauthnUser, error) {
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("generate user handle: %w", err)
	}
	return &webauthnUser{ID: id, Username: username}, nil
}

// server держит зависимости HTTP-хендлеров: подключение к Redis (хранилище
// пользователей/credential'ов и session data между begin/finish) и
// сконфигурированный по RPID/RPOrigins экземпляр библиотеки go-webauthn.
type server struct {
	rdb *redis.Client
	wa  *webauthn.WebAuthn
}

// newRouter собирает chi-роутер demo-сервиса WebAuthn: registration
// (attestation) и login (assertion) ceremony, каждая в два шага begin/finish,
// плюс (если staticDir непустой) раздачу статичного frontend'а (Task 6:
// web/index.html+app.js) с того же origin, с которого идут fetch()-запросы к
// ceremony endpoint'ам — это убирает необходимость в CORS.
func newRouter(rdb *redis.Client, wa *webauthn.WebAuthn, staticDir string) http.Handler {
	s := &server{rdb: rdb, wa: wa}

	r := chi.NewRouter()
	r.Post("/register/begin", s.registerBegin)
	r.Post("/register/finish", s.registerFinish)
	r.Post("/login/begin", s.loginBegin)
	r.Post("/login/finish", s.loginFinish)

	if staticDir != "" {
		r.Handle("/*", http.FileServer(http.Dir(staticDir)))
	}

	return r
}

func userKey(username string) string { return "webauthn:user:" + username }

func credKey(credID []byte) string {
	return "webauthn:cred:" + base64.RawURLEncoding.EncodeToString(credID)
}

func sessionKey(kind, username string) string { return "webauthn:session:" + kind + ":" + username }

func (s *server) loadUser(ctx context.Context, username string) (*webauthnUser, error) {
	raw, err := s.rdb.Get(ctx, userKey(username)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var u webauthnUser
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// saveUser сохраняет полную запись пользователя (с credential'ами) под
// webauthn:user:{username} и, для каждого credential, обратный указатель
// webauthn:cred:{credID} → username — пригодится для сценариев passkey-логина
// по discoverable credential, где известен только credential id.
func (s *server) saveUser(ctx context.Context, u *webauthnUser) error {
	raw, err := json.Marshal(u)
	if err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, userKey(u.Username), raw, 0).Err(); err != nil {
		return err
	}
	for _, c := range u.Credentials {
		if err := s.rdb.Set(ctx, credKey(c.ID), u.Username, 0).Err(); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) saveSession(ctx context.Context, kind, username string, sess *webauthn.SessionData) error {
	raw, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, sessionKey(kind, username), raw, sessionTTL).Err()
}

func (s *server) loadAndDeleteSession(ctx context.Context, kind, username string) (*webauthn.SessionData, error) {
	key := sessionKey(kind, username)

	raw, err := s.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("no active %s session for %q (begin the ceremony first, or it expired)", kind, username)
	}
	if err != nil {
		return nil, err
	}

	// Challenge одноразовый — удаляем сразу после чтения независимо от
	// исхода верификации, чтобы assertion/attestation нельзя было повторно
	// предъявить против того же challenge.
	_ = s.rdb.Del(ctx, key).Err()

	var sess webauthn.SessionData
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

type usernameRequest struct {
	Username string `json:"username"`
}

func (s *server) registerBegin(w http.ResponseWriter, r *http.Request) {
	var req usernameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	ctx := r.Context()

	user, err := s.loadUser(ctx, req.Username)
	if err != nil {
		slog.Error("load user failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		user, err = newUser(req.Username)
		if err != nil {
			slog.Error("create user failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if err := s.saveUser(ctx, user); err != nil {
			slog.Error("save user failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	creation, session, err := s.wa.BeginRegistration(user)
	if err != nil {
		slog.Error("begin registration failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := s.saveSession(ctx, "register", req.Username, session); err != nil {
		slog.Error("save registration session failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, creation)
}

func (s *server) registerFinish(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("username")
	if username == "" {
		writeError(w, http.StatusBadRequest, "username required")
		return
	}

	ctx := r.Context()

	user, err := s.loadUser(ctx, username)
	if err != nil {
		slog.Error("load user failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		writeError(w, http.StatusBadRequest, "unknown user: call /register/begin first")
		return
	}

	session, err := s.loadAndDeleteSession(ctx, "register", username)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	credential, err := s.wa.FinishRegistration(user, *session, r)
	if err != nil {
		slog.Warn("registration verification rejected", "user", username, "err", err)
		writeError(w, http.StatusBadRequest, "registration verification failed")
		return
	}

	user.Credentials = append(user.Credentials, *credential)
	if err := s.saveUser(ctx, user); err != nil {
		slog.Error("save user failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) loginBegin(w http.ResponseWriter, r *http.Request) {
	var req usernameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	ctx := r.Context()

	user, err := s.loadUser(ctx, req.Username)
	if err != nil {
		slog.Error("load user failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil || len(user.Credentials) == 0 {
		writeError(w, http.StatusBadRequest, "no credentials registered for this user")
		return
	}

	assertion, session, err := s.wa.BeginLogin(user)
	if err != nil {
		slog.Error("begin login failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := s.saveSession(ctx, "login", req.Username, session); err != nil {
		slog.Error("save login session failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, assertion)
}

func (s *server) loginFinish(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("username")
	if username == "" {
		writeError(w, http.StatusBadRequest, "username required")
		return
	}

	ctx := r.Context()

	user, err := s.loadUser(ctx, username)
	if err != nil {
		slog.Error("load user failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		writeError(w, http.StatusBadRequest, "unknown user")
		return
	}

	session, err := s.loadAndDeleteSession(ctx, "login", username)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// FinishLogin проверяет origin/rpId (сверка CollectedClientData) и подпись
	// assertion (ECDSA по сохранённому публичному ключу credential'а) —
	// это делает сама библиотека go-webauthn, здесь только диспетчеризация.
	credential, err := s.wa.FinishLogin(user, *session, r)
	if err != nil {
		slog.Warn("assertion verification rejected", "user", username, "err", err)
		writeError(w, http.StatusUnauthorized, "assertion verification failed")
		return
	}

	// go-webauthn НЕ отклоняет ceremony сама при откате signature counter —
	// она только выставляет Authenticator.CloneWarning на возвращённом
	// credential (см. Authenticator.UpdateCounter). Откат counter — признак
	// клонированного аутентификатора, поэтому здесь ceremony отклоняется
	// явно, а сохранённый (более новый) counter не перезаписывается более
	// старым/равным значением из этой попытки.
	if credential.Authenticator.CloneWarning {
		slog.Warn("clone warning: signature counter did not increase", "user", username)
		writeError(w, http.StatusUnauthorized, "possible credential cloning detected (signature counter did not increase)")
		return
	}

	for i := range user.Credentials {
		if bytes.Equal(user.Credentials[i].ID, credential.ID) {
			user.Credentials[i] = *credential
			break
		}
	}
	if err := s.saveUser(ctx, user); err != nil {
		slog.Error("save user failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
