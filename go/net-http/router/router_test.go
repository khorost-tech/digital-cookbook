// router_test.go — тесты через httptest.NewRequest/NewRecorder (без запуска
// сетевого сервера) доказывают поведение ServeMux Go 1.22: верный метод+путь
// дают 200 и корректный id, чужой метод — 405, несуществующий путь — 404.
package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGetUserReturnsPathValue: GET /users/{id} → 200 и id из пути.
func TestGetUserReturnsPathValue(t *testing.T) {
	mux := New()
	req := httptest.NewRequest(http.MethodGet, "/users/42", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ожидали 200, получили %d", rec.Code)
	}
	var u User
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatalf("не разобрали тело: %v", err)
	}
	if u.ID != "42" {
		t.Errorf("ожидали id=42, получили %q", u.ID)
	}
}

// TestWrongMethodReturns405: POST на маршрут, зарегистрированный только под GET,
// даёт 405 — ServeMux знает про метод.
func TestWrongMethodReturns405(t *testing.T) {
	mux := New()
	req := httptest.NewRequest(http.MethodDelete, "/users/42", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("ожидали 405, получили %d", rec.Code)
	}
	// ServeMux сам выставляет заголовок Allow со списком разрешённых методов.
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("ожидали в Allow метод GET, получили %q", allow)
	}
}

// TestUnknownPathReturns404: путь без зарегистрированного маршрута — 404.
func TestUnknownPathReturns404(t *testing.T) {
	mux := New()
	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("ожидали 404, получили %d", rec.Code)
	}
}

// TestCreateUserReturns201: POST /users с валидным JSON → 201 и эхо тела.
func TestCreateUserReturns201(t *testing.T) {
	mux := New()
	body := strings.NewReader(`{"id":"7","name":"Ada"}`)
	req := httptest.NewRequest(http.MethodPost, "/users", body)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("ожидали 201, получили %d", rec.Code)
	}
	var u User
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatalf("не разобрали тело: %v", err)
	}
	if u.Name != "Ada" {
		t.Errorf("ожидали name=Ada, получили %q", u.Name)
	}
}
