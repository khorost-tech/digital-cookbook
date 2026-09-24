// Package mockidp — встроенный минимальный mock-OIDC-провайдер для демо
// OAuth/OIDC-потоков (login-methods). Реализует:
//   - authorization_code + PKCE (S256): GET /authorize, POST /token, GET /userinfo;
//   - client_credentials: POST /token;
//   - discovery: GET /.well-known/openid-configuration, GET /.well-known/jwks.json;
//   - интроспекцию токенов (RFC 7662): POST /introspect.
//
// НЕ предназначен для боевого использования — состояние (коды авторизации,
// access-токены) хранится в памяти процесса. id_token подписывается RS256
// ключом, сгенерированным заново при каждом New() (эфемерный ключ демо-стенда);
// публичная часть публикуется через JWKS — клиент проверяет подпись локально
// по ключу из JWKS (см. пакет oidc), без похода к mockidp за каждым токеном.
package mockidp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"khorost.tech/cookbook/auth/internal/token"
)

// codeTTL — время жизни выданного authorization code до обмена на токен.
const codeTTL = 2 * time.Minute

// deviceCodeTTL — время жизни device_code/user_code (RFC 8628) до истечения
// ("expired_token"), если пользователь так и не подтвердил вход.
const deviceCodeTTL = 10 * time.Minute

// deviceInterval — минимальный интервал поллинга /token в секундах,
// возвращаемый в device_authorization-ответе (RFC 8628 §3.2). Для demo
// фиксирован в 1с — достаточно быстрый, но не нулевой polling.
const deviceInterval = 1

// idTokenTTL — время жизни id_token, выпущенного mock-провайдером.
const idTokenTTL = 10 * time.Minute

// accessTokenTTL — время жизни access-токена (code-flow и client_credentials),
// после которого /introspect обязан вернуть active:false.
const accessTokenTTL = 10 * time.Minute

// defaultClientID — client_id, используемый, если /authorize вызван без него
// (демо допускает такой вызов ради простоты существующих login-methods-тестов).
const defaultClientID = "mockidp-demo-client"

// demoClientCredentialsID/Secret — фиксированные тестовые client credentials
// для grant_type=client_credentials. Явно демонстрационные, НЕ секрет в
// каком-либо реальном смысле.
const (
	demoClientCredentialsID     = "demo-client"
	demoClientCredentialsSecret = "demo-secret"
)

// pendingAuth — состояние authorization code между /authorize и /token.
type pendingAuth struct {
	challenge   string
	redirectURI string
	email       string
	name        string
	clientID    string
	createdAt   time.Time
}

// issuedProfile — профиль/метаданные, привязанные к выданному access_token
// (используется /userinfo и /introspect).
type issuedProfile struct {
	Sub       string
	Email     string
	Name      string
	Scope     string
	ClientID  string
	ExpiresAt time.Time
}

// deviceStatus — состояние device_code между /device_authorization и
// успешным обменом на токен в /token.
type deviceStatus int

const (
	deviceStatusPending deviceStatus = iota
	deviceStatusApproved
)

// deviceAuth — состояние одного device authorization flow (RFC 8628),
// хранится в памяти процесса по ключу device_code. Состояние живёт в
// in-memory map под тем же h.mu, что и codes/tokens — для demo-стенда этого
// достаточно (никакой персистентности между рестартами не требуется, как и
// для остальных состояний mockidp); внешний internal/rstore (Redis) здесь
// намеренно не используется, чтобы не тащить сетевую зависимость в мок,
// который и так живёт только в памяти процесса теста/demo-сервера.
type deviceAuth struct {
	userCode  string
	status    deviceStatus
	email     string
	name      string
	clientID  string
	expiresAt time.Time
}

