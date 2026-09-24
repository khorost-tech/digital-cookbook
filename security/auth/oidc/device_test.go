package oidc_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"khorost.tech/cookbook/auth/login-methods/mockidp"
	"khorost.tech/cookbook/auth/oidc"
)

// approveDeviceErr выполняет "подтверждение с телефона" — POST
// /device/approve с user_code, показанным клиенту device-flow. Возвращает
// ошибку вместо падения теста напрямую: RunDeviceFlow (oidc/device.go) вызывает
// approve-колбэк из ОТДЕЛЬНОЙ горутины (`go approve(...)`), а t.Fatalf/FailNow
// разрешён только из горутины самого теста — вызов из чужой горутины при
// сбое не завершает тест, а вешает его до общего таймаута пакета. Поэтому
// approve-колбэки, запускаемые RunDeviceFlow, обязаны сообщать ошибку через
// канал и дать основной горутине теста самой решить, когда звать t.Fatalf.
func approveDeviceErr(srvURL, userCode string) error {
	resp, err := http.PostForm(srvURL+"/device/approve", url.Values{"user_code": {userCode}}) //nolint:noctx // тестовый helper
	if err != nil {
		return fmt.Errorf("approveDevice: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("approveDevice: want 200, got %d", resp.StatusCode)
	}
	return nil
}

// approveDevice — синхронная обёртка над approveDeviceErr для мест, где
// колбэк точно выполняется в главной горутине теста (не под `go approve(...)`
// из RunDeviceFlow) — там t.Fatalf безопасен напрямую.
func approveDevice(t *testing.T, srvURL, userCode string) {
	t.Helper()
	if err := approveDeviceErr(srvURL, userCode); err != nil {
		t.Fatalf("%v", err)
	}
}

// TestDeviceFlowFull прогоняет полный живой device authorization flow
// (RFC 8628): клиент запрашивает device_code/user_code, "показывает" код
// пользователю (approve вызывается из горутины — имитация подтверждения с
// другого устройства), поллит /token до успеха.
//
// approve-колбэк выполняется RunDeviceFlow в отдельной горутине (`go
// approve(...)`), поэтому он НЕ вызывает t.Fatalf напрямую (FailNow из
// не-тестовой горутины запрещён и в случае сбоя тест не упал бы быстро, а
// завис до таймаута пакета) — вместо этого шлёт результат в буферизованный
// канал approveErrCh, который основная горутина теста проверяет после
// возврата из RunDeviceFlow.
func TestDeviceFlowFull(t *testing.T) {
	srv := httptest.NewServer(mockidp.New())
	defer srv.Close()

	approveErrCh := make(chan error, 1)
	tok, err := oidc.RunDeviceFlow(srv.URL, func(userCode string) {
		approveErrCh <- approveDeviceErr(srv.URL, userCode)
	})
	if err != nil || tok == "" {
		t.Fatalf("device flow failed: tok=%q err=%v", tok, err)
	}
	// RunDeviceFlow вернулось с токеном только после того, как сервер отметил
	// device approved, а approve-горутина отправляет в канал сразу вслед за
	// этим — блокирующий select с коротким таймаутом (не non-blocking
	// default) ждёт это сообщение без гонки, но всё равно падает быстро, если
	// канал почему-то пуст.
	select {
	case approveErr := <-approveErrCh:
		if approveErr != nil {
			t.Fatalf("approve callback failed: %v", approveErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approve callback never completed (channel empty 5s after RunDeviceFlow returned)")
	}
}

// TestDeviceFlowPendingBeforeApprove бьёт по сырому HTTP-протоколу device
// flow напрямую (без клиента oidc.RunDeviceFlow): убеждается, что poll ДО
// approve возвращает authorization_pending (не токен), а poll ПОСЛЕ approve —
// access_token. Проверяет сам протокол mockidp, а не клиентскую петлю.
func TestDeviceFlowPendingBeforeApprove(t *testing.T) {
	srv := httptest.NewServer(mockidp.New())
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
		Interval   int    `json:"interval"`
	}
	if err := json.NewDecoder(authResp.Body).Decode(&auth); err != nil {
		t.Fatalf("decode device_authorization: %v", err)
	}
	if auth.DeviceCode == "" || auth.UserCode == "" {
		t.Fatal("device_authorization: empty device_code/user_code")
	}

	pollForm := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {auth.DeviceCode},
	}

	// poll ДО approve -> authorization_pending, HTTP 400, без токена.
	pendingResp, err := http.PostForm(srv.URL+"/token", pollForm) //nolint:noctx // тест
	if err != nil {
		t.Fatalf("token poll (pending): %v", err)
	}
	defer func() { _ = pendingResp.Body.Close() }()
	if pendingResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("token poll (pending): want 400, got %d", pendingResp.StatusCode)
	}
	var pendingBody struct {
		Error       string `json:"error"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(pendingResp.Body).Decode(&pendingBody); err != nil {
		t.Fatalf("decode pending response: %v", err)
	}
	if pendingBody.Error != "authorization_pending" || pendingBody.AccessToken != "" {
		t.Fatalf("token poll (pending): want authorization_pending, got error=%q access_token=%q",
			pendingBody.Error, pendingBody.AccessToken)
	}

	approveDevice(t, srv.URL, auth.UserCode)

	// poll ПОСЛЕ approve -> access_token.
	okResp, err := http.PostForm(srv.URL+"/token", pollForm) //nolint:noctx // тест
	if err != nil {
		t.Fatalf("token poll (approved): %v", err)
	}
	defer func() { _ = okResp.Body.Close() }()
	if okResp.StatusCode != http.StatusOK {
		t.Fatalf("token poll (approved): want 200, got %d", okResp.StatusCode)
	}
	var okBody struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
	}
	if err := json.NewDecoder(okResp.Body).Decode(&okBody); err != nil {
		t.Fatalf("decode approved response: %v", err)
	}
	if okBody.AccessToken == "" || okBody.IDToken == "" {
		t.Fatal("token poll (approved): expected access_token and id_token")
	}
}

// TestDeviceFlowUnknownDeviceCodeInvalidGrant — poll с неизвестным
// device_code должен возвращать invalid_grant, а не authorization_pending
// (RFC 8628 §3.5): клиент не должен путать "ещё не подтверждено" с
// "device_code никогда не существовал".
func TestDeviceFlowUnknownDeviceCodeInvalidGrant(t *testing.T) {
	srv := httptest.NewServer(mockidp.New())
	defer srv.Close()

	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {"never-issued-device-code"},
	}
	resp, err := http.PostForm(srv.URL+"/token", form) //nolint:noctx // тест
	if err != nil {
		t.Fatalf("token poll: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "invalid_grant" {
		t.Fatalf("want invalid_grant, got %q", body.Error)
	}
}

// TestDeviceFlowClientTimesOutWithoutApprove проверяет клиентскую петлю
// RunDeviceFlow изолированно от mockidp: поднимает фейковый сервер, который
// всегда отвечает authorization_pending с коротким expires_in — RunDeviceFlow
// обязан вернуть ошибку по истечении expires_in, а не поллить вечно.
// Использует отдельный лёгкий сервер (не mockidp), чтобы не зависеть от
// боевого device_code TTL (600с) и оставаться быстрым и детерминированным.
func TestDeviceFlowClientTimesOutWithoutApprove(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/device_authorization", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "fake-device-code",
			"user_code":   "FAKE-CODE",
			"expires_in":  1,
			"interval":    1,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	done := make(chan struct{})
	var tok string
	var runErr error
	go func() {
		tok, runErr = oidc.RunDeviceFlow(srv.URL, func(string) {
			// намеренно НЕ подтверждаем — expires_in=1с должен истечь.
		})
		close(done)
	}()

	select {
	case <-done:
		if runErr == nil {
			t.Fatalf("expected timeout error, got tok=%q", tok)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunDeviceFlow did not return within 10s — looks like an infinite poll loop")
	}
}
