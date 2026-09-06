package auth

// Unit-тесты контракта Middleware: какой HTTP-код получает клиент в каждом исходе
// проверки токена. Ключевая проверяемая инвариантность — операционный сбой обращения
// к IdP (introspection) даёт 503, а НЕ 401: 401 остаётся строго за валидным вердиктом
// "токен неактивен" (active:false). Тесты гоняют introspect-режим против фейкового
// introspection-эндпоинта (httptest) — без реального Keycloak.

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	_ "crypto/sha256" // регистрирует SHA-256 для crypto.SHA256.New()
	_ "crypto/sha512" // регистрирует SHA-512 для crypto.SHA512.New()
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newIntrospectVerifier поднимает фейковый introspection-эндпоинт с заданным
// обработчиком и строит Verifier в режиме introspect, нацеленный на него.
// В introspect-режиме New() не делает OIDC discovery, поэтому фейкового сервера
// достаточно; путь запроса обработчик игнорирует.
func newIntrospectVerifier(t *testing.T, h http.HandlerFunc) (*Verifier, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	v, err := New(context.Background(), Config{
		Mode:         ModeIntrospect,
		Issuer:       srv.URL, // introspectURL = Issuer + /protocol/openid-connect/token/introspect
		Audience:     "backend",
		ClientID:     "backend",
		ClientSecret: "s3cr3t-demo",
	})
	if err != nil {
		srv.Close()
		t.Fatalf("New(introspect): %v", err)
	}
	return v, srv
}

