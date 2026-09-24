package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/redis/go-redis/v9"
)

const (
	testRPID   = "localhost"
	testOrigin = "http://localhost:8087"
)

// Флаги байта authenticatorData (см. §6.1 спецификации WebAuthn).
const (
	flagUserPresent          = 0x01
	flagUserVerified         = 0x04
	flagAttestedCredentialAT = 0x40
)

func newRDB(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func newTestWebAuthn(t *testing.T) *webauthn.WebAuthn {
	t.Helper()
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          testRPID,
		RPDisplayName: "Test RP",
		RPOrigins:     []string{testOrigin},
	})
	if err != nil {
		t.Fatalf("webauthn.New: %v", err)
	}
	return wa
}

// virtualAuthenticator — честный аутентификатор ES256 (P-256) для тестов:
// сам генерирует ключевую пару, сам собирает authenticatorData/attestationObject
// для регистрации и сам подписывает assertion при логине. Backend верифицирует
// эти данные через настоящую библиотеку go-webauthn, а не через подставные моки.
type virtualAuthenticator struct {
	priv    *ecdsa.PrivateKey
	credID  []byte
	counter uint32
}

func newVirtualAuthenticator(t *testing.T) *virtualAuthenticator {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	credID := make([]byte, 16)
	if _, err := rand.Read(credID); err != nil {
		t.Fatalf("generate credential id: %v", err)
	}
	return &virtualAuthenticator{priv: priv, credID: credID}
}

func rpIDHash(rpID string) []byte {
	h := sha256.Sum256([]byte(rpID))
	return h[:]
}

// coseKey кодирует публичный ключ в формате COSE_Key (EC2/ES256), как это
// делает реальный аутентификатор, используя ровно тот же кодировщик CBOR
// (webauthncbor), которым backend будет декодировать данные обратно.
func (a *virtualAuthenticator) coseKey(t *testing.T) []byte {
	t.Helper()

	// Uncompressed SEC1 point: 0x04 || X(32) || Y(32) for P-256.
	pub, err := a.priv.PublicKey.Bytes()
	if err != nil {
		t.Fatalf("encode public key: %v", err)
	}
	if len(pub) != 65 || pub[0] != 0x04 {
		t.Fatalf("unexpected P-256 public key encoding: len=%d prefix=%#x", len(pub), pub[0])
	}
	x := pub[1:33]
	y := pub[33:65]

	key := webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{
			KeyType:   int64(webauthncose.EllipticKey),
			Algorithm: int64(webauthncose.AlgES256),
		},
		Curve:  int64(webauthncose.P256),
		XCoord: x,
		YCoord: y,
	}

	b, err := webauthncbor.Marshal(key)
	if err != nil {
		t.Fatalf("marshal COSE key: %v", err)
	}
	return b
}

// authDataRegister собирает authenticatorData для ceremony регистрации:
// rpIdHash || flags(UP|UV|AT) || counter || AAGUID(0) || credIdLen || credId || COSE-ключ.
func (a *virtualAuthenticator) authDataRegister(t *testing.T, rpID string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write(rpIDHash(rpID))
	buf.WriteByte(flagUserPresent | flagUserVerified | flagAttestedCredentialAT)

	counter := make([]byte, 4)
	binary.BigEndian.PutUint32(counter, a.counter)
	buf.Write(counter)

	buf.Write(make([]byte, 16)) // AAGUID — демо-аутентификатор, нули.

	idLen := make([]byte, 2)
	binary.BigEndian.PutUint16(idLen, uint16(len(a.credID)))
	buf.Write(idLen)
	buf.Write(a.credID)
	buf.Write(a.coseKey(t))

	return buf.Bytes()
}

// authDataLogin собирает authenticatorData для ceremony логина (без attested
// credential data): rpIdHash || flags(UP|UV) || counter.
func (a *virtualAuthenticator) authDataLogin(rpID string) []byte {
	var buf bytes.Buffer
	buf.Write(rpIDHash(rpID))
	buf.WriteByte(flagUserPresent | flagUserVerified)

	counter := make([]byte, 4)
	binary.BigEndian.PutUint32(counter, a.counter)
	buf.Write(counter)

	return buf.Bytes()
}

func clientDataJSON(t *testing.T, typ, challenge, origin string) []byte {
	t.Helper()
	b, err := json.Marshal(struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}{Type: typ, Challenge: challenge, Origin: origin})
	if err != nil {
		t.Fatalf("marshal clientDataJSON: %v", err)
	}
	return b
}

// buildAttestation собирает тело POST /register/finish: attestationObject
// формата "none" (fmt=none, пустой attStmt) поверх честного authenticatorData.
func (a *virtualAuthenticator) buildAttestation(t *testing.T, rpID, origin, challenge string) []byte {
	t.Helper()

	cData := clientDataJSON(t, "webauthn.create", challenge, origin)
	authData := a.authDataRegister(t, rpID)

	attObj, err := webauthncbor.Marshal(struct {
		Fmt      string                 `cbor:"fmt"`
		AttStmt  map[string]interface{} `cbor:"attStmt"`
		AuthData []byte                 `cbor:"authData"`
	}{Fmt: "none", AttStmt: map[string]interface{}{}, AuthData: authData})
	if err != nil {
		t.Fatalf("marshal attestationObject: %v", err)
	}

	credB64 := base64.RawURLEncoding.EncodeToString(a.credID)

	body, err := json.Marshal(map[string]any{
		"id":    credB64,
		"rawId": credB64,
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(cData),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attObj),
		},
	})
	if err != nil {
		t.Fatalf("marshal attestation body: %v", err)
	}
	return body
}

