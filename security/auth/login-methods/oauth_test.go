package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"khorost.tech/cookbook/auth/login-methods/mockidp"
)

func newOAuthTestRDB(t *testing.T) *redis.Client {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

// oauthTestRouter wires startOAuth/callbackOAuth behind chi so that
// chi.URLParam(r, "provider") works exactly as in production.
func oauthTestRouter(oauth *oauthHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/oauth/{provider}/start", oauth.startOAuth)
	r.Get("/oauth/{provider}/callback", oauth.callbackOAuth)
	return r
}

// followToAuthorize performs the start-request against router and returns the
// Location URL it redirects to (the mockidp /authorize URL with PKCE params).
func followToAuthorize(t *testing.T, router http.Handler, provider string) *url.URL {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/oauth/"+provider+"/start", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("start: want 302, got %d body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc == "" {
		t.Fatal("start: empty Location header")
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("start: bad Location %q: %v", loc, err)
	}
	return u
}

// runAuthorize hits the real mockidp httptest.Server at authorizeURL (without
// following the redirect, since redirect_uri points at a non-existent host in
// tests) and returns the callback URL (code+state) mockidp redirected to.
func runAuthorize(t *testing.T, client *http.Client, authorizeURL *url.URL) *url.URL {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, authorizeURL.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("authorize request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: want 302, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	cbURL, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("authorize: bad Location %q: %v", loc, err)
	}
	return cbURL
}

func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func TestOAuthHappyPath(t *testing.T) {
	idp := httptest.NewServer(mockidp.New())
	defer idp.Close()

	rdb := newOAuthTestRDB(t)
	oauth := newOAuthHandler(rdb, []byte("secret"), idp.URL, "test-client", "http://app.local", false)
	router := oauthTestRouter(oauth)

	authorizeURL := followToAuthorize(t, router, "oidc-provider")
	if got := authorizeURL.Query().Get("code_challenge_method"); got != "S256" {
		t.Fatalf("authorize URL missing code_challenge_method=S256: %s", authorizeURL)
	}

	callbackURL := runAuthorize(t, noRedirectClient(), authorizeURL)
	if callbackURL.Query().Get("code") == "" || callbackURL.Query().Get("state") == "" {
		t.Fatalf("callback URL missing code/state: %s", callbackURL)
	}

	req := httptest.NewRequest(http.MethodGet, callbackURL.RequestURI(), nil)
	rec := httptest.NewRecorder()
	// callbackURL.RequestURI() only carries the path+query of the fake
	// redirect_uri (/oauth/oidc-provider/callback?...), so route it through
	// our own router the same way a real browser hitting our callback would.
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("callback: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("empty access_token in response")
	}

	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == accessCookieName && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("app_at cookie not set: %v", rec.Result().Cookies())
	}
}

func TestOAuthBadState(t *testing.T) {
	idp := httptest.NewServer(mockidp.New())
	defer idp.Close()

	rdb := newOAuthTestRDB(t)
	oauth := newOAuthHandler(rdb, []byte("secret"), idp.URL, "test-client", "http://app.local", false)
	router := oauthTestRouter(oauth)

	req := httptest.NewRequest(http.MethodGet, "/oauth/oidc-provider/callback?code=whatever&state=unknown-state", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("callback with bad state: want 400, got %d body=%s", rec.Code, rec.Body.String())
	}

	for _, c := range rec.Result().Cookies() {
		if c.Name == accessCookieName && c.Value != "" {
			t.Fatalf("session must not be created for bad state, got cookie %v", c)
		}
	}
}

func TestOAuthPKCEMismatch(t *testing.T) {
	idp := httptest.NewServer(mockidp.New())
	defer idp.Close()
	client := noRedirectClient()

	authorizeURL, err := url.Parse(idp.URL + "/authorize")
	if err != nil {
		t.Fatal(err)
	}
	q := authorizeURL.Query()
	q.Set("client_id", "test-client")
	q.Set("redirect_uri", "http://app.local/oauth/oidc-provider/callback")
	q.Set("state", "s1")
	q.Set("code_challenge", "some-challenge-that-wont-match-verifier")
	q.Set("code_challenge_method", "S256")
	authorizeURL.RawQuery = q.Encode()

	cbURL := runAuthorize(t, client, authorizeURL)
	code := cbURL.Query().Get("code")
	if code == "" {
		t.Fatal("authorize did not return a code")
	}

	form := url.Values{
		"code":          {code},
		"code_verifier": {"wrong-verifier-does-not-hash-to-challenge"},
	}
	resp, err := http.Post(idp.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		t.Fatal("token endpoint must reject mismatched PKCE verifier")
	}
}
