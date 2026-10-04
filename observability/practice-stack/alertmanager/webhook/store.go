package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
)

// Notification — тело, которое присылает Alertmanager (schema version 4).
// Разбираются только поля, нужные проверке; остальные игнорируются, чтобы
// приёмник не ломался при смене версии Alertmanager.
type Notification struct {
	Receiver    string            `json:"receiver"`
	Status      string            `json:"status"`
	Alerts      []Alert           `json:"alerts"`
	GroupLabels map[string]string `json:"groupLabels"`
	GroupKey    string            `json:"groupKey"`
	Version     string            `json:"version"`
}

type Alert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    string            `json:"startsAt"`
}

// Store держит полученное в памяти. Хранилища на диске здесь не нужно: приёмник
// существует ради одного — дать проверке ТОЧНЫЙ критерий доставки вместо
// «посмотрел в логи Alertmanager, вроде отправилось».
//
// Мьютекс не для красоты: Alertmanager отправляет уведомления по группам
// параллельно, и без него срез и счётчик — гонка данных. Ловится `go test -race`
// в TestConcurrentPostsAreCountedExactly.
type Store struct {
	mu     sync.Mutex
	alerts []Alert
	// Счётчик уведомлений считается отдельно от алертов: в одном уведомлении их
	// может быть несколько, и путать «пришло 3 уведомления» с «пришло 3 алерта»
	// значит получить неверный критерий.
	notifications int
}

func NewStore() *Store {
	return &Store{}
}

func (s *Store) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /alerts", s.handlePost)
	mux.HandleFunc("GET /alerts", s.handleGet)
	mux.HandleFunc("POST /reset", s.handleReset)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func (s *Store) handlePost(w http.ResponseWriter, r *http.Request) {
	var n Notification
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		// 400, а не 500: Alertmanager при 5xx уходит в повторы и в логах это
		// выглядит как проблема доставки, хотя дело в теле запроса.
		http.Error(w, "некорректный JSON", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.notifications++
	s.alerts = append(s.alerts, n.Alerts...)
	s.mu.Unlock()

	for _, a := range n.Alerts {
		log.Printf("алерт %s: %s (%s)", a.Labels["alertname"], a.Status, a.Labels["service_name"])
	}

	// Ответ обязателен. Приёмник, который не отвечает HTTP-ответом, Alertmanager
	// считает недоступным: проверено заглушкой на netcat — шесть попыток и
	// «notify retry canceled after 6 attempts» в логах.
	w.WriteHeader(http.StatusOK)
}

func (s *Store) handleGet(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := struct {
		Received int     `json:"received"`
		Alerts   []Alert `json:"alerts"`
	}{
		Received: s.notifications,
		Alerts:   append([]Alert(nil), s.alerts...),
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Store) handleReset(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.alerts = nil
	s.notifications = 0
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *Store) Received() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notifications
}

func (s *Store) CountByStatus(status string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.alerts {
		if a.Status == status {
			n++
		}
	}
	return n
}

func (s *Store) CountByName(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.alerts {
		if a.Labels["alertname"] == name {
			n++
		}
	}
	return n
}
