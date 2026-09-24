// Package oidc — device authorization flow (RFC 8628): вход на устройствах
// без (удобного) браузера или клавиатуры — TV, CLI, IoT. Устройство
// запрашивает device_code+user_code, показывает user_code и
// verification_uri пользователю, а сам поллит /token, пока пользователь не
// подтвердит вход с другого устройства (телефона/ноутбука).
package oidc

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// deviceGrantType — grant_type для поллинга /token в device flow
// (RFC 8628 §3.4).
const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// deviceClientID — demo client_id, отправляемый в device_authorization.
// mockidp не проверяет client_id для device flow (см. login-methods/mockidp) —
// значение здесь чисто демонстрационное, для полноты протокола.
const deviceClientID = "oidc-device-demo-client"

// defaultDeviceExpiresIn/defaultDeviceInterval — запасные значения, если
// провайдер вернул expires_in/interval <= 0 (не должно происходить с
// mockidp, но клиент не обязан доверять серверу вслепую).
const (
	defaultDeviceExpiresIn = 600 * time.Second
	defaultDeviceInterval  = time.Second
)

// ErrDeviceFlowTimedOut возвращается RunDeviceFlow, если device_code/user_code
// истекли (expires_in) раньше, чем пользователь подтвердил вход.
var ErrDeviceFlowTimedOut = errors.New("oidc: device flow timed out before approval")

// errAuthorizationPending/errSlowDown — внутренние сигналы цикла поллинга,
// наружу из RunDeviceFlow не просачиваются (обрабатываются в цикле).
var (
	errAuthorizationPending = errors.New("oidc: authorization_pending")
	errSlowDown             = errors.New("oidc: slow_down")
)

// deviceAuthorizationResponse — тело ответа POST /device_authorization
// (RFC 8628 §3.2).
type deviceAuthorizationResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// deviceTokenResponse — тело ответа POST /token (device_code grant), и
// успешное (access_token/id_token), и ошибочное (error).
type deviceTokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	Error       string `json:"error"`
}

// RunDeviceFlow выполняет полный device authorization flow (RFC 8628) против
// OIDC-провайдера по адресу base: запрашивает device_code/user_code через
// POST base+"/device_authorization", вызывает approve(userCode) — в реальном
// приложении это точка, где user_code и verification_uri показываются
// пользователю (на экране TV/в терминале); approve запускается в отдельной
// горутине сразу после получения user_code, чтобы не блокировать цикл
// поллинга (в demo approve сам подтверждает вход "с телефона" синхронно
// внутри колбэка) — затем поллит POST base+"/token" с интервалом,
// возвращаемым провайдером (respecting slow_down), пока пользователь не
// подтвердит вход (success) или пока не истечёт expires_in (ErrDeviceFlowTimedOut).
func RunDeviceFlow(base string, approve func(userCode string)) (accessToken string, err error) {
	auth, err := startDeviceAuthorization(base)
	if err != nil {
		return "", err
	}

	interval := time.Duration(auth.Interval) * time.Second
	if interval <= 0 {
		interval = defaultDeviceInterval
	}
	expiresIn := time.Duration(auth.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = defaultDeviceExpiresIn
	}
	deadline := time.Now().Add(expiresIn)

	go approve(auth.UserCode)

	for {
		if time.Now().After(deadline) {
			return "", ErrDeviceFlowTimedOut
		}
		time.Sleep(interval)

		tok, pollErr := pollDeviceToken(base, auth.DeviceCode)
		switch {
		case pollErr == nil:
			return tok, nil
		case errors.Is(pollErr, errAuthorizationPending):
			continue
		case errors.Is(pollErr, errSlowDown):
			interval += time.Second
			continue
		default:
			return "", pollErr
		}
	}
}

// startDeviceAuthorization — POST base+"/device_authorization".
func startDeviceAuthorization(base string) (deviceAuthorizationResponse, error) {
	var out deviceAuthorizationResponse

	form := url.Values{"client_id": {deviceClientID}}
	resp, err := http.PostForm(base+"/device_authorization", form) //nolint:noctx,gosec // demo-клиент стенда, base не пользовательский ввод HTTP-обработчика
	if err != nil {
		return out, fmt.Errorf("oidc: device_authorization request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("oidc: device_authorization returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("oidc: decode device_authorization response: %w", err)
	}
	if out.DeviceCode == "" || out.UserCode == "" {
		return out, errors.New("oidc: device_authorization response missing device_code/user_code")
	}
	return out, nil
}

// pollDeviceToken — один POST base+"/token" grant_type=device_code. Успех
// возвращает access_token; authorization_pending/slow_down возвращаются как
// сигнальные ошибки (errAuthorizationPending/errSlowDown) для цикла
// RunDeviceFlow; любая другая ошибка провайдера (expired_token, invalid_grant
// и т.п.) — как обычная error, останавливающая поллинг.
func pollDeviceToken(base, deviceCode string) (string, error) {
	form := url.Values{
		"grant_type":  {deviceGrantType},
		"device_code": {deviceCode},
	}
	resp, err := http.PostForm(base+"/token", form) //nolint:noctx,gosec // demo-клиент стенда
	if err != nil {
		return "", fmt.Errorf("oidc: token poll request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body deviceTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("oidc: decode token poll response: %w", err)
	}

	if resp.StatusCode == http.StatusOK && body.AccessToken != "" {
		return body.AccessToken, nil
	}

	switch body.Error {
	case "authorization_pending":
		return "", errAuthorizationPending
	case "slow_down":
		return "", errSlowDown
	case "":
		return "", fmt.Errorf("oidc: token poll: unexpected response, http %d", resp.StatusCode)
	default:
		return "", fmt.Errorf("oidc: token poll error: %s", body.Error)
	}
}
