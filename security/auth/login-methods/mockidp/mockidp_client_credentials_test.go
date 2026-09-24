package mockidp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func postForm(t *testing.T, target string, form map[string]string, v any) {
	t.Helper()
	vals := url.Values{}
	for k, val := range form {
		vals.Set(k, val)
	}
	resp, err := http.Post(target, "application/x-www-form-urlencoded", strings.NewReader(vals.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

// clientCredentialsToken выполняет grant_type=client_credentials с фиксированными
// демо-креды (demo-client/demo-secret) и возвращает access_token.
func clientCredentialsToken(t *testing.T, srvURL string) string {
	t.Helper()
	var resp map[string]any
	postForm(t, srvURL+"/token", map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     demoClientCredentialsID,
		"client_secret": demoClientCredentialsSecret,
	}, &resp)
	tok, _ := resp["access_token"].(string)
	return tok
}

// introspectAsClient добавляет к телу form демо client credentials, требуемые
// /introspect с FIX N-2 (RFC 7662 §2.1: introspection endpoint аутентифицирует
// вызывающего).
func introspectAsClient(t *testing.T, srvURL string, form map[string]string, v any) {
	t.Helper()
	full := map[string]string{
		"client_id":     demoClientCredentialsID,
		"client_secret": demoClientCredentialsSecret,
	}
	for k, val := range form {
		full[k] = val
	}
	postForm(t, srvURL+"/introspect", full, v)
}

func TestClientCredentialsAndIntrospect(t *testing.T) {
	srv := httptest.NewServer(New())
	defer srv.Close()

	tok := clientCredentialsToken(t, srv.URL)
	if tok == "" {
		t.Fatal("no access token")
	}

	var r map[string]any
	introspectAsClient(t, srv.URL, map[string]string{"token": tok}, &r)
	if r["active"] != true {
		t.Fatalf("valid token must be active: %v", r)
	}
	if r["scope"] == nil {
		t.Fatalf("active introspection response should include scope: %v", r)
	}

	introspectAsClient(t, srv.URL, map[string]string{"token": "garbage"}, &r)
	if r["active"] != false {
		t.Fatalf("garbage must be inactive: %v", r)
	}
}

// TestIntrospectRequiresClientAuth проверяет FIX N-2: /introspect без
// валидных client credentials обязан отвечать 401, не раскрывая active/claims
// невовлечённому вызывающему — ни без креденшелов вовсе, ни с неверным
// secret'ом.
func TestIntrospectRequiresClientAuth(t *testing.T) {
	srv := httptest.NewServer(New())
	defer srv.Close()

	tok := clientCredentialsToken(t, srv.URL)
	if tok == "" {
		t.Fatal("no access token")
	}

	resp, err := http.PostForm(srv.URL+"/introspect", url.Values{"token": {tok}}) //nolint:noctx // тест
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("introspect without client credentials: want 401, got %d", resp.StatusCode)
	}

	resp2, err := http.PostForm(srv.URL+"/introspect", url.Values{ //nolint:noctx // тест
		"token":         {tok},
		"client_id":     {demoClientCredentialsID},
		"client_secret": {"wrong-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("introspect with wrong client secret: want 401, got %d", resp2.StatusCode)
	}
}

func TestClientCredentialsRejectsBadSecret(t *testing.T) {
	srv := httptest.NewServer(New())
	defer srv.Close()

	vals := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {demoClientCredentialsID},
		"client_secret": {"wrong-secret"},
	}
	resp, err := http.Post(srv.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(vals.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("bad client secret must not yield 200 OK / an access token")
	}
}