// idTokenClaims — claims id_token mock-провайдера.
type idTokenClaims struct {
	jwt.RegisteredClaims
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Handler — состояние mock-OIDC-провайдера.
type Handler struct {
	mu     sync.Mutex
	codes  map[string]pendingAuth
	tokens map[string]issuedProfile

	// devices — device_code -> состояние device-flow.
	// deviceUserCodes — нормализованный user_code -> device_code, для
	// быстрого поиска в /device/approve (пользователь вводит user_code, не
	// device_code — тот виден только клиентскому устройству).
	devices         map[string]deviceAuth
	deviceUserCodes map[string]string

	signingKey *rsa.PrivateKey
	kid        string

	mux *http.ServeMux
}

// New создаёт mock-OIDC-провайдер как http.Handler с маршрутами /authorize,
// /token, /userinfo, /introspect, /.well-known/openid-configuration,
// /.well-known/jwks.json. Генерирует свежий RSA-2048 ключ подписи id_token
// (эфемерный — живёт, пока жив процесс/тест).
func New() http.Handler {
	signingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("mockidp: failed to generate RSA signing key: " + err.Error())
	}

	h := &Handler{
		codes:           make(map[string]pendingAuth),
		tokens:          make(map[string]issuedProfile),
		devices:         make(map[string]deviceAuth),
		deviceUserCodes: make(map[string]string),
		signingKey:      signingKey,
		kid:             computeKid(&signingKey.PublicKey),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", h.authorize)
	mux.HandleFunc("/token", h.token)
	mux.HandleFunc("/userinfo", h.userinfo)
	mux.HandleFunc("/introspect", h.introspect)
	mux.HandleFunc("/device_authorization", h.deviceAuthorization)
	mux.HandleFunc("/device/approve", h.deviceApprove)
	mux.HandleFunc("/device", h.deviceVerificationPage)
	mux.HandleFunc("/.well-known/openid-configuration", h.discovery)
	mux.HandleFunc("/.well-known/jwks.json", h.jwks)
	h.mux = mux

	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// authorize — GET /authorize?client_id&redirect_uri&state&code_challenge&code_challenge_method=S256[&login_hint&name_hint].
// Требует присутствия code_challenge с method=S256 (без PKCE запрос отклоняется).
// Выпускает одноразовый authorization code, привязанный к challenge, и
// редиректит на redirect_uri с code+state.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirectURI := q.Get("redirect_uri")
	state := q.Get("state")
	challenge := q.Get("code_challenge")
	method := q.Get("code_challenge_method")

	if redirectURI == "" {
		http.Error(w, "invalid_request: redirect_uri required", http.StatusBadRequest)
		return
	}
	if challenge == "" || method != "S256" {
		http.Error(w, "invalid_request: code_challenge with method=S256 required", http.StatusBadRequest)
		return
	}

	clientID := q.Get("client_id")
	if clientID == "" {
		clientID = defaultClientID
	}

	email := q.Get("login_hint")
	if email == "" {
		email = "mockuser@example.com"
	}
	name := q.Get("name_hint")
	if name == "" {
		name = "Mock User"
	}

	code, err := token.OpaqueHex(16)
	if err != nil {
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}

	h.mu.Lock()
	h.codes[code] = pendingAuth{
		challenge:   challenge,
		redirectURI: redirectURI,
		email:       email,
		name:        name,
		clientID:    clientID,
		createdAt:   time.Now(),
	}
	h.mu.Unlock()

	dest, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid_request: malformed redirect_uri", http.StatusBadRequest)
		return
	}
	qs := dest.Query()
	qs.Set("code", code)
	if state != "" {
		qs.Set("state", state)
	}
	dest.RawQuery = qs.Encode()

	http.Redirect(w, r, dest.String(), http.StatusFound)
}

