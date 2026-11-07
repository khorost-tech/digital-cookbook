// Package httpapi — HTTP-граница сервиса: маршруты, разбор запроса, ошибки домена в коды ответа.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"shortlinks/internal/links"
)

type API struct {
	Links   *links.Service
	BaseURL string
	Ready   *atomic.Bool  // false — /readyz отвечает 503: балансировщик перестаёт слать трафик
	SlowRed time.Duration // искусственная задержка редиректа (опыты с остановкой)

	requests *prometheus.CounterVec
	latency  *prometheus.HistogramVec
}

func New(svc *links.Service, baseURL string, ready *atomic.Bool, reg prometheus.Registerer) *API {
	a := &API{Links: svc, BaseURL: baseURL, Ready: ready,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "http_requests_total", Help: "HTTP-запросы по маршруту и коду"},
			[]string{"route", "code"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "http_request_duration_seconds", Help: "Длительность HTTP-запроса",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1}}, []string{"route"})}
	reg.MustRegister(a.requests, a.latency)
	return a
}

func (a *API) Routes(mux *http.ServeMux) {
	mux.Handle("POST /links", a.observe("create", a.create))
	mux.Handle("GET /links/{code}/stats", a.observe("stats", a.stats))
	mux.Handle("GET /{code}", a.observe("redirect", a.redirect))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !a.Ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

func (a *API) observe(route string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		h(sw, r)
		a.requests.WithLabelValues(route, strconv.Itoa(sw.code)).Inc()
		a.latency.WithLabelValues(route).Observe(time.Since(t).Seconds())
	})
}

// writeError — единственное место, где ошибки домена становятся кодами ответа.
// Внутренние подробности наружу не уходят: клиенту — класс ошибки, в лог — причина.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := http.StatusInternalServerError, "внутренняя ошибка"
	switch {
	case errors.Is(err, links.ErrInvalidURL):
		status, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, links.ErrNotFound):
		status, msg = http.StatusNotFound, err.Error()
	case errors.Is(err, context.Canceled):
		status, msg = 499, "клиент ушёл" // nginx-код: ответ уже некому отдать
	case errors.Is(err, context.DeadlineExceeded):
		status, msg = http.StatusServiceUnavailable, "не успели"
	}
	if status >= 500 {
		slog.Error("запрос не обработан", "path", r.URL.Path, "err", err)
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "тело — JSON {\"url\": …}"})
		return
	}
	l, err := a.Links.Create(r.Context(), req.URL)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"code": l.Code, "short_url": a.BaseURL + "/" + l.Code})
}

func (a *API) redirect(w http.ResponseWriter, r *http.Request) {
	if a.SlowRed > 0 {
		time.Sleep(a.SlowRed)
	}
	target, err := a.Links.Redirect(r.Context(), r.PathValue("code"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (a *API) stats(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	n, err := a.Links.Stats(r.Context(), code)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "clicks": n})
}