// introspectJSON — обработчик, отдающий заданный статус и тело.
func introspectJSON(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// runMiddleware прогоняет один запрос с Bearer-токеном через Middleware и
// возвращает HTTP-код ответа. Терминальный хендлер (доступ разрешён) отдаёт 200.
// Пустой token означает запрос без заголовка Authorization.
func runMiddleware(v *Verifier, requiredRole, token string) int {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := v.Middleware(next, requiredRole)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestMiddlewareIntrospectStatusCodes(t *testing.T) {
	const activeAdmin = `{"active":true,"typ":"Bearer","sub":"u1","realm_access":{"roles":["admin","user"]}}`
	const activeUser = `{"active":true,"typ":"Bearer","sub":"u2","realm_access":{"roles":["user"]}}`

	t.Run("валидный токен + нужная роль → 200", func(t *testing.T) {
		v, srv := newIntrospectVerifier(t, introspectJSON(http.StatusOK, activeAdmin))
		defer srv.Close()
		if got := runMiddleware(v, "admin", "tok"); got != http.StatusOK {
			t.Fatalf("ожидали 200, получили %d", got)
		}
	})

	t.Run("валидный токен, роли не хватает → 403", func(t *testing.T) {
		v, srv := newIntrospectVerifier(t, introspectJSON(http.StatusOK, activeUser))
		defer srv.Close()
		if got := runMiddleware(v, "admin", "tok"); got != http.StatusForbidden {
			t.Fatalf("ожидали 403, получили %d", got)
		}
	})

	t.Run("active:false (валидный вердикт IdP) → 401", func(t *testing.T) {
		v, srv := newIntrospectVerifier(t, introspectJSON(http.StatusOK, `{"active":false}`))
		defer srv.Close()
		if got := runMiddleware(v, "", "tok"); got != http.StatusUnauthorized {
			t.Fatalf("ожидали 401, получили %d", got)
		}
	})

	t.Run("introspection отвечает 5xx → 503 (операционный сбой, не 401)", func(t *testing.T) {
		v, srv := newIntrospectVerifier(t, introspectJSON(http.StatusInternalServerError, "boom"))
		defer srv.Close()
		if got := runMiddleware(v, "", "tok"); got != http.StatusServiceUnavailable {
			t.Fatalf("ожидали 503, получили %d", got)
		}
	})

	t.Run("introspection вернул не-JSON (200) → 503", func(t *testing.T) {
		v, srv := newIntrospectVerifier(t, introspectJSON(http.StatusOK, "not-a-json"))
		defer srv.Close()
		if got := runMiddleware(v, "", "tok"); got != http.StatusServiceUnavailable {
			t.Fatalf("ожидали 503, получили %d", got)
		}
	})

	t.Run("нет Bearer-токена → 401", func(t *testing.T) {
		v, srv := newIntrospectVerifier(t, introspectJSON(http.StatusOK, activeAdmin))
		defer srv.Close()
		if got := runMiddleware(v, "", ""); got != http.StatusUnauthorized {
			t.Fatalf("ожидали 401, получили %d", got)
		}
	})

	t.Run("active, но typ=ID (подстановка id_token) → 401", func(t *testing.T) {
		v, srv := newIntrospectVerifier(t, introspectJSON(http.StatusOK,
			`{"active":true,"typ":"ID","sub":"u","realm_access":{"roles":["user"]}}`))
		defer srv.Close()
		if got := runMiddleware(v, "", "tok"); got != http.StatusUnauthorized {
			t.Fatalf("ожидали 401 (typ mismatch), получили %d", got)
		}
	})

	t.Run("active, но typ=Refresh → 401", func(t *testing.T) {
		v, srv := newIntrospectVerifier(t, introspectJSON(http.StatusOK,
			`{"active":true,"typ":"Refresh","sub":"u","realm_access":{"roles":["user"]}}`))
		defer srv.Close()
		if got := runMiddleware(v, "", "tok"); got != http.StatusUnauthorized {
			t.Fatalf("ожидали 401 (typ mismatch), получили %d", got)
		}
	})
}

func TestMiddlewareIntrospectNetworkErrorGives503(t *testing.T) {
	// Поднимаем фейковый introspection-эндпоинт и СРАЗУ закрываем его: последующее
	// обращение получит connection refused → сетевой сбой → ErrUpstreamUnavailable → 503.
	// Это отделяет "IdP недоступен" (503) от "токен невалиден" (401).
	srv := httptest.NewServer(introspectJSON(http.StatusOK, "{}"))
	url := srv.URL
	srv.Close()

	v, err := New(context.Background(), Config{
		Mode:         ModeIntrospect,
		Issuer:       url,
		Audience:     "backend",
		ClientID:     "backend",
		ClientSecret: "s3cr3t-demo",
	})
	if err != nil {
		t.Fatalf("New(introspect): %v", err)
	}
	if got := runMiddleware(v, "", "tok"); got != http.StatusServiceUnavailable {
		t.Fatalf("ожидали 503 при сетевом сбое, получили %d", got)
	}
}

// --- JWKS-режим: настоящий access-token verifier (подпись + iss/aud/exp/nbf) ---

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signRS подписывает JWT алгоритмом alg (RS256/RS512) ключом key. Header typ = "JWT"
// (как у Keycloak); тип токена (Bearer/ID) кладётся в CLAIM typ внутри claims.
func signRS(t *testing.T, key *rsa.PrivateKey, kid, alg string, claims map[string]any) string {
	t.Helper()
	var h crypto.Hash
	switch alg {
	case "RS256":
		h = crypto.SHA256
	case "RS512":
		h = crypto.SHA512
	default:
		t.Fatalf("unsupported alg %q", alg)
	}
	hdr, _ := json.Marshal(map[string]any{"alg": alg, "kid": kid, "typ": "JWT"})
	pl, _ := json.Marshal(claims)
	signingInput := b64(hdr) + "." + b64(pl)
	hasher := h.New()
	hasher.Write([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, h, hasher.Sum(nil))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signingInput + "." + b64(sig)
}

// signRS256 — обычный access-token: RS256.
func signRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	return signRS(t, key, kid, "RS256", claims)
}

// tokenAlgNone формирует неподписанный JWT с alg=none (атака downgrade).
func tokenAlgNone(claims map[string]any) string {
	hdr, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	pl, _ := json.Marshal(claims)
	return b64(hdr) + "." + b64(pl) + "."
}

// jwksJSON отдаёт JWKS из одного RSA-ключа.
func jwksJSON(kid string, pub *rsa.PublicKey) string {
	n := b64(pub.N.Bytes())
	e := b64(big.NewInt(int64(pub.E)).Bytes())
	return fmt.Sprintf(`{"keys":[{"kty":"RSA","use":"sig","alg":"RS256","kid":%q,"n":%q,"e":%q}]}`, kid, n, e)
}

// newJWKSVerifier поднимает фейковый OIDC (discovery + JWKS) и строит Verifier в ModeJWKS.
func newJWKSVerifier(t *testing.T, kid string, pub *rsa.PublicKey) (*Verifier, *httptest.Server) {
	t.Helper()
	var issuer string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":%q,"authorization_endpoint":%q,"token_endpoint":%q}`,
			issuer, issuer+"/certs", issuer+"/auth", issuer+"/token")
	})
	mux.HandleFunc("/certs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, jwksJSON(kid, pub))
	})
	srv := httptest.NewServer(mux)
	issuer = srv.URL // должен совпадать с Config.Issuer и полем issuer в discovery
	// JWKSRefreshInterval: -1 — выключаем фоновое обновление в тестах (детерминизм, без утечки горутин).
	v, err := New(context.Background(), Config{Mode: ModeJWKS, Issuer: srv.URL, Audience: "backend", JWKSRefreshInterval: -1})
	if err != nil {
		srv.Close()
		t.Fatalf("New(jwks): %v", err)
	}
	return v, srv
}

