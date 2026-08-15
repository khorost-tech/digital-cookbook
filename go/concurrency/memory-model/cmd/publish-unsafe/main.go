// Демо к статье #3 «Модель памяти Go: happens-before»
// серии «Конкурентность в Go» (khorost.tech).
//
// Раздел «Публикация объекта: как читатель видит недостроенный Config».
//
// СЛОМАННЫЙ пример. Одна горутина конструирует *Config и записывает указатель
// в общее незащищённое поле. Другая читает это поле и разыменовывает указатель.
// Синхронизации нет — happens-before между инициализацией полей объекта и его
// чтением не установлен.
//
// Что покажет запуск:
//
//	go run -race .
//	  → WARNING: DATA RACE
//	    Write ...: main.main.func1 (shared = cfg)
//	    Read  ...: main.main       (c := shared)
//
// Даже без -race это опасно: модель памяти Go не гарантирует, что запись полей
// Config (внутри newConfig) станет видимой читателю ДО того, как он увидит сам
// указатель. Читатель может получить ненулевой указатель, но прочитать по нему
// нули/мусор — «частично сконструированный» объект. Правильная публикация —
// в publish_safe.go через atomic.Pointer[Config].
package main

import (
	"fmt"
	"time"
)

// Config — объект, который «публикуем» между горутинами.
type Config struct {
	Endpoint string
	Retries  int
	Ready    bool
}

// shared — незащищённое поле публикации. Тип — обычный указатель без атомика.
var shared *Config

func newConfig() *Config {
	return &Config{
		Endpoint: "https://api.khorost.tech",
		Retries:  3,
		Ready:    true,
	}
}

func main() {
	go func() {
		// Конструируем объект и публикуем указатель БЕЗ синхронизации.
		shared = newConfig() // гонка: незащищённая запись указателя
	}()

	// Ждём появления указателя тем же незащищённым чтением.
	for {
		c := shared // гонка: незащищённое чтение указателя
		if c != nil {
			// Читатель может увидеть ненулевой указатель, но недостроенные поля:
			// Endpoint == "", Retries == 0, Ready == false.
			fmt.Printf("endpoint=%q retries=%d ready=%v\n", c.Endpoint, c.Retries, c.Ready)
			return
		}
		time.Sleep(time.Millisecond)
	}
}
