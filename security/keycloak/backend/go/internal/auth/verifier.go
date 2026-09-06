// Package auth реализует проверку access-токенов Keycloak двумя способами:
// локальная проверка подписи по JWKS (offline, режим "jwks") и обращение к
// introspection-эндпоинту IdP (online, режим "introspect"). В обоих случаях
// роли достаются из claim realm_access.roles, как их кладёт Keycloak.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
)

// Mode — способ валидации токена.
type Mode string

const (
	// ModeJWKS — локальная проверка подписи по ключам из JWKS (без обращения к IdP на каждый запрос).
	ModeJWKS Mode = "jwks"
	// ModeIntrospect — проверка через RFC 7662 introspection на стороне Keycloak.
	ModeIntrospect Mode = "introspect"
)

// Principal — извлечённые из токена данные, которые прокидываются в хендлеры.
type Principal struct {
	Subject string   `json:"sub"`
	Roles   []string `json:"roles"`
}

// ErrUpstreamUnavailable сигнализирует об ОПЕРАЦИОННОМ сбое обращения к IdP при
// introspection: сетевая ошибка, таймаут, HTTP 5xx / любой не-200, нечитаемый или
// невалидный JSON ответа. Это НЕ вердикт по токену. Middleware превращает такую
// ошибку в HTTP 503 (Service Unavailable), а не в 401: токен может быть полностью
// валиден, просто проверить его прямо сейчас невозможно из-за недоступности IdP.
// Только валидный ответ introspection с active:false даёт 401.
var ErrUpstreamUnavailable = errors.New("idp introspection unavailable")

type ctxKey struct{}

