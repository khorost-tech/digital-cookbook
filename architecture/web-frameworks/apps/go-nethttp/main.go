// Минимальный: net/http из стандартной библиотеки, маршруты с методом и параметром (Go 1.22+).
package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

type item struct {
	Name string `json:"name"`
	Qty  int    `json:"qty"`
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": id, "name": "item"})
	})
	mux.HandleFunc("POST /items", func(w http.ResponseWriter, r *http.Request) {
		var it item
		if err := json.NewDecoder(r.Body).Decode(&it); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(it)
	})
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, r *http.Request) { panic("boom: секрет в сообщении marker-7f3a9c") })
	http.ListenAndServe(":8080", mux)
}
