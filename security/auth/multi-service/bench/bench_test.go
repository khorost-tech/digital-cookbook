// Package bench сравнивает цену локальной валидации JWT (jwtclaims.Parse, в
// процессе) с ценой похода за подтверждением токена в отдельный сервис по
// HTTP (round-trip) — числовое обоснование выбора "локальная валидация без
// похода в auth на каждый запрос" из статьи.
package bench

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"khorost.tech/cookbook/auth/internal/jwtclaims"
)

// BenchmarkLocalValidate измеряет цену jwtclaims.Parse — то, что реально
// делает multi-service/authmw на каждый запрос.
func BenchmarkLocalValidate(b *testing.B) {
	secret := []byte("bench-secret")
	raw, err := jwtclaims.Issue(secret, jwtclaims.Claims{AccountID: 1, Nick: "bench"}, time.Hour)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := jwtclaims.Parse(secret, raw); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRoundTrip измеряет цену гипотетической альтернативы — похода в
// auth-сервис за подтверждением токена на каждый запрос. Сервер эмулирует
// его работу (валидация + ответ) искусственной задержкой в 1ms; в реальности
// к этому добавились бы сеть между подами/хостами и нагрузка на auth.
func BenchmarkRoundTrip(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(srv.URL)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}