// baseClaims — валидный набор claims access-токена под issuer iss.
func baseClaims(iss string) map[string]any {
	return map[string]any{
		"iss":          iss,
		"sub":          "u1",
		"typ":          "Bearer", // Keycloak: тип токена в CLAIM (access = Bearer)
		"aud":          []string{"backend", "account"},
		"exp":          time.Now().Add(5 * time.Minute).Unix(),
		"nbf":          time.Now().Add(-1 * time.Minute).Unix(),
		"realm_access": map[string]any{"roles": []string{"user", "admin"}},
	}
}

func TestMiddlewareJWKSAccessToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	const kid = "test-kid"

	t.Run("валидный access-token + нужная роль → 200", func(t *testing.T) {
		v, srv := newJWKSVerifier(t, kid, &key.PublicKey)
		defer srv.Close()
		tok := signRS256(t, key, kid, baseClaims(srv.URL))
		if got := runMiddleware(v, "admin", tok); got != http.StatusOK {
			t.Fatalf("ожидали 200, получили %d", got)
		}
	})

	t.Run("валидный, но роли не хватает → 403", func(t *testing.T) {
		v, srv := newJWKSVerifier(t, kid, &key.PublicKey)
		defer srv.Close()
		c := baseClaims(srv.URL)
		c["realm_access"] = map[string]any{"roles": []string{"user"}}
		tok := signRS256(t, key, kid, c)
		if got := runMiddleware(v, "admin", tok); got != http.StatusForbidden {
			t.Fatalf("ожидали 403, получили %d", got)
		}
	})

	t.Run("истёкший (exp в прошлом) → 401", func(t *testing.T) {
		v, srv := newJWKSVerifier(t, kid, &key.PublicKey)
		defer srv.Close()
		c := baseClaims(srv.URL)
		c["exp"] = time.Now().Add(-5 * time.Minute).Unix()
		tok := signRS256(t, key, kid, c)
		if got := runMiddleware(v, "", tok); got != http.StatusUnauthorized {
			t.Fatalf("ожидали 401, получили %d", got)
		}
	})

	t.Run("ещё не действителен (nbf в будущем) → 401", func(t *testing.T) {
		v, srv := newJWKSVerifier(t, kid, &key.PublicKey)
		defer srv.Close()
		c := baseClaims(srv.URL)
		c["nbf"] = time.Now().Add(5 * time.Minute).Unix()
		tok := signRS256(t, key, kid, c)
		if got := runMiddleware(v, "", tok); got != http.StatusUnauthorized {
			t.Fatalf("ожидали 401, получили %d", got)
		}
	})

	t.Run("чужой aud → 401", func(t *testing.T) {
		v, srv := newJWKSVerifier(t, kid, &key.PublicKey)
		defer srv.Close()
		c := baseClaims(srv.URL)
		c["aud"] = []string{"frontend"}
		tok := signRS256(t, key, kid, c)
		if got := runMiddleware(v, "", tok); got != http.StatusUnauthorized {
			t.Fatalf("ожидали 401, получили %d", got)
		}
	})

	t.Run("чужой iss → 401", func(t *testing.T) {
		v, srv := newJWKSVerifier(t, kid, &key.PublicKey)
		defer srv.Close()
		tok := signRS256(t, key, kid, baseClaims("http://evil.example/realms/demo"))
		if got := runMiddleware(v, "", tok); got != http.StatusUnauthorized {
			t.Fatalf("ожидали 401, получили %d", got)
		}
	})

	t.Run("alg=none (downgrade) → 401", func(t *testing.T) {
		v, srv := newJWKSVerifier(t, kid, &key.PublicKey)
		defer srv.Close()
		tok := tokenAlgNone(baseClaims(srv.URL))
		if got := runMiddleware(v, "", tok); got != http.StatusUnauthorized {
			t.Fatalf("ожидали 401, получили %d", got)
		}
	})

	t.Run("подпись чужим ключом → 401", func(t *testing.T) {
		v, srv := newJWKSVerifier(t, kid, &key.PublicKey)
		defer srv.Close()
		other, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("keygen: %v", err)
		}
		tok := signRS256(t, other, kid, baseClaims(srv.URL)) // JWKS содержит key, подписано other
		if got := runMiddleware(v, "", tok); got != http.StatusUnauthorized {
			t.Fatalf("ожидали 401, получили %d", got)
		}
	})

	t.Run("подстановка id_token (typ=ID, валидная подпись) → 401", func(t *testing.T) {
		v, srv := newJWKSVerifier(t, kid, &key.PublicKey)
		defer srv.Close()
		// Корректно подписанный JWT с нужными claims, но CLAIM typ=ID — это id_token, не access.
		c := baseClaims(srv.URL)
		c["typ"] = "ID"
		tok := signRS256(t, key, kid, c)
		if got := runMiddleware(v, "", tok); got != http.StatusUnauthorized {
			t.Fatalf("ожидали 401 (typ mismatch), получили %d", got)
		}
	})

	t.Run("разрешённый, но не ожидаемый алгоритм (RS512 при ожидаемом RS256) → 401", func(t *testing.T) {
		v, srv := newJWKSVerifier(t, kid, &key.PublicKey)
		defer srv.Close()
		// Подпись валидным RS512, но verifier сконфигурирован строго на RS256.
		tok := signRS(t, key, kid, "RS512", baseClaims(srv.URL))
		if got := runMiddleware(v, "", tok); got != http.StatusUnauthorized {
			t.Fatalf("ожидали 401 (alg mismatch), получили %d", got)
		}
	})
}

