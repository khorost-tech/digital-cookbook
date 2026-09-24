package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"khorost.tech/cookbook/auth/internal/session"
	"khorost.tech/cookbook/auth/internal/token"
)

// oauthStateTTL — время жизни записи oauth:state:{state} в Redis: окно, в
// течение которого пользователь должен успеть пройти consent на провайдере
// и вернуться на callback.
const oauthStateTTL = 5 * time.Minute

// ErrOAuthBadState возвращается, когда state из callback не найден в Redis —
// либо истёк, либо был подделан (защита от CSRF на OAuth-callback), либо
// уже был использован (GetDel одноразовый).
var ErrOAuthBadState = errors.New("oauth: unknown or expired state")

// oidcProfile — профиль, полученный от mock-OIDC-провайдера через /userinfo.
type oidcProfile struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// oauthHandler — HTTP-хендлеры OAuth authorization-code+PKCE потока поверх
// mock-OIDC-провайдера (mockidp). Один issuer/clientID на весь процесс —
// демо-стенд обслуживает один провайдер под именем {provider} в пути
// (значение сейчас не влияет на выбор issuer, оставлено параметром URL для
// реалистичности маршрута /oauth/{provider}/...).
type oauthHandler struct {
	rdb           *redis.Client
	secret        []byte
	issuer        string // базовый URL mock-OIDC-провайдера (.../authorize, .../token, .../userinfo)
	clientID      string
	publicURL     string // базовый публичный URL ЭТОГО сервиса — для построения redirect_uri
	httpClient    *http.Client
	secureCookies bool
}

func newOAuthHandler(rdb *redis.Client, secret []byte, issuer, clientID, publicURL string, secureCookies bool) *oauthHandler {
	return &oauthHandler{
		rdb:           rdb,
		secret:        secret,
		issuer:        strings.TrimRight(issuer, "/"),
		clientID:      clientID,
		publicURL:     strings.TrimRight(publicURL, "/"),
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		secureCookies: secureCookies,
	}
}

