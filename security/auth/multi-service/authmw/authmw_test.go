package authmw

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"khorost.tech/cookbook/auth/internal/jwtclaims"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddlewareValid(t *testing.T) {
	sec := []byte("s")
	raw, _ := jwtclaims.Issue(sec, jwtclaims.Claims{AccountID: 3, Roles: []string{"user"}}, time.Minute)
	h := Middleware(sec, Opts{})(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200 got %d", rec.Code)
	}
}

func TestMiddlewareMissing(t *testing.T) {
	h := Middleware([]byte("s"), Opts{})(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 401 {
		t.Fatalf("want 401 got %d", rec.Code)
	}
}

func TestMiddlewareValidViaCookie(t *testing.T) {
	sec := []byte("s")
	raw, _ := jwtclaims.Issue(sec, jwtclaims.Claims{AccountID: 3}, time.Minute)
	h := Middleware(sec, Opts{})(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "app_at", Value: raw})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200 got %d", rec.Code)
	}
}

func TestMiddlewareInvalidToken(t *testing.T) {
	h := Middleware([]byte("s"), Opts{})(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("want 401 got %d", rec.Code)
	}
}

func TestMiddlewareOptionalPassThrough(t *testing.T) {
	h := Middleware([]byte("s"), Opts{Optional: true})(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("want 200 got %d", rec.Code)
	}
}

func TestRequireRoleForbidden(t *testing.T) {
	sec := []byte("s")
	raw, _ := jwtclaims.Issue(sec, jwtclaims.Claims{AccountID: 3, Roles: []string{"user"}}, time.Minute)
	h := Middleware(sec, Opts{})(RequireRole("admin")(okHandler()))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("want 403 got %d", rec.Code)
	}
}

func TestRequireRoleAllowed(t *testing.T) {
	sec := []byte("s")
	raw, _ := jwtclaims.Issue(sec, jwtclaims.Claims{AccountID: 3, Roles: []string{"admin"}}, time.Minute)
	h := Middleware(sec, Opts{})(RequireRole("admin")(okHandler()))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200 got %d", rec.Code)
	}
}