// --- JWKS-ротация и коалессация конкурентных промахов по kid ---

// newMutableJWKSVerifier — как newMutableJWKSVerifierIv, но с ВЫКЛЮЧЕННЫМ фоновым
// обновлением (детерминизм для on-demand тестов).
func newMutableJWKSVerifier(t *testing.T, kid string, pub *rsa.PublicKey) (*Verifier, *httptest.Server, *atomic.Int32, func(kid string, pub *rsa.PublicKey), func(bool)) {
	return newMutableJWKSVerifierIv(t, kid, pub, -1)
}

// newMutableJWKSVerifierIv поднимает discovery+JWKS, где набор ключей можно менять (ротация)
// и включать HTTP-500 на /certs (сбой IdP). Считает обращения к /certs. iv — интервал
// фонового обновления (<=0 выключает). Возвращает verifier, сервер, счётчик обращений,
// функцию смены ключа и функцию включения ошибки.
func newMutableJWKSVerifierIv(t *testing.T, kid string, pub *rsa.PublicKey, iv time.Duration) (*Verifier, *httptest.Server, *atomic.Int32, func(kid string, pub *rsa.PublicKey), func(bool)) {
	t.Helper()
	var mu sync.Mutex
	current := jwksJSON(kid, pub)
	var hits atomic.Int32
	var fail atomic.Bool
	var issuer string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":%q,"authorization_endpoint":%q,"token_endpoint":%q}`,
			issuer, issuer+"/certs", issuer+"/auth", issuer+"/token")
	})
	mux.HandleFunc("/certs", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		mu.Lock()
		b := current
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, b)
	})
	srv := httptest.NewServer(mux)
	issuer = srv.URL
	v, err := New(context.Background(), Config{Mode: ModeJWKS, Issuer: srv.URL, Audience: "backend", JWKSRefreshInterval: iv})
	if err != nil {
		srv.Close()
		t.Fatalf("New(jwks): %v", err)
	}
	setKey := func(kid string, p *rsa.PublicKey) {
		mu.Lock()
		current = jwksJSON(kid, p)
		mu.Unlock()
	}
	setErr := func(f bool) { fail.Store(f) }
	return v, srv, &hits, setKey, setErr
}

