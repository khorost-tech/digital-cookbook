// server.go — конструктор http.Server с ЯВНЫМИ таймаутами и graceful-shutdown.
// http.ListenAndServe(addr, handler) создаёт Server без единого таймаута: такое
// соединение живёт неограниченно долго. Медленный клиент (Slowloris) шлёт байты
// по одному, удерживая коннекты открытыми, и исчерпывает лимит дескрипторов —
// отказ в обслуживании без единого «тяжёлого» запроса. Таймауты ниже это чинят.
package server

import (
	"context"
	"net/http"
	"time"
)

// New собирает *http.Server с осмысленными таймаутами на всех фазах обмена.
// Значения — стартовая точка для сервиса за обратным прокси; под свою нагрузку
// их подбирают, но нулей (значение по умолчанию = «без ограничения») быть не
// должно.
func New(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:    addr,
		Handler: handler,

		// ReadHeaderTimeout — срок на чтение заголовков. Главный барьер против
		// Slowloris: клиент, тянущий заголовки, будет отключён.
		ReadHeaderTimeout: 5 * time.Second,

		// ReadTimeout — полный срок на чтение запроса вместе с телом.
		ReadTimeout: 10 * time.Second,

		// WriteTimeout — срок на запись ответа; отсекает клиентов, читающих
		// ответ по байту.
		WriteTimeout: 10 * time.Second,

		// IdleTimeout — сколько keep-alive соединение ждёт следующего запроса,
		// прежде чем сервер его закроет.
		IdleTimeout: 60 * time.Second,
	}
}

// Shutdown корректно останавливает сервер: перестаёт принимать новые соединения
// и ждёт завершения активных запросов до истечения ctx. Возвращает ошибку
// контекста, если дедлайн наступил раньше, чем запросы дообработались.
func Shutdown(ctx context.Context, srv *http.Server) error {
	return srv.Shutdown(ctx)
}
