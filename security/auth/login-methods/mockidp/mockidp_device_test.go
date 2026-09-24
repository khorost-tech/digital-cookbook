package mockidp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestDeviceCodeSingleUse проверяет одноразовость device_code (RFC 8628):
// после approve первый POST /token с device_code обязан выдать токен, а
// ВТОРОЙ poll тем же (уже потреблённым) device_code — invalid_grant, а не
// второй токен. До фикса tokenDeviceCode читал device и удалял его из
// h.devices под РАЗНЫМИ критическими секциями (TOCTOU race) — здесь
// проверяется наблюдаемое следствие бага последовательно (без гонки):
// device_code не должен обслуживать более одного успешного обмена.
func TestDeviceCodeSingleUse(t *testing.T) {
	srv := httptest.NewServer(New())
	defer srv.Close()

	authResp, err := http.PostForm(srv.URL+"/device_authorization", url.Values{}) //nolint:noctx // тест
	if err != nil {
		t.Fatalf("device_authorization: %v", err)
	}
	defer func() { _ = authResp.Body.Close() }()
	if authResp.StatusCode != http.StatusOK {
		t.Fatalf("device_authorization: want 200, got %d", authResp.StatusCode)
	}

	var auth struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	if err := json.NewDecoder(authResp.Body).Decode(&auth); err != nil {
		t.Fatalf("decode device_authorization: %v", err)
	}
	if auth.DeviceCode == "" || auth.UserCode == "" {
		t.Fatal("device_authorization: empty device_code/user_code")
	}

	approveResp, err := http.PostForm(srv.URL+"/device/approve", url.Values{"user_code": {auth.UserCode}}) //nolint:noctx // тест
	if err != nil {
		t.Fatalf("device/approve: %v", err)
	}
	defer func() { _ = approveResp.Body.Close() }()
	if approveResp.StatusCode != http.StatusOK {
		t.Fatalf("device/approve: want 200, got %d", approveResp.StatusCode)
	}

	pollForm := url.Values{
		"grant_type":  {deviceGrantType},
		"device_code": {auth.DeviceCode},
	}

	// первый poll после approve -> access_token.
	firstResp, err := http.PostForm(srv.URL+"/token", pollForm) //nolint:noctx // тест
	if err != nil {
		t.Fatalf("token poll (first): %v", err)
	}
	defer func() { _ = firstResp.Body.Close() }()
	if firstResp.StatusCode != http.StatusOK {
		t.Fatalf("token poll (first): want 200, got %d", firstResp.StatusCode)
	}
	var firstBody struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
	}
	if err := json.NewDecoder(firstResp.Body).Decode(&firstBody); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if firstBody.AccessToken == "" || firstBody.IDToken == "" {
		t.Fatal("token poll (first): expected access_token and id_token")
	}

	// второй poll тем же device_code -> invalid_grant, НЕ второй токен.
	secondResp, err := http.PostForm(srv.URL+"/token", pollForm) //nolint:noctx // тест
	if err != nil {
		t.Fatalf("token poll (second): %v", err)
	}
	defer func() { _ = secondResp.Body.Close() }()
	if secondResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("token poll (second): want 400, got %d", secondResp.StatusCode)
	}
	var secondBody struct {
		Error       string `json:"error"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(secondResp.Body).Decode(&secondBody); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if secondBody.Error != "invalid_grant" {
		t.Fatalf("token poll (second): want invalid_grant, got error=%q", secondBody.Error)
	}
	if secondBody.AccessToken != "" {
		t.Fatalf("token poll (second): device_code reused, issued a second access_token %q", secondBody.AccessToken)
	}
}

// TestDeviceVerificationPageResolves проверяет FIX N-4: verification_uri,
// отдаваемый /device_authorization, обязан резолвиться (не 404) — раньше
// /device не имел зарегистрированного хендлера вовсе.
func TestDeviceVerificationPageResolves(t *testing.T) {
	srv := httptest.NewServer(New())
	defer srv.Close()

	authResp, err := http.PostForm(srv.URL+"/device_authorization", url.Values{}) //nolint:noctx // тест
	if err != nil {
		t.Fatalf("device_authorization: %v", err)
	}
	defer func() { _ = authResp.Body.Close() }()

	var auth struct {
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
	}
	if err := json.NewDecoder(authResp.Body).Decode(&auth); err != nil {
		t.Fatalf("decode device_authorization: %v", err)
	}

	resp, err := http.Get(auth.VerificationURI) //nolint:noctx // тест
	if err != nil {
		t.Fatalf("GET verification_uri: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: want 200, got %d", auth.VerificationURI, resp.StatusCode)
	}

	respComplete, err := http.Get(auth.VerificationURIComplete) //nolint:noctx // тест
	if err != nil {
		t.Fatalf("GET verification_uri_complete: %v", err)
	}
	defer func() { _ = respComplete.Body.Close() }()
	if respComplete.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: want 200, got %d", auth.VerificationURIComplete, respComplete.StatusCode)
	}
}