func TestJWKSRotationImmediate(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	v, srv, _, setKey, _ := newMutableJWKSVerifier(t, "kid1", &key.PublicKey)
	defer srv.Close()

	// Исходный kid работает.
	if got := runMiddleware(v, "", signRS256(t, key, "kid1", baseClaims(srv.URL))); got != http.StatusOK {
		t.Fatalf("kid1: ожидали 200, получили %d", got)
	}
	// Realm ротировал ключ на kid2. Новый kid должен приниматься СРАЗУ (без окна отказа):
	// первый промах по неизвестному kid идёт в refresh немедленно.
	setKey("kid2", &key.PublicKey)
	if got := runMiddleware(v, "", signRS256(t, key, "kid2", baseClaims(srv.URL))); got != http.StatusOK {
		t.Fatalf("kid2 после ротации должен приниматься сразу, получили %d", got)
	}
}

func TestJWKSConcurrentMissCoalesced(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	v, srv, hits, setKey, _ := newMutableJWKSVerifier(t, "kid1", &key.PublicKey)
	defer srv.Close()

	// Прогреть kid1, затем ротировать на kid2 и обнулить счётчик — считаем только фазу
	// конкурентных промахов.
	runMiddleware(v, "", signRS256(t, key, "kid1", baseClaims(srv.URL)))
	setKey("kid2", &key.PublicKey)
	hits.Store(0)

	tok := signRS256(t, key, "kid2", baseClaims(srv.URL))
	const N = 20
	var wg sync.WaitGroup
	codes := make([]int, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); codes[i] = runMiddleware(v, "", tok) }(i)
	}
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("конкурентный запрос %d: ожидали 200, получили %d", i, c)
		}
	}
	// Коалессация: N одновременных промахов по kid2 должны вызвать НЕ N обращений к JWKS,
	// а фактически одно (допускаем небольшой люфт на гонку).
	if h := hits.Load(); h > 2 {
		t.Fatalf("коалессация не сработала: %d обращений к JWKS на %d конкурентных промахов (ожидали ≤2)", h, N)
	}
}

// TestJWKSCooldownNotPoisonedByUnknownKid — adversarial: токен с неизвестным (поддельным)
// kid не должен «отравлять» кэш так, чтобы легитимная ротация сразу после него отклонялась.
func TestJWKSCooldownNotPoisonedByUnknownKid(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	v, srv, _, setKey, _ := newMutableJWKSVerifier(t, "kid1", &key.PublicKey)
	defer srv.Close()

	// Злоумышленник шлёт токен с НЕИЗВЕСТНЫМ kid → refetch, ключа в JWKS нет → 401.
	attacker := signRS256(t, key, "attacker-kid", baseClaims(srv.URL))
	if got := runMiddleware(v, "", attacker); got != http.StatusUnauthorized {
		t.Fatalf("attacker kid: ожидали 401, получили %d", got)
	}
	// Keycloak СРАЗУ ротирует ключ на kid2.
	setKey("kid2", &key.PublicKey)
	// Легитимный токен с kid2 должен пройти НЕМЕДЛЕННО — промах по kid backoff не ставит,
	// поэтому cooldown не отравлен и новый JWKS запрашивается.
	if got := runMiddleware(v, "", signRS256(t, key, "kid2", baseClaims(srv.URL))); got != http.StatusOK {
		t.Fatalf("kid2 после ротации должен приниматься сразу (нет отравления cooldown), получили %d", got)
	}
}

// TestJWKSErrorBackoffAndRecovery — сбой JWKS-эндпоинта включает backoff (во время него
// повторные промахи НЕ долбят IdP), а после восстановления и истечения backoff kid проходит.
func TestJWKSErrorBackoffAndRecovery(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	v, srv, hits, setKey, setErr := newMutableJWKSVerifier(t, "kid1", &key.PublicKey)
	defer srv.Close()

	// JWKS-эндпоинт падает (HTTP 500); realm ротировал ключ, но забрать его нельзя.
	setErr(true)
	setKey("kid2", &key.PublicKey)
	hits.Store(0)
	tok2 := signRS256(t, key, "kid2", baseClaims(srv.URL))
	if got := runMiddleware(v, "", tok2); got != http.StatusUnauthorized {
		t.Fatalf("ошибка JWKS: ожидали 401, получили %d", got)
	}
	afterFirst := hits.Load()
	// DoS-защита: во время backoff повторный промах НЕ делает нового запроса к JWKS.
	if got := runMiddleware(v, "", tok2); got != http.StatusUnauthorized {
		t.Fatalf("во время backoff: ожидали 401, получили %d", got)
	}
	if hits.Load() != afterFirst {
		t.Fatalf("во время backoff не должно быть новых обращений к JWKS: было %d, стало %d", afterFirst, hits.Load())
	}
	// Эндпоинт восстановился; после истечения backoff (initial 1с) kid2 должен приняться.
	setErr(false)
	time.Sleep(jwksBackoffInitial + 300*time.Millisecond)
	if got := runMiddleware(v, "", tok2); got != http.StatusOK {
		t.Fatalf("после восстановления и истечения backoff: ожидали 200, получили %d", got)
	}
}

