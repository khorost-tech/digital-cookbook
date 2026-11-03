package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestStallWindows(t *testing.T) {
	t0 := time.Unix(1000, 0)
	s := stall{start: t0, p: time.Second, t: 10 * time.Second}
	cases := []struct {
		at   time.Duration
		want time.Duration // 0 — не пауза
	}{
		{0, 0}, {8999 * time.Millisecond, 0},
		{9 * time.Second, 10 * time.Second}, {9500 * time.Millisecond, 10 * time.Second},
		{10 * time.Second, 0}, {19 * time.Second, 20 * time.Second},
	}
	for _, c := range cases {
		got := s.until(t0.Add(c.at))
		if c.want == 0 && !got.IsZero() || c.want != 0 && !got.Equal(t0.Add(c.want)) {
			t.Errorf("at %v: got %v, want %v", c.at, got.Sub(t0), c.want)
		}
	}
	if !(stall{}).until(t0).IsZero() {
		t.Error("без расписания пауз нет")
	}
}

// Ёмкость: 80 одновременных запросов при C=8 и S=10 мс — 10 волн, ≈100 мс.
func TestCapacity(t *testing.T) {
	t.Setenv("S", "10ms")
	t.Setenv("C", "8")
	sv := newServer()
	ts := httptest.NewServer(http.HandlerFunc(sv.work))
	defer ts.Close()
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 80; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(ts.URL)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	el := time.Since(start)
	if el < 100*time.Millisecond || el > 200*time.Millisecond {
		t.Errorf("80 запросов при C=8, S=10ms: %v, ожидалось ≈100ms", el)
	}
	if len(sv.lat) != 80 {
		t.Errorf("сервис насчитал %d запросов из 80", len(sv.lat))
	}
}

// Пауза задерживает начало обслуживания до конца окна.
func TestStallDelaysService(t *testing.T) {
	t.Setenv("S", "1ms")
	sv := newServer()
	sv.stall = stall{start: time.Now().Add(-950 * time.Millisecond), p: 200 * time.Millisecond, t: time.Second}
	ts := httptest.NewServer(http.HandlerFunc(sv.work))
	defer ts.Close()
	start := time.Now()
	resp, err := http.Get(ts.URL) // попадает в окно [800, 1000) мс — ждёт ≈50 мс
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if el := time.Since(start); el < 40*time.Millisecond || el > 120*time.Millisecond {
		t.Errorf("запрос в паузе: %v, ожидалось ≈50ms", el)
	}
}

// Запасной ответ устойчивого варианта — последняя полученная цена и её возраст.
func TestFallbackServesLastPriceWithAge(t *testing.T) {
	t.Setenv("S", "1ms")
	t.Setenv("MODE", "resilient")
	t.Setenv("DEP_TIMEOUT", "50ms")
	dep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("42\n"))
	}))
	t.Setenv("DEP_URL", dep.URL)
	sv := newServer()
	ts := httptest.NewServer(http.HandlerFunc(sv.order))
	defer ts.Close()
	get := func() (*http.Response, string) {
		resp, err := http.Get(ts.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := make([]byte, 16)
		n, _ := resp.Body.Read(b)
		return resp, string(b[:n])
	}
	if resp, body := get(); resp.Header.Get("X-Fallback") != "" || body != "42\n" {
		t.Fatalf("зависимость жива: %q, fallback=%q", body, resp.Header.Get("X-Fallback"))
	}
	dep.Close()
	time.Sleep(30 * time.Millisecond)
	resp, body := get()
	if resp.Header.Get("X-Fallback") != "1" || body != "42\n" {
		t.Fatalf("зависимость лежит: %q, fallback=%q", body, resp.Header.Get("X-Fallback"))
	}
	if age, err := strconv.Atoi(resp.Header.Get("X-Fallback-Age-Ms")); err != nil || age < 30 {
		t.Fatalf("возраст запасного ответа: %q", resp.Header.Get("X-Fallback-Age-Ms"))
	}
}