// deviceGrantType — значение grant_type для device authorization flow
// (RFC 8628 §3.4).
const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// token — POST /token (application/x-www-form-urlencoded). Диспетчеризует по
// grant_type: пустое значение или "authorization_code" — существующий
// code+PKCE обмен; "client_credentials" — выдача access-токена по
// client_id/client_secret (RFC 6749 §4.4); urn:ietf:params:oauth:grant-type:device_code —
// поллинг device authorization flow (RFC 8628 §3.4).
func (h *Handler) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", "")
		return
	}

	switch r.PostFormValue("grant_type") {
	case "", "authorization_code":
		h.tokenAuthorizationCode(w, r)
	case "client_credentials":
		h.tokenClientCredentials(w, r)
	case deviceGrantType:
		h.tokenDeviceCode(w, r)
	default:
		writeTokenError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

// writeTokenError формирует ответ ошибки /token в формате, единообразном для
// ВСЕХ grant-типов (code+PKCE, client_credentials, device_code): JSON
// {"error": "...", "error_description": "..."} (RFC 6749 §5.2 требует именно
// такой формат для /token — plain-text здесь недопустим, в отличие от
// человекочитаемых ошибок /authorize или demo-эндпоинтов device_authorization
// /device/approve, которые не являются /token и этим правилом не связаны).
// description опускается из ответа, если пуст.
func writeTokenError(w http.ResponseWriter, status int, code, description string) {
	resp := map[string]string{"error": code}
	if description != "" {
		resp["error_description"] = description
	}
	writeJSON(w, status, resp)
}

// tokenAuthorizationCode — обмен authorization code + PKCE verifier на
// id_token/access_token. Проверяет S256(code_verifier) == challenge,
// сохранённый на /authorize для этого code (ОБЯЗАТЕЛЬНАЯ проверка PKCE).
// Code одноразовый — удаляется из состояния сразу при обмене, независимо от
// исхода PKCE-проверки. Возвращает JSON {id_token, access_token, token_type}.
func (h *Handler) tokenAuthorizationCode(w http.ResponseWriter, r *http.Request) {
	code := r.PostFormValue("code")
	verifier := r.PostFormValue("code_verifier")
	if code == "" || verifier == "" {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", "code and code_verifier required")
		return
	}

	h.mu.Lock()
	pending, ok := h.codes[code]
	if ok {
		delete(h.codes, code)
	}
	h.mu.Unlock()

	if !ok {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant", "unknown or already-used code")
		return
	}
	if time.Since(pending.createdAt) > codeTTL {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant", "code expired")
		return
	}

	if !verifyPKCE(verifier, pending.challenge) {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}

	sub := subjectFromEmail(pending.email)

	idToken, err := h.issueIDToken(sub, pending.email, pending.name, baseURL(r), pending.clientID)
	if err != nil {
		writeTokenError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	accessToken, err := token.OpaqueHex(24)
	if err != nil {
		writeTokenError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	h.mu.Lock()
	h.tokens[accessToken] = issuedProfile{
		Sub:       sub,
		Email:     pending.email,
		Name:      pending.name,
		Scope:     "openid profile email",
		ClientID:  pending.clientID,
		ExpiresAt: time.Now().Add(accessTokenTTL),
	}
	h.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{
		"id_token":     idToken,
		"access_token": accessToken,
		"token_type":   "Bearer",
	})
}

// tokenClientCredentials — POST /token grant_type=client_credentials.
// client_id/client_secret принимаются либо через HTTP Basic Auth, либо через
// form-поля (оба варианта допустимы RFC 6749). Демо-креды фиксированные
// (demoClientCredentialsID/Secret) — явно тестовые, не боевые секреты.
// id_token НЕ выдаётся (client_credentials — чистый OAuth2-grant, без
// пользовательского контекста, вне OIDC id_token).
func (h *Handler) tokenClientCredentials(w http.ResponseWriter, r *http.Request) {
	clientID, clientSecret, ok := clientCredentialsFromRequest(r)
	if !ok || clientID != demoClientCredentialsID || clientSecret != demoClientCredentialsSecret {
		writeTokenError(w, http.StatusUnauthorized, "invalid_client", "")
		return
	}

	scope := r.PostFormValue("scope")
	if scope == "" {
		scope = "demo.read"
	}

	accessToken, err := token.OpaqueHex(24)
	if err != nil {
		writeTokenError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	h.mu.Lock()
	h.tokens[accessToken] = issuedProfile{
		Sub:       clientID,
		Scope:     scope,
		ClientID:  clientID,
		ExpiresAt: time.Now().Add(accessTokenTTL),
	}
	h.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   int(accessTokenTTL.Seconds()),
		"scope":        scope,
	})
}

// clientCredentialsFromRequest достаёт client_id/client_secret из HTTP Basic
// Auth (приоритет) либо из form-полей client_id/client_secret.
func clientCredentialsFromRequest(r *http.Request) (clientID, clientSecret string, ok bool) {
	if id, secret, basicOK := r.BasicAuth(); basicOK {
		return id, secret, true
	}
	id := r.PostFormValue("client_id")
	secret := r.PostFormValue("client_secret")
	if id == "" || secret == "" {
		return "", "", false
	}
	return id, secret, true
}

// deviceAuthorization — POST /device_authorization (RFC 8628 §3.1/§3.2).
// Выдаёт device_code (для поллинга клиентом) и user_code (для ручного ввода
// пользователем на "втором экране"), сохраняет pending-состояние и
// возвращает verification_uri(+_complete), expires_in, interval.
func (h *Handler) deviceAuthorization(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	}

	clientID := r.PostFormValue("client_id")
	if clientID == "" {
		clientID = defaultClientID
	}

	deviceCode, err := token.OpaqueHex(16)
	if err != nil {
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}
	userCode, err := token.Code()
	if err != nil {
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}
	normalizedUserCode := token.NormalizeCode(userCode)

	h.mu.Lock()
	h.devices[deviceCode] = deviceAuth{
		userCode:  userCode,
		status:    deviceStatusPending,
		email:     "mockuser@example.com",
		name:      "Mock User",
		clientID:  clientID,
		expiresAt: time.Now().Add(deviceCodeTTL),
	}
	h.deviceUserCodes[normalizedUserCode] = deviceCode
	h.mu.Unlock()

	verificationURI := baseURL(r) + "/device"
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               deviceCode,
		"user_code":                 userCode,
		"verification_uri":          verificationURI,
		"verification_uri_complete": verificationURI + "?user_code=" + url.QueryEscape(userCode),
		"expires_in":                int(deviceCodeTTL.Seconds()),
		"interval":                  deviceInterval,
	})
}

