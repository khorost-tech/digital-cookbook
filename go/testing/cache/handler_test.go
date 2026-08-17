package cache_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"khorost.tech/go-testing/cache"
)

// newTestService строит Service с фейками (fakeRepo/fakeCache из
// service_test.go, тот же пакет cache_test) поверх заданного набора профилей.
func newTestService(profiles map[int64]cache.Profile) *cache.Service {
	repo := &fakeRepo{profiles: profiles}
	c := &fakeCache{data: map[string]string{}}
	return &cache.Service{Repo: repo, Cache: c, TTL: time.Minute}
}

// TestHandlerRecorder гоняет Handler через httptest.NewRecorder — проверяет
// 200/тело/X-Cache MISS→HIT на повторный запрос, 404 на неизвестного
// пользователя и 400 на нечисловой id.
func TestHandlerRecorder(t *testing.T) {
	t.Parallel()

	alice := cache.Profile{ID: 1, Name: "Alice", Email: "alice@example.com"}
	svc := newTestService(map[int64]cache.Profile{1: alice})
	h := cache.Handler(svc)

	// первый запрос — промах кэша, идём в репозиторий
	req1, err := http.NewRequest(http.MethodGet, "/user/1", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: status = %d, want %d", rec1.Code, http.StatusOK)
	}
	if got := rec1.Header().Get("X-Cache"); got != "MISS" {
		t.Errorf("first request: X-Cache = %q, want MISS", got)
	}
	if ct := rec1.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("first request: Content-Type = %q, want application/json", ct)
	}
	// Проверяем ТОЧНЫЙ набор wire-полей, а не только декодирование обратно в
	// cache.Profile: декодирование в ту же структуру замаскировало бы смену
	// wire-схемы (добавили/переименовали json-тег — encoder и тестовый
	// decoder поменялись бы синхронно, тест остался бы зелёным). Разбор в
	// map[string]json.RawMessage и сверка набора ключей ловит и лишнее, и
	// переименованное/пропавшее поле.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rec1.Body.Bytes(), &fields); err != nil {
		t.Fatalf("тело ответа — не JSON-объект: %v (%s)", err, rec1.Body.String())
	}
	for _, k := range []string{"ID", "Name", "Email"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("в теле нет ожидаемого поля %q: %s", k, rec1.Body.String())
		}
		delete(fields, k)
	}
	if len(fields) != 0 {
		t.Errorf("в теле лишние (неожиданные) поля: %v", fields)
	}
	var gotProfile cache.Profile
	if err := json.Unmarshal(rec1.Body.Bytes(), &gotProfile); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if gotProfile != alice {
		t.Errorf("first request: body = %+v, want %+v", gotProfile, alice)
	}

	// второй запрос на тот же id — попадание в кэш
	req2, err := http.NewRequest(http.MethodGet, "/user/1", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("second request: status = %d, want %d", rec2.Code, http.StatusOK)
	}
	if got := rec2.Header().Get("X-Cache"); got != "HIT" {
		t.Errorf("second request: X-Cache = %q, want HIT", got)
	}

	// несуществующий пользователь -> 404
	req3, err := http.NewRequest(http.MethodGet, "/user/999", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusNotFound {
		t.Errorf("unknown user: status = %d, want %d", rec3.Code, http.StatusNotFound)
	}

	// нечисловой id -> 400
	req4, err := http.NewRequest(http.MethodGet, "/user/abc", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	rec4 := httptest.NewRecorder()
	h.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusBadRequest {
		t.Errorf("non-numeric id: status = %d, want %d", rec4.Code, http.StatusBadRequest)
	}
}

// TestHandler_MethodAndError покрывает оставшиеся ветки HTTP-матрицы, не
// затронутые happy-path: 405 на неверный метод (ServeMux Go 1.22 сам
// отвечает Method Not Allowed на зарегистрированный путь GET /user/{id}) и
// 500, когда сервис вернул не-ErrNotFound ошибку (репозиторий недоступен —
// именно эта ветка handler.go иначе никогда не исполнялась бы в тестах).
func TestHandler_MethodAndError(t *testing.T) {
	t.Parallel()

	alice := cache.Profile{ID: 1, Name: "Alice", Email: "alice@example.com"}

	// 405: путь зарегистрирован, но метод не тот
	h := cache.Handler(newTestService(map[int64]cache.Profile{1: alice}))
	rec405 := httptest.NewRecorder()
	h.ServeHTTP(rec405, httptest.NewRequest(http.MethodPost, "/user/1", nil))
	if rec405.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /user/1: status = %d, want %d", rec405.Code, http.StatusMethodNotAllowed)
	}

	// 500: репозиторий вернул произвольную (не ErrNotFound) ошибку —
	// хендлер обязан отдать 500, а не 404.
	repo := &fakeRepo{err: errors.New("db: connection refused")}
	c := &fakeCache{data: map[string]string{}}
	svc := &cache.Service{Repo: repo, Cache: c, TTL: time.Minute}
	rec500 := httptest.NewRecorder()
	cache.Handler(svc).ServeHTTP(rec500, httptest.NewRequest(http.MethodGet, "/user/1", nil))
	if rec500.Code != http.StatusInternalServerError {
		t.Errorf("repo error: status = %d, want %d", rec500.Code, http.StatusInternalServerError)
	}
}

// TestHandlerServer показывает серверный вариант: httptest.NewServer +
// реальный http.Client вместо ServeHTTP напрямую.
func TestHandlerServer(t *testing.T) {
	t.Parallel()

	bob := cache.Profile{ID: 2, Name: "Bob", Email: "bob@example.com"}
	svc := newTestService(map[int64]cache.Profile{2: bob})

	srv := httptest.NewServer(cache.Handler(svc))
	defer srv.Close()

	client := srv.Client()
	resp, err := client.Get(srv.URL + "/user/2")
	if err != nil {
		t.Fatalf("GET /user/2: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("X-Cache"); got != "MISS" {
		t.Errorf("X-Cache = %q, want MISS", got)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var gotProfile cache.Profile
	if err := json.Unmarshal(body, &gotProfile); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if gotProfile != bob {
		t.Errorf("body = %+v, want %+v", gotProfile, bob)
	}
}
