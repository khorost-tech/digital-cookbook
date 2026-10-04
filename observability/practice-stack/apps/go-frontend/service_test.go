package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Базовая линия должна быть заведомо рабочей: если позже, с подключённым
// инструментированием, что-то отвалится, эти тесты отвечают на вопрос
// «приложение сломано или телеметрия».

func quietLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

type fakeInventory struct {
	items map[string]Item
	err   error
}

func (f fakeInventory) Lookup(_ context.Context, sku string) (Item, error) {
	if f.err != nil {
		return Item{}, f.err
	}
	item, ok := f.items[sku]
	if !ok {
		return Item{}, ErrNotFound
	}
	return item, nil
}

// Двойник тоже вызывается из разных горутин: Publisher дёргается из обработчика
// HTTP, а тот параллелен. Без мьютекса `go test -race` показывает DATA RACE
// внутри самого теста — и это не придуманный пример, а найденный здесь.
type recordingBus struct {
	mu        sync.Mutex
	published [][]byte
	err       error
}

func (r *recordingBus) Publish(_ context.Context, _ string, payload []byte) error {
	if r.err != nil {
		return r.err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.published = append(r.published, payload)
	return nil
}

func (r *recordingBus) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.published)
}

func newTestService(inv InventoryClient, bus Publisher) *Service {
	svc := &Service{Inventory: inv, Bus: bus, Subject: "orders.created", Log: quietLogger()}
	// SDK в тестах не инициализирован намеренно: инструменты берутся из
	// глобальных no-op провайдеров. Тесты проверяют логику, и то, что она
	// работает без телеметрии, — часть требований.
	if err := svc.initInstruments(); err != nil {
		panic(err)
	}
	return svc
}

func TestHandleOrder(t *testing.T) {
	inv := fakeInventory{items: map[string]Item{
		"SKU-1": {SKU: "SKU-1", Name: "Гайка", Quantity: 10},
		"SKU-2": {SKU: "SKU-2", Name: "Болт", Quantity: 1},
	}}

	cases := []struct {
		name         string
		body         string
		wantStatus   int
		wantReserved bool
		wantEvents   int
	}{
		{"успешный заказ", `{"sku":"SKU-1","quantity":3}`, http.StatusCreated, true, 1},
		{"товара не хватает", `{"sku":"SKU-2","quantity":5}`, http.StatusCreated, false, 1},
		{"неизвестный sku", `{"sku":"NOPE","quantity":1}`, http.StatusNotFound, false, 0},
		{"пустой sku", `{"sku":"","quantity":1}`, http.StatusBadRequest, false, 0},
		{"нулевое количество", `{"sku":"SKU-1","quantity":0}`, http.StatusBadRequest, false, 0},
		{"битый JSON", `{sku`, http.StatusBadRequest, false, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := &recordingBus{}
			svc := newTestService(inv, bus)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/order", strings.NewReader(tc.body))
			svc.Routes().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("код ответа = %d, ожидался %d (тело: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if bus.count() != tc.wantEvents {
				t.Errorf("опубликовано событий = %d, ожидалось %d", bus.count(), tc.wantEvents)
			}
			if tc.wantStatus != http.StatusCreated {
				return
			}
			var resp OrderResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("ответ не разобрался: %v", err)
			}
			if resp.Reserved != tc.wantReserved {
				t.Errorf("reserved = %v, ожидалось %v", resp.Reserved, tc.wantReserved)
			}
			if resp.OrderID == "" {
				t.Error("order_id пуст")
			}
		})
	}
}

// Отказ публикации не должен ронять заказ: событие вторично, заказ уже создан.
func TestPublishFailureDoesNotFailOrder(t *testing.T) {
	inv := fakeInventory{items: map[string]Item{"SKU-1": {SKU: "SKU-1", Quantity: 5}}}
	svc := newTestService(inv, &recordingBus{err: context.DeadlineExceeded})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/order", strings.NewReader(`{"sku":"SKU-1","quantity":1}`))
	svc.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("код ответа = %d, ожидался 201", rec.Code)
	}
}

// Отказ бэкенда склада — это 502, а не 404 и не 500: различие кодов и есть
// смысл будущей метрики ошибок.
func TestUpstreamFailureMapsTo502(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer backend.Close()

	svc := newTestService(newHTTPInventory(backend.URL, 2*time.Second), &recordingBus{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/order", strings.NewReader(`{"sku":"SKU-1","quantity":1}`))
	svc.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("код ответа = %d, ожидался 502", rec.Code)
	}
}

func TestInventoryClientDecodesItem(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inventory/SKU-7" {
			t.Errorf("путь запроса = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"sku":"SKU-7","name":"Шайба","quantity":42}`)
	}))
	defer backend.Close()

	item, err := newHTTPInventory(backend.URL, 2*time.Second).Lookup(context.Background(), "SKU-7")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if item.Name != "Шайба" || item.Quantity != 42 {
		t.Errorf("получен %+v", item)
	}
}

func TestHealth(t *testing.T) {
	svc := newTestService(fakeInventory{}, &recordingBus{})
	rec := httptest.NewRecorder()
	svc.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health вернул %d", rec.Code)
	}
}

// Параллельная нагрузка на обработчик: без atomic-счётчика этот тест под -race
// сообщает WARNING: DATA RACE. Ради этого он и написан — гонка в счётчике
// заказов была в первой редакции сервиса.
func TestConcurrentOrdersNoRace(t *testing.T) {
	inv := fakeInventory{items: map[string]Item{"SKU-1": {SKU: "SKU-1", Quantity: 1000}}}
	svc := newTestService(inv, &recordingBus{})
	handler := svc.Routes()

	const n = 64
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/order", strings.NewReader(`{"sku":"SKU-1","quantity":1}`))
			handler.ServeHTTP(rec, req)
			done <- rec.Code
		}()
	}

	for i := 0; i < n; i++ {
		if code := <-done; code != http.StatusCreated {
			t.Errorf("код ответа = %d", code)
		}
	}
	// Счётчик должен был выдать n различных значений.
	if got := svc.counter.Load(); got != n {
		t.Errorf("счётчик = %d, ожидалось %d", got, n)
	}
}
