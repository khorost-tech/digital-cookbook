package mockidp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryAndJWKS(t *testing.T) {
	srv := httptest.NewServer(New())
	defer srv.Close()
	var disc map[string]any
	getJSON(t, srv.URL+"/.well-known/openid-configuration", &disc)
	for _, k := range []string{"issuer", "jwks_uri", "token_endpoint", "authorization_endpoint"} {
		if disc[k] == nil {
			t.Fatalf("discovery missing %q: %v", k, disc)
		}
	}
	if disc["issuer"] != srv.URL {
		t.Fatalf("issuer must match server URL: got %v want %v", disc["issuer"], srv.URL)
	}
	var jwks struct{ Keys []map[string]any }
	getJSON(t, srv.URL+"/.well-known/jwks.json", &jwks)
	if len(jwks.Keys) == 0 || jwks.Keys[0]["kty"] != "RSA" {
		t.Fatalf("jwks must expose RSA key: %+v", jwks)
	}
}
