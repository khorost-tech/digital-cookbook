package cache

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// Handler возвращает http.Handler с единственным маршрутом GET /user/{id}:
// отдаёт профиль пользователя как JSON. Заголовок X-Cache сообщает, был ли
// ответ получен из кэша (HIT) или загружен из репозитория (MISS).
//
//	200 + JSON профиля, X-Cache: HIT|MISS — профиль найден
//	400                                    — id не число
//	404                                    — профиль не найден (ErrNotFound)
func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		p, fromCache, err := s.GetProfile(r.Context(), id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.Error(w, "profile not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		cacheStatus := "MISS"
		if fromCache {
			cacheStatus = "HIT"
		}

		w.Header().Set("X-Cache", cacheStatus)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(p)
	})
	return mux
}
