package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Тело реального уведомления Alertmanager 0.33.1, снятое со стенда. Формат
// version=4; сокращено до полей, которые проверка использует.
const sampleNotification = `{
  "receiver": "webhook",
  "status": "firing",
  "alerts": [
    {
      "status": "firing",
      "labels": {"alertname": "SLOBurnRateFast", "service_name": "go-frontend", "severity": "critical"},
      "annotations": {"summary": "бюджет ошибок тратится быстро"},
      "startsAt": "2026-08-06T10:00:00.000Z"
    }
  ],
  "groupLabels": {"alertname": "SLOBurnRateFast"},
  "groupKey": "{}:{alertname=\"SLOBurnRateFast\"}",
  "version": "4"
}`

func postNotification(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/alerts", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Alertmanager считает доставку неудачной, если приёмник не ответил HTTP-ответом:
// проверено на стенде заглушкой из netcat — шесть попыток и явный отказ в логах.
// Поэтому первое, что проверяется, — что ответ вообще есть и он 200.
func TestPostAlertAnswers200(t *testing.T) {
	s := NewStore()
	rec := postNotification(t, s.Routes(), sampleNotification)

	if rec.Code != http.StatusOK {
		t.Fatalf("код ответа = %d, ожидался 200: Alertmanager сочтёт доставку неудачной", rec.Code)
	}
}

func TestStoredAlertIsReadableBack(t *testing.T) {
	s := NewStore()
	h := s.Routes()
	postNotification(t, h, sampleNotification)

	req := httptest.NewRequest(http.MethodGet, "/alerts", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /alerts вернул %d", rec.Code)
	}

	var got struct {
		Received int `json:"received"`
		Alerts   []struct {
			Status string            `json:"status"`
			Labels map[string]string `json:"labels"`
		} `json:"alerts"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("ответ не разбирается как JSON: %v", err)
	}

	if got.Received != 1 {
		t.Errorf("received = %d, ожидалось 1", got.Received)
	}
	if len(got.Alerts) != 1 {
		t.Fatalf("алертов = %d, ожидался 1", len(got.Alerts))
	}
	if got.Alerts[0].Labels["alertname"] != "SLOBurnRateFast" {
		t.Errorf("alertname = %q", got.Alerts[0].Labels["alertname"])
	}
	if got.Alerts[0].Status != "firing" {
		t.Errorf("status = %q, ожидался firing", got.Alerts[0].Status)
	}
}

// Уведомление о том, что алерт погас, приходит тем же путём с status=resolved.
// Проверка «алерт не горит на нормальной нагрузке» опирается именно на это, и
// путать firing с resolved нельзя.
func TestResolvedIsDistinguishedFromFiring(t *testing.T) {
	s := NewStore()
	h := s.Routes()
	postNotification(t, h, sampleNotification)
	postNotification(t, h, strings.ReplaceAll(sampleNotification, `"firing"`, `"resolved"`))

	if got := s.CountByStatus("firing"); got != 1 {
		t.Errorf("firing = %d, ожидался 1", got)
	}
	if got := s.CountByStatus("resolved"); got != 1 {
		t.Errorf("resolved = %d, ожидался 1", got)
	}
	if got := s.CountByName("SLOBurnRateFast"); got != 2 {
		t.Errorf("по имени = %d, ожидалось 2", got)
	}
}

// Некорректное тело не должно ронять приёмник: упавший приёмник Alertmanager
// начнёт перебирать попытки, и в логах это будет выглядеть как проблема
// доставки, а не как проблема тела запроса.
func TestBadBodyIsRejectedWithoutPanic(t *testing.T) {
	s := NewStore()
	rec := postNotification(t, s.Routes(), "{это не json")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("код ответа = %d, ожидался 400", rec.Code)
	}
	if s.Received() != 0 {
		t.Errorf("некорректное уведомление попало в хранилище")
	}
}

// Alertmanager отправляет уведомления параллельно по группам. Счётчик и срез без
// мьютекса — гонка данных; последовательный тест её не увидит, этот — увидит
// под -race.
func TestConcurrentPostsAreCountedExactly(t *testing.T) {
	s := NewStore()
	h := s.Routes()

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			postNotification(t, h, sampleNotification)
		}()
	}
	wg.Wait()

	if got := s.Received(); got != n {
		t.Errorf("получено %d уведомлений, ожидалось ровно %d", got, n)
	}
	if got := s.CountByName("SLOBurnRateFast"); got != n {
		t.Errorf("по имени %d, ожидалось ровно %d", got, n)
	}
}

// Сброс нужен проверке: она снимает состояние до сценария и после, а не
// полагается на пустой старт контейнера.
func TestResetClearsEverything(t *testing.T) {
	s := NewStore()
	h := s.Routes()
	postNotification(t, h, sampleNotification)

	req := httptest.NewRequest(http.MethodPost, "/reset", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /reset вернул %d", rec.Code)
	}
	if s.Received() != 0 {
		t.Errorf("после сброса получено %d", s.Received())
	}
}