// deviceApprove — POST /device/approve (demo-эндпоинт, НЕ часть RFC 8628:
// в реальном IdP это обычная страница верификации, где залогиненный
// пользователь вводит user_code и подтверждает вход; здесь она сведена к
// одному API-вызову для живого прогона стенда). По user_code находит
// связанный device_code и помечает его approved — следующий poll /token
// выдаст токен.
func (h *Handler) deviceApprove(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	}
	normalizedUserCode := token.NormalizeCode(r.PostFormValue("user_code"))

	h.mu.Lock()
	defer h.mu.Unlock()

	deviceCode, ok := h.deviceUserCodes[normalizedUserCode]
	if !ok {
		http.Error(w, "invalid_request: unknown user_code", http.StatusBadRequest)
		return
	}
	dev, ok := h.devices[deviceCode]
	if !ok {
		http.Error(w, "invalid_request: unknown user_code", http.StatusBadRequest)
		return
	}
	if time.Now().After(dev.expiresAt) {
		http.Error(w, "invalid_request: user_code expired", http.StatusBadRequest)
		return
	}

	dev.status = deviceStatusApproved
	h.devices[deviceCode] = dev

	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

// deviceVerificationPage — GET /device: значение verification_uri, отдаваемое
// deviceAuthorization (RFC 8628 §3.2). В реальном IdP это полноценная
// HTML-страница, где залогиненный пользователь вводит user_code и
// подтверждает вход; без неё verification_uri резолвился бы в 404. Для
// demo-стенда — минимальная учебная заглушка: показывает user_code, если он
// передан через ?user_code= (как в verification_uri_complete), и объясняет,
// что фактическое подтверждение выполняется вызовом POST /device/approve
// (см. deviceApprove) — сам этот handler approve не вызывает.
func (h *Handler) deviceVerificationPage(w http.ResponseWriter, r *http.Request) {
	userCode := r.URL.Query().Get("user_code")

	var codeLine string
	if userCode != "" {
		codeLine = "<p>Код устройства: <code>" + html.EscapeString(userCode) + "</code></p>"
	} else {
		codeLine = "<p>Введите код, показанный вашим устройством.</p>"
	}

	body := "<!doctype html><html><head><meta charset=\"utf-8\">" +
		"<title>Device verification (mockidp demo)</title></head><body>" +
		"<h1>Подтверждение входа с устройства</h1>" +
		"<p>Это учебная заглушка mockidp (verification_uri из RFC 8628 device authorization flow), " +
		"не полноценный UI реального IdP.</p>" +
		codeLine +
		"<p>Подтверждение выполняется вызовом <code>POST /device/approve</code> с полем " +
		"<code>user_code</code> (см. deviceApprove) — эта страница только объясняет поток и сама " +
		"approve не вызывает.</p></body></html>"

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// tokenDeviceCode — POST /token grant_type=urn:ietf:params:oauth:grant-type:device_code
// (RFC 8628 §3.4/§3.5). Ошибки возвращаются как JSON {"error": "..."} с
// HTTP 400 (RFC 6749 §5.2, на которое ссылается RFC 8628): invalid_grant для
// неизвестного device_code, expired_token для истёкшего, authorization_pending
// пока пользователь не подтвердил. При успехе device_code одноразовый —
// claim (чтение статуса + delete из h.devices/h.deviceUserCodes) происходит
// атомарно под ОДНИМ Lock, как только видим approved; выдача токена (issueIDToken,
// генерация access_token) уже идёт вне лока по локальной копии dev. Так два
// конкурентных poll с одним approved device_code не оба пройдут — второй не
// найдёт device_code в h.devices (уже удалён первым) и получит invalid_grant,
// как и для authorization_code выше. pending-статус НЕ удаляет device — иначе
// клиент не смог бы повторно опрашивать до подтверждения.
func (h *Handler) tokenDeviceCode(w http.ResponseWriter, r *http.Request) {
	deviceCode := r.PostFormValue("device_code")
	if deviceCode == "" {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", "")
		return
	}

	h.mu.Lock()
	dev, ok := h.devices[deviceCode]
	if !ok {
		h.mu.Unlock()
		writeTokenError(w, http.StatusBadRequest, "invalid_grant", "")
		return
	}
	if time.Now().After(dev.expiresAt) {
		delete(h.devices, deviceCode)
		delete(h.deviceUserCodes, token.NormalizeCode(dev.userCode))
		h.mu.Unlock()
		writeTokenError(w, http.StatusBadRequest, "expired_token", "")
		return
	}
	if dev.status != deviceStatusApproved {
		h.mu.Unlock()
		writeTokenError(w, http.StatusBadRequest, "authorization_pending", "")
		return
	}
	// approved — claim атомарно здесь же, под тем же Lock, до генерации
	// токенов: второй конкурентный запрос с этим device_code уже не найдёт
	// его в h.devices и получит invalid_grant.
	delete(h.devices, deviceCode)
	delete(h.deviceUserCodes, token.NormalizeCode(dev.userCode))
	h.mu.Unlock()

	sub := subjectFromEmail(dev.email)
	idToken, err := h.issueIDToken(sub, dev.email, dev.name, baseURL(r), dev.clientID)
	if err != nil {
		writeTokenError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	accessToken, err := token.OpaqueHex(24)
	if err != nil {
		writeTokenError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	h.mu.Lock()
	h.tokens[accessToken] = issuedProfile{
		Sub:       sub,
		Email:     dev.email,
		Name:      dev.name,
		Scope:     "openid profile email",
		ClientID:  dev.clientID,
		ExpiresAt: time.Now().Add(accessTokenTTL),
	}
	h.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{
		"id_token":     idToken,
		"access_token": accessToken,
		"token_type":   "Bearer",
	})
}

// userinfo — GET /userinfo, Authorization: Bearer <access_token> -> профиль
// {sub, email, name}.
func (h *Handler) userinfo(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || tok == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	h.mu.Lock()
	profile, ok := h.tokens[tok]
	h.mu.Unlock()
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"sub":   profile.Sub,
		"email": profile.Email,
		"name":  profile.Name,
	})
}

