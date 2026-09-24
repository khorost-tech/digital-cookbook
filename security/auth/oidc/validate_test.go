package oidc_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"khorost.tech/cookbook/auth/internal/token"
	"khorost.tech/cookbook/auth/login-methods/mockidp"
	"khorost.tech/cookbook/auth/oidc"
)

const testAudience = "oidc-test-client"

func issuerOf(srv *httptest.Server) string {
	return srv.URL
}

func audienceOf() string {
	return testAudience
}

// obtainIDToken проходит code+PKCE-флоу напрямую против mockidp httptest.Server
// (без login-methods/oauth.go — тут интересен только сам id_token) и
// возвращает "сырой" id_token.
func obtainIDToken(t *testing.T, srvURL string) string {
	t.Helper()

	verifier, err := token.OpaqueHex(32)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	authorizeURL, err := url.Parse(srvURL + "/authorize")
	if err != nil {
		t.Fatal(err)
	}
	q := authorizeURL.Query()
	q.Set("client_id", testAudience)
	q.Set("redirect_uri", "http://app.local/callback")
	q.Set("state", "s1")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	authorizeURL.RawQuery = q.Encode()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(authorizeURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: want 302, got %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatal("authorize: no code in redirect")
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {testAudience},
	}
	tokResp, err := http.Post(srvURL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tokResp.Body.Close() }()
	if tokResp.StatusCode != http.StatusOK {
		t.Fatalf("token: want 200, got %d", tokResp.StatusCode)
	}

	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(tokResp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.IDToken == "" {
		t.Fatal("token: empty id_token")
	}
	return body.IDToken
}

// forgeHS256SamePayload берёт claims настоящего RS256 id_token и подписывает
// их заново алгоритмом HS256 произвольным (атакующим выбранным) секретом —
// моделирует classic alg-confusion атаку (RS256 -> HS256 с публичным ключом
// или произвольным секретом вместо него).
func forgeHS256SamePayload(t *testing.T, raw string) string {
	t.Helper()

	parser := jwt.NewParser()
	claims := jwt.MapClaims{}
	if _, _, err := parser.ParseUnverified(raw, claims); err != nil {
		t.Fatalf("forge: parse original token: %v", err)
	}

	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := forged.SignedString([]byte("attacker-controlled-secret"))
	if err != nil {
		t.Fatalf("forge: sign: %v", err)
	}
	return signed
}

func TestValidateIDTokenAcceptsValidRejectsForged(t *testing.T) {
	srv := httptest.NewServer(mockidp.New())
	defer srv.Close()

	raw := obtainIDToken(t, srv.URL)

	claims, err := oidc.ValidateIDToken(srv.URL+"/.well-known/jwks.json", issuerOf(srv), audienceOf(), raw)
	if err != nil {
		t.Fatalf("valid id_token rejected: %v", err)
	}
	if claims["sub"] == nil {
		t.Fatal("no sub claim")
	}

	forged := forgeHS256SamePayload(t, raw)
	if _, err := oidc.ValidateIDToken(srv.URL+"/.well-known/jwks.json", issuerOf(srv), audienceOf(), forged); err == nil {
		t.Fatal("forged HS256 token must be rejected (alg confusion)")
	}
}

func TestValidateIDTokenRejectsWrongIssuerAudience(t *testing.T) {
	srv := httptest.NewServer(mockidp.New())
	defer srv.Close()

	raw := obtainIDToken(t, srv.URL)

	if _, err := oidc.ValidateIDToken(srv.URL+"/.well-known/jwks.json", "https://not-the-issuer.example", audienceOf(), raw); err == nil {
		t.Fatal("wrong issuer must be rejected")
	}
	if _, err := oidc.ValidateIDToken(srv.URL+"/.well-known/jwks.json", issuerOf(srv), "not-the-audience", raw); err == nil {
		t.Fatal("wrong audience must be rejected")
	}
}