// TestJWKSRandomKidAmplificationBounded — adversarial: поток JWT со СЛУЧАЙНЫМИ kid не должен
// вызывать по одному запросу к JWKS на каждый (амплификация/DoS на IdP). Здесь 100 kid
// отправляются быстро (elapsed≈0), поэтому число refetch ≈ burst; точная граница
// burst + refillRate·elapsed проверяется детерминированно в TestTokenBucket.
func TestJWKSRandomKidAmplificationBounded(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	v, srv, hits, _, _ := newMutableJWKSVerifier(t, "kid1", &key.PublicKey)
	defer srv.Close()

	hits.Store(0) // считаем только фазу мусорных kid (initial fetch из New не учитываем)
	const N = 100
	for i := 0; i < N; i++ {
		tok := signRS256(t, key, fmt.Sprintf("attacker-%d", i), baseClaims(srv.URL))
		if got := runMiddleware(v, "", tok); got != http.StatusUnauthorized {
			t.Fatalf("мусорный kid #%d: ожидали 401, получили %d", i, got)
		}
	}
	// Число обращений к JWKS должно быть ограничено ~burst, а не N=100.
	if h := hits.Load(); h > int32(jwksRefetchBurst)+2 {
		t.Fatalf("амплификация: %d обращений к JWKS на %d мусорных kid (ожидали ≲%d)", h, N, int32(jwksRefetchBurst))
	}
}

// TestJWKSBackgroundRefreshPicksUpRotation — фоновое проактивное обновление подхватывает
// ротацию ключа даже БЕЗ on-demand запроса (документированное окно принятия нового ключа).
func TestJWKSBackgroundRefreshPicksUpRotation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	v, srv, _, setKey, _ := newMutableJWKSVerifierIv(t, "kid1", &key.PublicKey, 40*time.Millisecond)
	defer srv.Close()
	defer v.Close() // остановить фоновый ticker (LIFO: до srv.Close)

	// Ротация БЕЗ on-demand запроса: фоновый refresh должен занести kid2 в кэш сам.
	setKey("kid2", &key.PublicKey)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v.lookupKey("kid2") != nil {
			return // фон подхватил kid2
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("фоновый refresh не подхватил kid2 в кэш за отведённое время")
}

// TestTokenBucket — детерминированная проверка лимитера с УПРАВЛЯЕМЫМИ timestamps
// (без реального времени): burst, пополнение refillRate·elapsed, потолок capacity.
func TestTokenBucket(t *testing.T) {
	t0 := time.Unix(1000, 0)
	b := newTokenBucket(3, 1, t0) // burst=3, 1 токен/сек

	// Burst: ровно capacity=3 подряд в t0, четвёртый — отказ.
	for i := 0; i < 3; i++ {
		if !b.allow(t0) {
			t.Fatalf("burst: запрос %d должен пройти", i)
		}
	}
	if b.allow(t0) {
		t.Fatal("после исчерпания burst в t0 ожидали отказ")
	}
	// Через 1с пополняется ровно 1 токен: один запрос проходит, второй — нет.
	t1 := t0.Add(time.Second)
	if !b.allow(t1) {
		t.Fatal("через 1с должен появиться 1 токен")
	}
	if b.allow(t1) {
		t.Fatal("через 1с — только 1 токен, второй отказ")
	}
	// Через 10с пополнение упирается в capacity=3, не больше.
	t2 := t1.Add(10 * time.Second)
	got := 0
	for i := 0; i < 10; i++ {
		if b.allow(t2) {
			got++
		}
	}
	if got != 3 {
		t.Fatalf("пополнение ограничено capacity=3, прошло %d", got)
	}
}