// introspect — POST /introspect (token=...), RFC 7662. Требует
// аутентификации клиента (RFC 7662 §2.1: introspection endpoint — это
// protected resource, вызывающий клиент обязан аутентифицироваться) — те же
// демо-креды, что и grant_type=client_credentials (demo-client/demo-secret),
// через HTTP Basic Auth или form-поля client_id/client_secret. Без валидных
// креденшелов — 401, ДО чтения token= вовсе: иначе любой невовлечённый
// клиент мог бы интроспектировать чужие токены и узнавать sub/scope/exp.
// Для неизвестного или просроченного токена (уже аутентифицированного
// вызова) возвращает {"active": false} (и намеренно не различает эти два
// случая в ответе — как и полагается introspection endpoint'у: детали не
// должны раскрываться клиенту).
func (h *Handler) introspect(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	}

	clientID, clientSecret, ok := clientCredentialsFromRequest(r)
	if !ok || clientID != demoClientCredentialsID || clientSecret != demoClientCredentialsSecret {
		http.Error(w, "invalid_client", http.StatusUnauthorized)
		return
	}

	tok := r.PostFormValue("token")

	h.mu.Lock()
	profile, ok := h.tokens[tok]
	h.mu.Unlock()

	if !ok || time.Now().After(profile.ExpiresAt) {
		writeJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}

	resp := map[string]any{
		"active":     true,
		"sub":        profile.Sub,
		"exp":        profile.ExpiresAt.Unix(),
		"token_type": "Bearer",
	}
	if profile.Scope != "" {
		resp["scope"] = profile.Scope
	}
	if profile.ClientID != "" {
		resp["client_id"] = profile.ClientID
	}
	writeJSON(w, http.StatusOK, resp)
}

