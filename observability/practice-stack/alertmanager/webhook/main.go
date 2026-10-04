// Приёмник уведомлений Alertmanager для стенда.
//
// Существует ради одного: дать проверке алертов ТОЧНЫЙ критерий доставки. Без
// него единственный способ убедиться, что уведомление ушло, — читать логи
// Alertmanager глазами, а это не критерий.
//
// Заглушка на netcat не годится принципиально: она не отвечает HTTP-ответом, и
// Alertmanager считает доставку неудачной — шесть попыток и явный отказ в логах.
// Проверено на стенде при разведке выполнимости.
package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":9099"
	}

	store := NewStore()
	srv := &http.Server{
		Addr:              addr,
		Handler:           store.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("приёмник алертов слушает %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("приёмник остановлен: %v", err)
	}
}