// FromContext возвращает Principal, положенный Middleware в контекст запроса.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// realmClaims отражает часть payload токена/introspection-ответа Keycloak.
type realmClaims struct {
	Subject     string `json:"sub"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// Verifier инкапсулирует выбранный режим проверки и его зависимости.
type Verifier struct {
	mode       Mode
	audience   string
	logger     *slog.Logger
	httpClient *http.Client
	// ModeJWKS: свой кэш JWKS + верификация через go-jose. RemoteKeySet.VerifySignature
	// вызывать напрямую нельзя — go-oidc помечает его как «Users MUST NOT call this method»
	// (пропускает часть проверок, экспортирован лишь для реализаций KeySet).
	issuer       string
	jwksURL      string
	expectedAlg  jose.SignatureAlgorithm // ЕДИНСТВЕННЫЙ ожидаемый алгоритм подписи (RFC 8725)
	expectedTyp  string                  // ожидаемый CLAIM typ (Keycloak access = "Bearer"; НЕ header — там всегда "JWT")
	jwksMu       sync.RWMutex            // защищает jwks
	jwks         jose.JSONWebKeySet
	fetchMu      sync.Mutex    // коалессация on-demand refetch + защита backoff-состояния
	backoff      time.Duration // текущий экспоненциальный backoff ПОСЛЕ ошибок refresh (под fetchMu)
	backoffUntil time.Time     // до этого времени on-demand refetch пропускается (под fetchMu)
	bucket       *tokenBucket  // лимитер on-demand refetch: защита IdP от потока случайных kid
	stopCh       chan struct{} // остановка фонового обновления JWKS
	stopOnce     sync.Once
	// ModeIntrospect:
	introspectURL string
	clientID      string
	clientSecret  string
}

// Config — параметры конструктора Verifier.
type Config struct {
	Mode         Mode
	Issuer       string
	Audience     string
	ClientID     string // confidential-клиент для introspection (например, backend)
	ClientSecret string
	// ExpectedAlg — ЕДИНСТВЕННЫЙ допустимый алгоритм подписи (дефолт "RS256"). ExpectedTyp —
	// ожидаемый CLAIM typ (дефолт "Bearer" — тип access-токена Keycloak; тип лежит в claim,
	// НЕ в header). Отсекает подстановку id_token/refresh. Применяется в ОБОИХ режимах.
	ExpectedAlg string
	ExpectedTyp string
	// JWKSRefreshInterval — период фонового проактивного обновления JWKS (ModeJWKS).
	// 0 → дефолт 5 мин; отрицательное → фоновое обновление выключено.
	JWKSRefreshInterval time.Duration
	Logger              *slog.Logger
}

// New создаёт Verifier. Для ModeJWKS выполняется OIDC discovery по issuer (go-oidc —
// только для discovery), затем строится собственный кэш JWKS; проверку JWT делает go-jose.
// Для ModeIntrospect discovery не требуется — introspection-URL строится из issuer.
func New(ctx context.Context, cfg Config) (*Verifier, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}

	v := &Verifier{
		mode:       cfg.Mode,
		audience:   cfg.Audience,
		logger:     logger,
		httpClient: httpClient,
	}

	switch cfg.Mode {
	case ModeJWKS:
		// OIDC discovery по issuer, из документа берём jwks_uri — портируемо, без хардкода
		// Keycloak-specific пути /protocol/openid-connect/certs.
		provider, err := oidc.NewProvider(oidc.ClientContext(ctx, httpClient), cfg.Issuer)
		if err != nil {
			return nil, fmt.Errorf("oidc discovery %q: %w", cfg.Issuer, err)
		}
		var disc struct {
			JWKSURL string `json:"jwks_uri"`
		}
		if err := provider.Claims(&disc); err != nil {
			return nil, fmt.Errorf("read discovery document: %w", err)
		}
		if disc.JWKSURL == "" {
			return nil, errors.New("discovery: empty jwks_uri")
		}
		// go-oidc используем ТОЛЬКО для discovery (поддерживаемый вызов). Саму проверку JWT
		// делаем через go-jose: единственный ожидаемый alg + typ + подпись + iss/aud/exp/nbf.
		v.issuer = cfg.Issuer
		v.jwksURL = disc.JWKSURL
		v.expectedAlg = jose.SignatureAlgorithm(orDefault(cfg.ExpectedAlg, "RS256"))
		v.expectedTyp = orDefault(cfg.ExpectedTyp, "Bearer")
		v.bucket = newTokenBucket(jwksRefetchBurst, jwksRefetchPerSec, time.Now())
		if err := v.refreshJWKS(ctx); err != nil {
			return nil, fmt.Errorf("initial jwks fetch: %w", err)
		}
		interval := cfg.JWKSRefreshInterval
		if interval == 0 {
			interval = jwksRefreshDefault
		}
		v.startBackgroundRefresh(interval) // проактивное обновление; 0<0 → выключено
		logger.Info("verifier ready", "mode", cfg.Mode, "issuer", cfg.Issuer, "audience", cfg.Audience,
			"jwks", disc.JWKSURL, "alg", string(v.expectedAlg), "typ", v.expectedTyp)
	case ModeIntrospect:
		if cfg.ClientID == "" || cfg.ClientSecret == "" {
			return nil, errors.New("introspect mode requires client_id and client_secret")
		}
		v.introspectURL = strings.TrimRight(cfg.Issuer, "/") + "/protocol/openid-connect/token/introspect"
		v.clientID = cfg.ClientID
		v.clientSecret = cfg.ClientSecret
		v.expectedTyp = orDefault(cfg.ExpectedTyp, "Bearer") // typ проверяем и в introspect-режиме
		logger.Info("verifier ready", "mode", cfg.Mode, "introspect_url", v.introspectURL, "typ", v.expectedTyp)
	default:
		return nil, fmt.Errorf("unknown auth mode %q", cfg.Mode)
	}

	return v, nil
}

// Middleware проверяет Bearer-токен и, если requiredRole != "", наличие роли.
// Коды ответа: 401 — токен отсутствует/невалиден (валидный вердикт IdP);
// 403 — токен валиден, но нужной роли нет; 503 — операционный сбой обращения к IdP
// в режиме introspect (сеть/таймаут/5xx/битый ответ), когда вердикт по токену
// получить невозможно (см. ErrUpstreamUnavailable).
func (v *Verifier) Middleware(next http.Handler, requiredRole string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := bearerToken(r)
		if err != nil {
			v.deny(w, http.StatusUnauthorized, "missing bearer token")
			return
		}

		principal, err := v.validate(r.Context(), raw)
		if err != nil {
			// Операционный сбой IdP (сеть/таймаут/5xx/битый ответ) ≠ невалидный токен:
			// отдаём 503, чтобы клиент/балансировщик мог повторить, а не считал токен
			// отозванным. Настоящий вердикт "неактивен/нет роли" даёт 401/403 ниже.
			if errors.Is(err, ErrUpstreamUnavailable) {
				v.logger.Error("token validation unavailable: idp introspection failed", "err", err)
				v.deny(w, http.StatusServiceUnavailable, "token validation temporarily unavailable")
				return
			}
			v.logger.Warn("token rejected", "err", err)
			v.deny(w, http.StatusUnauthorized, "invalid token")
			return
		}

		if requiredRole != "" && !hasRole(principal.Roles, requiredRole) {
			v.deny(w, http.StatusForbidden, "missing required role")
			return
		}

		ctx := context.WithValue(r.Context(), ctxKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// validate выполняет проверку токена согласно выбранному режиму.
func (v *Verifier) validate(ctx context.Context, raw string) (Principal, error) {
	switch v.mode {
	case ModeJWKS:
		return v.validateJWKS(ctx, raw)
	case ModeIntrospect:
		return v.validateIntrospect(ctx, raw)
	default:
		return Principal{}, fmt.Errorf("unknown auth mode %q", v.mode)
	}
}

// leeway — допустимый рассинхрон часов при проверке exp/nbf.
const leeway = 30 * time.Second

// Параметры JWKS-кэша (см. keyByKID и startBackgroundRefresh).
const (
	jwksRefreshDefault = 5 * time.Minute // период фонового проактивного обновления JWKS
	jwksBackoffInitial = time.Second     // старт экспоненциального backoff ПОСЛЕ ошибок refresh
	jwksBackoffMax     = 30 * time.Second
	// Token bucket on-demand refetch: не более refill/сек с burst=capacity. Ограничивает
	// поток запросов со СЛУЧАЙНЫМИ kid, чтобы он не превратил JWKS в online-зависимость.
	jwksRefetchBurst  = 5.0 // burst — разово столько refetch подряд (напр., атака + ротация)
	jwksRefetchPerSec = 1.0 // установившийся темп on-demand refetch
)

// nextBackoff — экспоненциальный рост backoff с потолком.
func nextBackoff(cur time.Duration) time.Duration {
	switch {
	case cur <= 0:
		return jwksBackoffInitial
	case cur*2 > jwksBackoffMax:
		return jwksBackoffMax
	default:
		return cur * 2
	}
}

// tokenBucket — лимитер on-demand refetch JWKS (потокобезопасный).
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	capacity float64
	refill   float64 // токенов в секунду
	last     time.Time
}

func newTokenBucket(capacity, refillPerSec float64, now time.Time) *tokenBucket {
	return &tokenBucket{tokens: capacity, capacity: capacity, refill: refillPerSec, last: now}
}

// allow забирает токен, если он есть (с учётом пополнения за прошедшее время).
func (b *tokenBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = min(b.capacity, b.tokens+elapsed*b.refill)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// refreshJWKS перезагружает JWKS с jwks_uri в кэш (потокобезопасно).
func (v *Verifier) refreshJWKS(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var ks jose.JSONWebKeySet
	if err := json.Unmarshal(body, &ks); err != nil {
		return err
	}
	v.jwksMu.Lock()
	v.jwks = ks
	v.jwksMu.Unlock()
	return nil
}

// lookupKey ищет в кэше подписной ключ по kid (шифровальные ключи use=enc пропускаем).
func (v *Verifier) lookupKey(kid string) *jose.JSONWebKey {
	v.jwksMu.RLock()
	defer v.jwksMu.RUnlock()
	for i := range v.jwks.Keys {
		k := &v.jwks.Keys[i]
		if k.KeyID == kid && k.Use != "enc" {
			return k
		}
	}
	return nil
}

// keyByKID возвращает подписной ключ по kid. Неизвестный kid — типичный признак ротации
// ключей realm — обрабатывается как небольшой state machine:
//   - КОАЛЕССАЦИЯ: конкурентные промахи (в т.ч. по РАЗНЫМ kid) сериализуются fetchMu, поэтому
//     JWKS перезапрашивается один раз; после успешного refetch остальные находят свой kid
//     в обновлённом кэше без нового запроса;
//   - НЕТ отравления cooldown: промах по kid (refetch удался, ключа нет) НЕ ставит backoff —
//     поэтому легитимная ротация СРАЗУ после токена с неизвестным (в т.ч. поддельным) kid
//     не блокируется (при наличии токенов в бакете);
//   - RATE LIMIT: on-demand refetch ограничен токен-бакетом (burst jwksRefetchBurst,
//     установившийся темп jwksRefetchPerSec/сек). Поток JWT со СЛУЧАЙНЫМИ kid НЕ превращает
//     JWKS в online-зависимость горячего пути: при исчерпании токенов новый kid отклоняется
//     БЕЗ запроса к IdP. Точная граница числа refetch за окно длиной elapsed —
//     burst + refillRate·elapsed (а не N=число мусорных kid);
//   - BACKOFF только на ОШИБКАХ refresh (сеть/5xx/битый JSON): экспоненциальный, чтобы не
//     долбить недоступный IdP; сбрасывается при первом успешном refetch;
//   - плюс фоновое проактивное обновление (startBackgroundRefresh) держит ключи свежими
//     независимо от запросов.
//
// Компромисс явный: при штатной работе ротация подхватывается on-demand мгновенно; под
// потоком мусорных kid (бакет исчерпан) легитимный новый ключ принимается не позднее
// следующего УСПЕШНОГО фонового обновления (обычно ≤ интервала, дефолт 5 мин; но если JWKS
// недоступен — timeout/5xx — строгой верхней границы нет, обновление тоже ждёт восстановления).
// Абсолютно мгновенную ротацию при любом объёме мусорных kid И неограниченную защиту IdP
// одновременно гарантировать нельзя.
func (v *Verifier) keyByKID(ctx context.Context, kid string) (*jose.JSONWebKey, error) {
	if k := v.lookupKey(kid); k != nil {
		return k, nil
	}
	v.fetchMu.Lock()
	defer v.fetchMu.Unlock()
	// Пока ждали fetchMu, другая горутина могла уже обновить JWKS — перепроверяем (коалессация).
	if k := v.lookupKey(kid); k != nil {
		return k, nil
	}
	// Backoff ставится ТОЛЬКО после ошибок refresh — во время него к IdP не ходим.
	if time.Now().Before(v.backoffUntil) {
		return nil, fmt.Errorf("no signing key for kid %q (jwks refresh backing off after error)", kid)
	}
	// Rate limit on-demand refetch: поток JWT со случайными kid не должен превращать JWKS в
	// online-эндпоинт. При исчерпании токен-бакета отклоняем kid до пополнения токена или
	// следующего ФОНОВОГО обновления — амплификация ограничена.
	if !v.bucket.allow(time.Now()) {
		return nil, fmt.Errorf("no signing key for kid %q (jwks refetch rate-limited)", kid)
	}
	if err := v.refreshJWKS(ctx); err != nil {
		v.backoff = nextBackoff(v.backoff)
		v.backoffUntil = time.Now().Add(v.backoff)
		return nil, fmt.Errorf("refresh jwks on kid miss: %w", err)
	}
	v.backoff, v.backoffUntil = 0, time.Time{} // успех → сброс backoff; промах по kid его НЕ ставит
	if k := v.lookupKey(kid); k != nil {
		return k, nil
	}
	return nil, fmt.Errorf("no signing key for kid %q", kid)
}

// startBackgroundRefresh запускает фоновое проактивное обновление JWKS каждые interval.
// interval <= 0 — обновление выключено. Останавливается через Close().
func (v *Verifier) startBackgroundRefresh(interval time.Duration) {
	if interval <= 0 {
		return
	}
	v.stopCh = make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-v.stopCh:
				return
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := v.refreshJWKS(ctx); err != nil {
					v.logger.Warn("periodic jwks refresh failed", "err", err)
				}
				cancel()
			}
		}
	}()
}

// Close останавливает фоновое обновление JWKS. Идемпотентен; безопасен, если фон не запускался.
func (v *Verifier) Close() {
	if v.stopCh != nil {
		v.stopOnce.Do(func() { close(v.stopCh) })
	}
}

// audience разбирает claim aud, который по RFC 7519 может быть строкой ИЛИ массивом строк.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

func (a audience) contains(want string) bool {
	for _, v := range a {
		if v == want {
			return true
		}
	}
	return false
}

// accessClaims — проверяемые claims access-токена (embed realmClaims даёт sub + роли).
type accessClaims struct {
	Issuer    string   `json:"iss"`
	Audience  audience `json:"aud"`
	Expiry    int64    `json:"exp"`
	NotBefore int64    `json:"nbf"`
	Typ       string   `json:"typ"` // Keycloak: тип токена в CLAIM (Bearer/ID/Refresh), не в header
	realmClaims
}

// validateJWKS — переносимый offline-верификатор ACCESS-токена на go-jose (именно
// access-token, не id_token). Шаги, как рекомендует JWT BCP (RFC 8725):
//  1. разбор с ЕДИНСТВЕННЫМ ожидаемым алгоритмом (go-jose отвергает любой другой, в т.ч.
//     none и «разрешённый, но не ожидаемый» — downgrade невозможен);
//  2. выбор ключа по kid + связывание ключ↔алгоритм (alg ключа == ожидаемый);
//  3. проверка подписи ключом;
//  4. явные проверки claims: typ (в Keycloak тип токена лежит в CLAIM: Bearer/ID/Refresh —
//     а НЕ в header typ, который здесь всегда "JWT"; так отсекается подстановка id_token),
//     iss (строго), aud (содержит наш), exp, nbf.
//
// Подход не привязан к Keycloak (кроме realm_access.roles и claim typ): работает с любым
// IdP, отдающим JWT access-token и стандартный JWKS.
func (v *Verifier) validateJWKS(ctx context.Context, raw string) (Principal, error) {
	// 1. Разбор строго с ожидаемым алгоритмом.
	tok, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{v.expectedAlg})
	if err != nil {
		return Principal{}, fmt.Errorf("parse jws: %w", err)
	}
	if len(tok.Signatures) != 1 {
		return Principal{}, errors.New("want exactly one signature")
	}
	hdr := tok.Signatures[0].Header

	// 2. Ключ по kid + связывание ключ↔алгоритм (RFC 8725: один ключ — один алгоритм).
	key, err := v.keyByKID(ctx, hdr.KeyID)
	if err != nil {
		return Principal{}, err
	}
	if key.Algorithm != "" && key.Algorithm != string(v.expectedAlg) {
		return Principal{}, fmt.Errorf("key alg %q != expected %q", key.Algorithm, v.expectedAlg)
	}

	// 3. Проверка подписи ключом (go-jose сопоставляет alg подписи с ключом).
	payload, err := tok.Verify(key)
	if err != nil {
		return Principal{}, fmt.Errorf("verify signature: %w", err)
	}

	// 4. Явные проверки claims access-токена. typ ПЕРВЫМ: в Keycloak тип токена лежит в
	//    CLAIM typ (Bearer/ID/Refresh), а не в header — это отсекает подстановку id_token.
	var c accessClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Principal{}, fmt.Errorf("parse claims: %w", err)
	}
	if c.Typ != v.expectedTyp {
		return Principal{}, fmt.Errorf("unexpected token typ %q (want %q)", c.Typ, v.expectedTyp)
	}
	if c.Issuer != v.issuer {
		return Principal{}, fmt.Errorf("issuer mismatch: %q != %q", c.Issuer, v.issuer)
	}
	if !c.Audience.contains(v.audience) {
		return Principal{}, fmt.Errorf("audience %v does not contain %q", []string(c.Audience), v.audience)
	}
	now := time.Now()
	if c.Expiry == 0 || now.After(time.Unix(c.Expiry, 0).Add(leeway)) {
		return Principal{}, errors.New("token expired or missing exp")
	}
	if c.NotBefore != 0 && now.Add(leeway).Before(time.Unix(c.NotBefore, 0)) {
		return Principal{}, errors.New("token not yet valid (nbf)")
	}
	return Principal{Subject: c.Subject, Roles: c.RealmAccess.Roles}, nil
}

func (v *Verifier) validateIntrospect(ctx context.Context, raw string) (Principal, error) {
	form := url.Values{}
	form.Set("token", raw)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.introspectURL, strings.NewReader(form.Encode()))
	if err != nil {
		// Не удалось собрать запрос к IdP — операционная проблема, не вердикт по токену.
		return Principal{}, fmt.Errorf("build introspect request: %w: %w", ErrUpstreamUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(v.clientID, v.clientSecret)

	resp, err := v.httpClient.Do(req)
	if err != nil {
		// Сетевой сбой или таймаут (context deadline) до IdP → 503, не 401.
		return Principal{}, fmt.Errorf("introspect call: %w: %w", ErrUpstreamUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Principal{}, fmt.Errorf("read introspect body: %w: %w", ErrUpstreamUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		// Любой не-200 (5xx, 4xx на самом introspection-эндпоинте, ошибка аутентификации
		// resource server'а к IdP и т.п.) — операционная проблема доступа к IdP, а не
		// вывод "токен пользователя невалиден". По RFC 7662 корректный вердикт приходит
		// только как 200 + {"active": ...}. Отдаём 503.
		return Principal{}, fmt.Errorf("introspect http %d: %w", resp.StatusCode, ErrUpstreamUnavailable)
	}

	var ir struct {
		Active bool   `json:"active"`
		Typ    string `json:"typ"` // introspection возвращает claim typ токена
		realmClaims
	}
	if err := json.Unmarshal(body, &ir); err != nil {
		// Ответ пришёл, но нечитаем (не JSON / обрезан) — IdP отвечает некорректно → 503.
		return Principal{}, fmt.Errorf("decode introspect: %w: %w", ErrUpstreamUnavailable, err)
	}
	if !ir.Active {
		// Валидный ответ IdP, токен неактивен → 401.
		return Principal{}, errors.New("token inactive")
	}
	// Тип токена: даже active-токен может быть id_token/refresh — отсекаем по claim typ,
	// как и в JWKS-режиме (иначе introspection пропускает подстановку id_token).
	if ir.Typ != v.expectedTyp {
		return Principal{}, fmt.Errorf("unexpected token typ %q (want %q)", ir.Typ, v.expectedTyp)
	}
	return Principal{Subject: ir.Subject, Roles: ir.RealmAccess.Roles}, nil
}

func (v *Verifier) deny(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	if code == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="demo"`)
	}
	if code == http.StatusServiceUnavailable {
		// Сбой временный — клиенту/прокси имеет смысл повторить.
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		v.logger.Error("write deny response", "err", err)
	}
}

// bearerToken извлекает токен из заголовка Authorization: Bearer <token>.
func bearerToken(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errors.New("no authorization header")
	}
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", errors.New("not a bearer token")
	}
	token := strings.TrimSpace(h[len(prefix):])
	if token == "" {
		return "", errors.New("empty bearer token")
	}
	return token, nil
}

func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}