// discovery — GET /.well-known/openid-configuration (OIDC Discovery 1.0).
// issuer и все *_endpoint вычисляются от фактического хоста запроса — так
// discovery остаётся верным независимо от того, на каком адресе/порту
// httptest.Server или реальный процесс сейчас слушает.
// device_authorization_endpoint указывает на реализованный /device_authorization
// (RFC 8628 device authorization flow).
func (h *Handler) discovery(w http.ResponseWriter, r *http.Request) {
	base := baseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/authorize",
		"token_endpoint":                        base + "/token",
		"userinfo_endpoint":                     base + "/userinfo",
		"jwks_uri":                              base + "/.well-known/jwks.json",
		"introspection_endpoint":                base + "/introspect",
		"device_authorization_endpoint":         base + "/device_authorization",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "client_credentials", "urn:ietf:params:oauth:grant-type:device_code"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
		"code_challenge_methods_supported":      []string{"S256"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

// jwks — GET /.well-known/jwks.json. Публикует публичную часть RSA-ключа
// подписи id_token (n/e в base64url без padding, как того требует RFC 7517).
func (h *Handler) jwks(w http.ResponseWriter, r *http.Request) {
	pub := h.signingKey.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"use": "sig",
				"alg": "RS256",
				"kid": h.kid,
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			},
		},
	})
}

// baseURL вычисляет схему+хост текущего запроса — используется и как issuer,
// и как база всех *_endpoint в discovery, и как значение iss в id_token
// (должны совпадать: клиент валидирует id_token против issuer из discovery).
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// computeKid детерминированно производит kid из отпечатка публичного ключа
// (первые 8 байт sha256 от DER-кодированного SubjectPublicKeyInfo).
func computeKid(pub *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		// x509.MarshalPKIXPublicKey не может провалиться на валидном
		// *rsa.PublicKey — фиксированный fallback только чтобы kid не был
		// пустым в теоретически недостижимом случае.
		return "mockidp-key"
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:8])
}

// verifyPKCE проверяет, что base64url(sha256(verifier)) (без padding)
// совпадает с challenge, полученным на /authorize (S256-метод из RFC 7636).
func verifyPKCE(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return computed == challenge
}

// subjectFromEmail детерминированно производит sub из email для demo-целей.
func subjectFromEmail(email string) string {
	sum := sha256.Sum256([]byte("mockidp:" + email))
	return hex.EncodeToString(sum[:])[:16]
}

// issueIDToken подписывает id_token алгоритмом RS256 ключом h.signingKey,
// проставляя kid в заголовок — клиент по kid находит нужный публичный ключ в
// JWKS (см. пакет oidc, ValidateIDToken).
func (h *Handler) issueIDToken(sub, email, name, iss, aud string) (string, error) {
	now := time.Now()
	claims := idTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    iss,
			Subject:   sub,
			Audience:  jwt.ClaimStrings{aud},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(idTokenTTL)),
		},
		Email: email,
		Name:  name,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = h.kid
	return tok.SignedString(h.signingKey)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
