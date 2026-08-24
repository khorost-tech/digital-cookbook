// router.go — маршрутизация на стандартном http.ServeMux с method+path
// паттернами Go 1.22: строки вида "GET /users/{id}" разбирают и метод, и путь,
// а wildcard-сегмент достаётся через r.PathValue("id"). До 1.22 такое требовало
// стороннего роутера (chi, gorilla/mux); теперь stdlib закрывает базовые нужды.
package router

import (
	"encoding/json"
	"net/http"
)

// User — минимальная доменная модель для примера.
type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// New собирает *http.ServeMux с method-aware маршрутами. Возвращаем именно
// *ServeMux (а не http.Handler), чтобы вызывающий мог при желании довесить
// маршруты; сам ServeMux реализует http.Handler и годится для http.Server.
func New() *http.ServeMux {
	mux := http.NewServeMux()

	// "GET /users/{id}" — метод и путь в одном паттерне. Несовпадение метода
	// даёт 405, несовпадение пути — 404; всё это ServeMux делает сам.
	mux.HandleFunc("GET /users/{id}", getUser)

	// Отдельный обработчик на POST того же префикса: методы не конфликтуют,
	// ServeMux выбирает по паре (метод, путь).
	mux.HandleFunc("POST /users", createUser)

	return mux
}

// getUser достаёт wildcard-сегмент {id} через r.PathValue и отдаёт JSON.
func getUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	writeJSON(w, http.StatusOK, User{ID: id, Name: "user-" + id})
}

// createUser разбирает тело запроса и отвечает 201 Created.
func createUser(w http.ResponseWriter, r *http.Request) {
	var u User
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		http.Error(w, "некорректный JSON", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

// writeJSON — общий помощник: выставляет заголовок и кодирует значение.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
