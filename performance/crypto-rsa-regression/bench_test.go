// Стенд к статье «Go 1.20 и RSA: что просело и как ловить регрессии».
//
// Бенчмарки крипто-операций, снимаемые на РАЗНЫХ версиях Go (см. matrix.sh).
// Go 1.20 перевёл арифметику crypto/rsa с math/big на constant-time
// crypto/internal/bigmod. По матрице замедлились ОБЕ стороны RSA, но по-разному:
// ПУБЛИЧНЫЕ операции (verify/encrypt) — в 5–7 раз (+405…+602%); ПРИВАТНАЯ подпись —
// +34% (2048) и +61% (4096). Всё p<=0.001. ECDSA/HMAC на 1.19->1.20 значимо не
// изменились (p>0.4). Release notes Go 1.20: decryption +15–45%, public encryption ~20×;
// Go 1.21 notes подтверждают регрессию и расшифровки, и подписи.
//
// ВАЖНО ПРО ПРОТОКОЛ: эти числа получены только после смены методики — раунды с
// РОТАЦИЕЙ порядка версий (см. matrix.sh). Без неё разброс по подписи доходил до
// ±60–70%, и разница 1.19->1.20 выходила статистически НЕзначимой (p≈0.1).
//
// Ключи ФИКСИРОВАНЫ (testdata/*.pem, go:embed) и одинаковы для всех версий Go —
// иначе сравнение data-dependent math/big мешало бы разброс ключа с разбросом
// версии. Только stdlib — компилируется на Go 1.19…1.26.
//
// RS256 (подпись JWT) = rsa.SignPKCS1v15 + SHA-256; golang-jwt зовёт те же
// примитивы crypto/rsa.
package crsabench

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"fmt"
	"os"
	"testing"
)

//go:embed testdata/rsa2048.pem
var rsa2048PEM []byte

//go:embed testdata/rsa4096.pem
var rsa4096PEM []byte

//go:embed testdata/ecp256.pem
var ecp256PEM []byte

var (
	rsa2048 *rsa.PrivateKey
	rsa4096 *rsa.PrivateKey
	ecKey   *ecdsa.PrivateKey

	// «JWT-подобный» payload и его SHA-256 дайджест (подписываем дайджест).
	msg    = []byte(`{"sub":"1234567890","name":"Ada Lovelace","iat":1516239022}`)
	digest = sha256.Sum256(msg)

	hmacKey = []byte("0123456789abcdef0123456789abcdef")

	// Предвычисленные подписи для verify-бенчей.
	rsa2048Sig []byte
	rsa4096Sig []byte
	ecSig      []byte
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup failed:", err)
		os.Exit(1)
	}
}

func parseRSA(pemBytes []byte) *rsa.PrivateKey {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		must(fmt.Errorf("не удалось декодировать PEM"))
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	must(err)
	return key.(*rsa.PrivateKey)
}

func parseECDSA(pemBytes []byte) *ecdsa.PrivateKey {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		must(fmt.Errorf("не удалось декодировать PEM"))
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	must(err)
	return key.(*ecdsa.PrivateKey)
}

func TestMain(m *testing.M) {
	// Фиксированные ключи — одинаковые на всех версиях Go.
	rsa2048 = parseRSA(rsa2048PEM)
	rsa4096 = parseRSA(rsa4096PEM)
	ecKey = parseECDSA(ecp256PEM)

	var err error
	if rsa2048Sig, err = rsa.SignPKCS1v15(rand.Reader, rsa2048, crypto.SHA256, digest[:]); err != nil {
		must(err)
	}
	if rsa4096Sig, err = rsa.SignPKCS1v15(rand.Reader, rsa4096, crypto.SHA256, digest[:]); err != nil {
		must(err)
	}
	if ecSig, err = ecdsa.SignASN1(rand.Reader, ecKey, digest[:]); err != nil {
		must(err)
	}

	os.Exit(m.Run())
}

// ── RSA: приватные операции (sign) — регрессия на 1.20 умеренная:
//    +34% (2048, p=0.001) и +61% (4096, p=0.000) при ротационном протоколе. ─────

func BenchmarkRSA2048Sign(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := rsa.SignPKCS1v15(rand.Reader, rsa2048, crypto.SHA256, digest[:]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRSA4096Sign(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := rsa.SignPKCS1v15(rand.Reader, rsa4096, crypto.SHA256, digest[:]); err != nil {
			b.Fatal(err)
		}
	}
}

// ── RSA: публичные операции (verify) — СЮДА бьёт регрессия Go 1.20 ────────────
// Публичная операция дешёвая (малый экспонент e=65537), и фиксированные
// per-call расходы bigmod (modulus/Montgomery/таблица степеней) на ней
// проявляются в разы; на дорогой приватной они тонут.

func BenchmarkRSA2048Verify(b *testing.B) {
	pub := &rsa2048.PublicKey
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], rsa2048Sig); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRSA4096Verify(b *testing.B) {
	pub := &rsa4096.PublicKey
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], rsa4096Sig); err != nil {
			b.Fatal(err)
		}
	}
}

// encMsg — короткий payload (как сессионный ключ) для RSA-шифрования.
var encMsg = []byte("0123456789abcdef0123456789abcdef")

// Encrypt — тоже ПУБЛИЧНАЯ операция (открытый экспонент). Именно её release note
// Go 1.20 называет «encryption ~20× slower». Меряем её напрямую, а не только verify.

func BenchmarkRSA2048Encrypt(b *testing.B) {
	pub := &rsa2048.PublicKey
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := rsa.EncryptPKCS1v15(rand.Reader, pub, encMsg); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRSA4096Encrypt(b *testing.B) {
	pub := &rsa4096.PublicKey
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := rsa.EncryptPKCS1v15(rand.Reader, pub, encMsg); err != nil {
			b.Fatal(err)
		}
	}
}

// ── ECDSA P-256 — контроль: на переходе 1.19→1.20 регрессии нет ───────────────

func BenchmarkECDSAP256Sign(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ecdsa.SignASN1(rand.Reader, ecKey, digest[:]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkECDSAP256Verify(b *testing.B) {
	pub := &ecKey.PublicKey
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !ecdsa.VerifyASN1(pub, digest[:], ecSig) {
			b.Fatal("verify failed")
		}
	}
}

// ── HMAC-SHA256 — контроль: симметрика, регрессии нет ─────────────────────────

func BenchmarkHMACSHA256(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mac := hmac.New(sha256.New, hmacKey)
		mac.Write(msg)
		_ = mac.Sum(nil)
	}
}

// ── RS256 round-trip (подпись + проверка «JWT») — sign доминирует ─────────────

func BenchmarkRS256Roundtrip(b *testing.B) {
	pub := &rsa2048.PublicKey
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sig, err := rsa.SignPKCS1v15(rand.Reader, rsa2048, crypto.SHA256, digest[:])
		if err != nil {
			b.Fatal(err)
		}
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
			b.Fatal(err)
		}
	}
}