// buildAssertion собирает тело POST /login/finish: подписывает
// authData || sha256(clientDataJSON) приватным ключом ES256 (ASN.1 DER,
// как того требует webauthncose.EC2PublicKeyData.Verify).
func (a *virtualAuthenticator) buildAssertion(t *testing.T, rpID, origin, challenge string) []byte {
	t.Helper()

	cData := clientDataJSON(t, "webauthn.get", challenge, origin)
	authData := a.authDataLogin(rpID)

	clientHash := sha256.Sum256(cData)
	sigData := append(append([]byte{}, authData...), clientHash[:]...)
	digest := sha256.Sum256(sigData)

	sig, err := ecdsa.SignASN1(rand.Reader, a.priv, digest[:])
	if err != nil {
		t.Fatalf("sign assertion: %v", err)
	}

	credB64 := base64.RawURLEncoding.EncodeToString(a.credID)

	body, err := json.Marshal(map[string]any{
		"id":    credB64,
		"rawId": credB64,
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(cData),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"signature":         base64.RawURLEncoding.EncodeToString(sig),
		},
	})
	if err != nil {
		t.Fatalf("marshal assertion body: %v", err)
	}
	return body
}

type creationEnvelope struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
	} `json:"publicKey"`
}

type assertionEnvelope struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
	} `json:"publicKey"`
}

func beginRegistration(t *testing.T, baseURL, username string) creationEnvelope {
	t.Helper()
	reqBody, _ := json.Marshal(map[string]string{"username": username})
	resp, err := http.Post(baseURL+"/register/begin", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("register/begin: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("register/begin status = %d, body=%s", resp.StatusCode, b)
	}
	var env creationEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode register/begin: %v", err)
	}
	return env
}

func finishRegister(t *testing.T, baseURL, username string, body []byte, wantStatus int) {
	t.Helper()
	url := baseURL + "/register/finish?username=" + username
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register/finish: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("register/finish status = %d, want %d, body=%s", resp.StatusCode, wantStatus, b)
	}
}

func beginLogin(t *testing.T, baseURL, username string) assertionEnvelope {
	t.Helper()
	reqBody, _ := json.Marshal(map[string]string{"username": username})
	resp, err := http.Post(baseURL+"/login/begin", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("login/begin: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("login/begin status = %d, body=%s", resp.StatusCode, b)
	}
	var env assertionEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode login/begin: %v", err)
	}
	return env
}

func finishLogin(t *testing.T, baseURL, username string, body []byte, wantStatus int) {
	t.Helper()
	url := baseURL + "/login/finish?username=" + username
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login/finish: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("login/finish status = %d, want %d, body=%s", resp.StatusCode, wantStatus, b)
	}
}

func TestRegisterThenLogin(t *testing.T) {
	rdb := newRDB(t)
	wa := newTestWebAuthn(t)
	ts := httptest.NewServer(newRouter(rdb, wa, ""))
	defer ts.Close()

	auth := newVirtualAuthenticator(t)
	const username = "alice"

	creation := beginRegistration(t, ts.URL, username)
	attBody := auth.buildAttestation(t, testRPID, testOrigin, creation.PublicKey.Challenge)
	finishRegister(t, ts.URL, username, attBody, http.StatusOK)

	auth.counter = 1
	assertionOpts := beginLogin(t, ts.URL, username)
	loginBody := auth.buildAssertion(t, testRPID, testOrigin, assertionOpts.PublicKey.Challenge)
	finishLogin(t, ts.URL, username, loginBody, http.StatusOK)
}

func TestLoginRejectsWrongOrigin(t *testing.T) {
	rdb := newRDB(t)
	wa := newTestWebAuthn(t)
	ts := httptest.NewServer(newRouter(rdb, wa, ""))
	defer ts.Close()

	auth := newVirtualAuthenticator(t)
	const username = "bob"

	creation := beginRegistration(t, ts.URL, username)
	attBody := auth.buildAttestation(t, testRPID, testOrigin, creation.PublicKey.Challenge)
	finishRegister(t, ts.URL, username, attBody, http.StatusOK)

	auth.counter = 1
	assertionOpts := beginLogin(t, ts.URL, username)
	// origin в clientDataJSON не совпадает с WEBAUTHN_RP_ORIGINS — библиотека
	// обязана отклонить ассерцию на шаге сверки CollectedClientData.
	loginBody := auth.buildAssertion(t, testRPID, "http://evil.example", assertionOpts.PublicKey.Challenge)
	finishLogin(t, ts.URL, username, loginBody, http.StatusUnauthorized)
}

func TestLoginRejectsCounterRollback(t *testing.T) {
	rdb := newRDB(t)
	wa := newTestWebAuthn(t)
	ts := httptest.NewServer(newRouter(rdb, wa, ""))
	defer ts.Close()

	auth := newVirtualAuthenticator(t)
	const username = "carol"

	creation := beginRegistration(t, ts.URL, username)
	attBody := auth.buildAttestation(t, testRPID, testOrigin, creation.PublicKey.Challenge)
	finishRegister(t, ts.URL, username, attBody, http.StatusOK)

	// Первый логин честно поднимает signature counter до 5 — успех.
	auth.counter = 5
	opts1 := beginLogin(t, ts.URL, username)
	body1 := auth.buildAssertion(t, testRPID, testOrigin, opts1.PublicKey.Challenge)
	finishLogin(t, ts.URL, username, body1, http.StatusOK)

	// Второй assertion с тем же counter (не возрос) — сигнал клонирования
	// аутентификатора, backend обязан отказать, а не просто принять.
	opts2 := beginLogin(t, ts.URL, username)
	body2 := auth.buildAssertion(t, testRPID, testOrigin, opts2.PublicKey.Challenge)
	finishLogin(t, ts.URL, username, body2, http.StatusUnauthorized)
}