// startOAuth — GET /oauth/{provider}/start. Генерирует state (CSRF-защита) и
// PKCE-пару verifier/challenge (S256), сохраняет "{provider}|{verifier}" в
// Redis под oauth:state:{state} на oauthStateTTL и редиректит браузер на
// authorize-URL mock-провайдера.
func (h *oauthHandler) startOAuth(w http.ResponseWriter, r *http.Request) {
	provider := chi.URLParam(r, "provider")

	state, err := token.OpaqueHex(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	verifier, err := token.OpaqueHex(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	challenge := pkceChallengeS256(verifier)

	stateKey := "oauth:state:" + state
	stateVal := provider + "|" + verifier
	if err := h.rdb.Set(r.Context(), stateKey, stateVal, oauthStateTTL).Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	authorizeURL, err := url.Parse(h.issuer + "/authorize")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	q := authorizeURL.Query()
	q.Set("client_id", h.clientID)
	q.Set("redirect_uri", h.callbackURL(provider))
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	authorizeURL.RawQuery = q.Encode()

	http.Redirect(w, r, authorizeURL.String(), http.StatusFound)
}

// callbackOAuth — GET /oauth/{provider}/callback?code&state. Достаёт и сразу
// удаляет (GetDel — одноразово) запись oauth:state:{state}: отсутствие
// записи означает подделанный/истёкший/повторно использованный state
// (ErrOAuthBadState, сессия НЕ создаётся). При валидном state обменивает
// code+verifier на токены у mock-провайдера, получает профиль, детерминированно
// отображает его в Account и выпускает пару access/refresh
// (session.CreateTokenPair, loginMethod=provider) с cookie app_at (refresh —
// см. disclaimer у accessCookieName в http.go, FIX M-4).
func (h *oauthHandler) callbackOAuth(w http.ResponseWriter, r *http.Request) {
	provider := chi.URLParam(r, "provider")

	q := r.URL.Query()
	state := q.Get("state")
	code := q.Get("code")
	if state == "" || code == "" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	verifier, err := h.consumeState(r.Context(), provider, state)
	if err != nil {
		if errors.Is(err, ErrOAuthBadState) {
			writeError(w, http.StatusBadRequest, "invalid_state")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	profile, err := h.exchangeCode(r.Context(), code, verifier, h.callbackURL(provider))
	if err != nil {
		writeError(w, http.StatusBadGateway, "token_exchange_failed")
		return
	}

	acc := accountFromOAuthProfile(provider, profile)

	access, _, err := session.CreateTokenPair(r.Context(), h.rdb, h.secret, acc, provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	setAccessCookie(w, r, access, h.secureCookies)

	writeJSON(w, http.StatusOK, map[string]string{"access_token": access})
}

// consumeState читает и атомарно удаляет oauth:state:{state} (GetDel — state
// одноразовый: повторный callback с тем же state обязан провалиться так же,
// как поддельный). Возвращает PKCE verifier, если значение принадлежит provider.
func (h *oauthHandler) consumeState(ctx context.Context, provider, state string) (string, error) {
	val, err := h.rdb.GetDel(ctx, "oauth:state:"+state).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrOAuthBadState
	}
	if err != nil {
		return "", err
	}

	parts := strings.SplitN(val, "|", 2)
	if len(parts) != 2 || parts[0] != provider {
		return "", ErrOAuthBadState
	}

	return parts[1], nil
}

// exchangeCode обменивает authorization code на токены у mock-провайдера
// (POST {issuer}/token, PKCE verifier — mock проверяет S256(verifier) против
// challenge, сохранённого на /authorize) и получает профиль через
// {issuer}/userinfo с полученным access_token.
func (h *oauthHandler) exchangeCode(ctx context.Context, code, verifier, redirectURI string) (oidcProfile, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
		"client_id":     {h.clientID},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.issuer+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return oidcProfile{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return oidcProfile{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return oidcProfile{}, errors.New("oauth: token endpoint returned " + resp.Status)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return oidcProfile{}, err
	}
	if tokenResp.AccessToken == "" {
		return oidcProfile{}, errors.New("oauth: empty access_token in response")
	}

	return h.fetchUserinfo(ctx, tokenResp.AccessToken)
}

func (h *oauthHandler) fetchUserinfo(ctx context.Context, accessToken string) (oidcProfile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.issuer+"/userinfo", nil)
	if err != nil {
		return oidcProfile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return oidcProfile{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return oidcProfile{}, errors.New("oauth: userinfo endpoint returned " + resp.Status)
	}

	var profile oidcProfile
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return oidcProfile{}, err
	}
	if profile.Sub == "" {
		return oidcProfile{}, errors.New("oauth: userinfo response missing sub")
	}

	return profile, nil
}

func (h *oauthHandler) callbackURL(provider string) string {
	return h.publicURL + "/oauth/" + provider + "/callback"
}

// pkceChallengeS256 вычисляет PKCE code_challenge (RFC 7636, метод S256) из verifier.
func pkceChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// accountFromOAuthProfile детерминированно производит Account из профиля
// провайдера для demo-целей: AccountID — первые 8 байт sha256(provider+sub)
// как положительное int64. В реальной системе здесь был бы lookup/insert по
// (provider, sub) в таблице привязок внешних аккаунтов; если email уже
// зарегистрирован под другим методом входа — привязка к существующему
// аккаунту, иначе — авто-создание нового (для демо оба случая сведены к
// одной и той же детерминированной функции: одинаковый provider+sub всегда
// даёт один и тот же AccountID).
func accountFromOAuthProfile(provider string, p oidcProfile) session.Account {
	sum := sha256.Sum256([]byte(provider + ":" + p.Sub))
	id := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)

	nick := p.Name
	if nick == "" {
		nick = p.Email
	}

	return session.Account{ID: id, Nick: nick, Roles: []string{"user"}}
}
