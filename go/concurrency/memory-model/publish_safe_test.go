// publish_safe_test.go — тесты к publish_safe.go.
// Раздел «Публикация объекта: как читатель видит недостроенный Config».
//
// Тест публикует Config из одной горутины и читает из другой через
// ConfigHolder. Читатель, увидевший ненулевой указатель, обязан видеть все
// поля инициализированными — иначе фиксируем ошибку. Гонок данных нет:
// вся синхронизация — через atomic.Pointer, поэтому suite под -race не падает.
package memorymodel

import (
	"testing"
	"time"
)

func TestConfigHolderPublishesFullyConstructed(t *testing.T) {
	var h ConfigHolder

	// До первой публикации Load возвращает nil.
	if got := h.Load(); got != nil {
		t.Fatalf("ожидали nil до Publish, получили %+v", got)
	}

	want := &Config{
		Endpoint: "https://api.khorost.tech",
		Retries:  3,
		Ready:    true,
	}

	go func() {
		h.Publish(&Config{
			Endpoint: want.Endpoint,
			Retries:  want.Retries,
			Ready:    want.Ready,
		})
	}()

	// Ждём публикации через атомарный Load (без гонок).
	deadline := time.Now().Add(time.Second)
	var got *Config
	for {
		got = h.Load()
		if got != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Publish не стал виден за отведённое время")
		}
		time.Sleep(time.Millisecond)
	}

	// Раз указатель ненулевой — все поля обязаны быть видны полностью.
	if got.Endpoint != want.Endpoint || got.Retries != want.Retries || got.Ready != want.Ready {
		t.Fatalf("прочитан недостроенный объект: got %+v, want %+v", got, want)
	}
}
